package proxy

import (
	"fmt"
	"net/http"

	"github.com/mark3labs/mcp-go/server"

	"github.com/tbxark/mcp-proxy/internal/config"
)

// Server wraps the outward-facing MCP server exposed to callers of the proxy,
// as opposed to Client which wraps a connection to an upstream MCP backend.
type Server struct {
	tokens    []string
	mcpServer *server.MCPServer
	handler   http.Handler
}

func newMCPServer(name string, serverConfig *config.MCPProxyConfigV2, opts *config.OptionsV2) (*Server, error) {
	if opts == nil {
		opts = &config.OptionsV2{}
	}
	serverOpts := []server.ServerOption{
		server.WithResourceCapabilities(true, true),
		server.WithRecovery(),
	}

	if opts.LogEnabled.OrElse(false) {
		serverOpts = append(serverOpts, server.WithLogging())
	}
	mcpServer := server.NewMCPServer(
		name,
		serverConfig.Version,
		serverOpts...,
	)

	var handler http.Handler

	switch serverConfig.Type {
	case config.MCPServerTypeSSE:
		handler = server.NewSSEServer(
			mcpServer,
			server.WithStaticBasePath(name),
			server.WithBaseURL(serverConfig.BaseURL),
		)
	case config.MCPServerTypeStreamable:
		handler = server.NewStreamableHTTPServer(
			mcpServer,
			server.WithStateLess(true),
		)
	default:
		return nil, fmt.Errorf("unknown server type: %s", serverConfig.Type)
	}
	srv := &Server{
		mcpServer: mcpServer,
		handler:   handler,
	}

	if len(opts.AuthTokens) > 0 {
		srv.tokens = opts.AuthTokens
	}

	return srv, nil
}
