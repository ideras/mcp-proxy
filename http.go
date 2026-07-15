package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"golang.org/x/sync/errgroup"
)

type MiddlewareFunc func(http.Handler) http.Handler

func chainMiddleware(h http.Handler, middlewares ...MiddlewareFunc) http.Handler {
	for _, mw := range middlewares {
		h = mw(h)
	}
	return h
}

func newAuthMiddleware(tokens []string) MiddlewareFunc {
	tokenSet := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		tokenSet[token] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(tokens) != 0 {
				token := r.Header.Get("Authorization")
				token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
				if token == "" {
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				if _, ok := tokenSet[token]; !ok {
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func loggerMiddleware(prefix string) MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Printf("<%s> Request [%s] %s", prefix, r.Method, r.URL.Path)
			next.ServeHTTP(w, r)
		})
	}
}

func recoverMiddleware(prefix string) MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					log.Printf("<%s> Recovered from panic: %v", prefix, err)
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func startHTTPServer(config *Config) error {
	baseURL, uErr := url.Parse(config.McpProxy.BaseURL)
	if uErr != nil {
		return uErr
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var errorGroup errgroup.Group
	httpMux := http.NewServeMux()
	httpServer := &http.Server{
		Addr:    config.McpProxy.Addr,
		Handler: httpMux,
	}
	info := mcp.Implementation{
		Name: config.McpProxy.Name,
	}

	// Build the set of clients that belong to a group; those are published
	// only through their group route, never on a standalone route.
	grouped := make(map[string]struct{})
	for _, group := range config.Groups {
		for _, sname := range group.Servers {
			grouped[sname] = struct{}{}
		}
	}

	for name, clientConfig := range config.McpServers {
		name, clientConfig := name, clientConfig
		if _, ok := grouped[name]; ok {
			continue
		}
		if clientConfig.Options.Disabled {
			log.Printf("<%s> Disabled", name)
			continue
		}
		mcpClient, err := newMCPClient(name, clientConfig)
		if err != nil {
			return err
		}
		mcpServer, err := newMCPServer(name, config.McpProxy, clientConfig.Options)
		if err != nil {
			return err
		}
		errorGroup.Go(func() error {
			log.Printf("<%s> Connecting", name)
			addErr := mcpClient.addToMCPServer(ctx, info, mcpServer.mcpServer)
			if addErr != nil {
				log.Printf("<%s> Failed to add client to server: %v", name, addErr)
				if clientConfig.Options.PanicIfInvalid.OrElse(false) {
					return addErr
				}
				return nil
			}
			log.Printf("<%s> Connected", name)
			mountRoute(httpMux, baseURL, name, mcpServer.handler, buildMiddlewares(name, clientConfig.Options))
			httpServer.RegisterOnShutdown(func() {
				log.Printf("<%s> Shutting down", name)
				_ = mcpClient.Close()
			})
			return nil
		})
	}

	for gname, group := range config.Groups {
		gname, group := gname, group
		errorGroup.Go(func() error {
			return registerGroup(ctx, httpMux, httpServer, baseURL, info, config, gname, group)
		})
	}

	go func() {
		err := errorGroup.Wait()
		if err != nil {
			log.Fatalf("Failed to add clients: %v", err)
		}
		log.Printf("All clients initialized")
	}()

	go func() {
		log.Printf("Starting %s server", config.McpProxy.Type)
		log.Printf("%s server listening on %s", config.McpProxy.Type, config.McpProxy.Addr)
		hErr := httpServer.ListenAndServe()
		if hErr != nil && !errors.Is(hErr, http.ErrServerClosed) {
			log.Fatalf("Failed to start server: %v", hErr)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	log.Println("Shutdown signal received")

	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 5*time.Second)
	defer shutdownCancel()

	err := httpServer.Shutdown(shutdownCtx)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// buildMiddlewares assembles the per-route middleware stack for a standalone
// client or a group from its effective options.
func buildMiddlewares(prefix string, opts *OptionsV2) []MiddlewareFunc {
	mw := []MiddlewareFunc{recoverMiddleware(prefix)}
	if opts != nil && opts.LogEnabled.OrElse(false) {
		mw = append(mw, loggerMiddleware(prefix))
	}
	if opts != nil && len(opts.AuthTokens) > 0 {
		mw = append(mw, newAuthMiddleware(opts.AuthTokens))
	}
	return mw
}

// mountRoute mounts a handler at "<baseURL.Path>/<name>/" on the mux after
// wrapping it with the provided middleware.
func mountRoute(mux *http.ServeMux, baseURL *url.URL, name string, handler http.Handler, mw []MiddlewareFunc) {
	route := path.Join(baseURL.Path, name)
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	if !strings.HasSuffix(route, "/") {
		route += "/"
	}
	log.Printf("<%s> Handling requests at %s", name, route)
	mux.Handle(route, chainMiddleware(handler, mw...))
}

// registerGroup merges the listed mcpServers entries into a single shared
// MCPServer exposed at "<baseURL.Path>/<groupName>/". Every member client is
// initialized sequentially against the shared server using a shared collision
// registry; member names are namespaced when the group uses ConflictModePrefix
// so tools/prompts/resources from different backends can coexist.
func registerGroup(
	ctx context.Context,
	mux *http.ServeMux,
	httpServer *http.Server,
	baseURL *url.URL,
	info mcp.Implementation,
	config *Config,
	gname string,
	group *GroupConfig,
) error {
	srv, err := newMCPServer(gname, config.McpProxy, group.Options)
	if err != nil {
		return err
	}
	registry := newNameRegistry()
	var members []*Client
	for _, sname := range group.Servers {
		clientConfig := config.McpServers[sname]
		if clientConfig.Options.Disabled {
			log.Printf("<%s/%s> Disabled", gname, sname)
			continue
		}
		mcpClient, err := newMCPClient(sname, clientConfig)
		if err != nil {
			if clientConfig.Options.PanicIfInvalid.OrElse(false) {
				return err
			}
			log.Printf("<%s/%s> Failed to create client: %v", gname, sname, err)
			continue
		}
		mcpClient.configureAggregate(sname, group.ConflictMode, registry)

		log.Printf("<%s/%s> Connecting", gname, sname)
		addErr := mcpClient.addToMCPServer(ctx, info, srv.mcpServer)
		if addErr != nil {
			var ce *collisionError
			if errors.As(addErr, &ce) {
				return addErr // a duplicate under error mode is a config bug, always fatal
			}
			if clientConfig.Options.PanicIfInvalid.OrElse(false) {
				return addErr
			}
			log.Printf("<%s/%s> Failed to add client to server: %v", gname, sname, addErr)
			continue
		}
		log.Printf("<%s/%s> Connected", gname, sname)
		members = append(members, mcpClient)
	}

	mountRoute(mux, baseURL, gname, srv.handler, buildMiddlewares(gname, group.Options))
	httpServer.RegisterOnShutdown(func() {
		log.Printf("<%s> Shutting down group (%d member)", gname, len(members))
		for _, m := range members {
			_ = m.Close()
		}
	})
	return nil
}
