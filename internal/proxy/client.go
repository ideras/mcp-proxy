package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/yosida95/uritemplate/v3"

	"github.com/tbxark/mcp-proxy/internal/config"
)

type Client struct {
	name            string
	needPing        bool
	needManualStart bool
	client          *client.Client
	options         *config.OptionsV2

	// Aggregate-mode fields. In standalone (per-route) mode `namespace` is empty,
	// `registerMode` is empty and `registry` is nil, so the client registers
	// every tool/prompt/resource verbatim (the original behavior). When the
	// client is a member of a group, `registry` tracks names already claimed on
	// the shared server, and `namespace`/`registerMode` decide how collisions
	// are handled.
	namespace    string
	registerMode config.ConflictMode
	registry     *nameRegistry
}

// nameRegistry remembers the keys (tool/prompt names, resource URIs, resource
// template patterns) already registered on a shared MCPServer so that a group
// can detect collisions between its member clients.
type nameRegistry struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newNameRegistry() *nameRegistry {
	return &nameRegistry{seen: make(map[string]struct{})}
}

// claim returns true when key was newly recorded. A false return means another
// member already registered an item under the same key (a collision).
func (r *nameRegistry) claim(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.seen[key]; ok {
		return false
	}
	r.seen[key] = struct{}{}
	return true
}

// collisionError marks a duplicate name encountered while ConflictMode is
// "error". Such errors are treated as fatal configuration errors regardless
// of PanicIfInvalid.
type collisionError struct {
	kind string
	name string
}

func (e *collisionError) Error() string {
	return fmt.Sprintf("conflict on %s %q in error conflict mode (use \"prefix\" or \"first-wins\" to resolve)", e.kind, e.name)
}

// configureAggregate switches a client into aggregate (grouped) mode. `prefix`
// is the client's group namespace name (its mcpServers key); it is only used
// to namespace names when mode is ConflictModePrefix.
func (c *Client) configureAggregate(namespace string, mode config.ConflictMode, registry *nameRegistry) {
	c.namespace = namespace
	c.registerMode = mode
	c.registry = registry
}

// namespaceActive reports whether this client should namespace the items it
// registers (true only in prefix mode).
func (c *Client) namespaceActive() bool {
	return c.registerMode == config.ConflictModePrefix && c.namespace != ""
}

// applyName returns the registered name for a tool/prompt given the conflict
// mode. In prefix mode the client name is dotted in front of the original name.
func (c *Client) applyName(name string) string {
	if c.namespaceActive() {
		return c.namespace + "-" + name
	}
	return name
}

// applyURI returns the registered URI for a resource/template. In prefix mode
// the client name is prepended as a virtual path segment.
func (c *Client) applyURI(uri string) string {
	if c.namespaceActive() {
		return c.namespace + "/" + uri
	}
	return uri
}

// resourceConflict checks `kind`/`name` against the shared registry and applies
// the configured conflict policy. It returns true when the item may be
// registered, and false when it must be skipped. An error (collisionError) is
// returned for fatal duplicates under ConflictModeError.
func (c *Client) resourceConflict(kind, name string) (bool, error) {
	if c.registry == nil {
		return true, nil // standalone mode never tracks collisions
	}
	var key string
	if kind == "tool" || kind == "prompt" {
		key = c.applyName(name)
	} else {
		key = c.applyURI(name)
	}
	if c.registry.claim(key) {
		return true, nil
	}
	switch c.registerMode {
	case config.ConflictModeError:
		return false, &collisionError{kind: kind, name: name}
	default: // ConflictModePrefix (reuse of own name) and ConflictModeFirstWins
		log.Printf("<%s> Skipping duplicate %s %q (conflict mode: %s)", c.name, kind, name, c.registerMode)
		return false, nil
	}
}

func newMCPClient(name string, conf *config.MCPClientConfigV2) (*Client, error) {
	clientInfo, pErr := config.ParseMCPClientConfigV2(conf)
	if pErr != nil {
		return nil, pErr
	}
	switch v := clientInfo.(type) {
	case *config.StdioMCPClientConfig:
		envs := make([]string, 0, len(v.Env))
		for kk, vv := range v.Env {
			envs = append(envs, fmt.Sprintf("%s=%s", kk, vv))
		}
		mcpClient, err := client.NewStdioMCPClient(v.Command, envs, v.Args...)
		if err != nil {
			return nil, err
		}
		if stdioTransport, ok := mcpClient.GetTransport().(*transport.Stdio); ok {
			if stderr := stdioTransport.Stderr(); stderr != nil {
				go func() {
					scanner := bufio.NewScanner(stderr)
					for scanner.Scan() {
						log.Printf("<%s:stderr> %s", name, scanner.Text())
					}
					if err := scanner.Err(); err != nil {
						if errors.Is(err, bufio.ErrTooLong) {
							log.Printf("<%s:stderr> line too long, falling back to discarding stderr", name)
							_, _ = io.Copy(io.Discard, stderr)
						} else {
							log.Printf("<%s:stderr> error reading stderr: %v", name, err)
						}
					}
				}()
			}
		}

		return &Client{
			name:    name,
			client:  mcpClient,
			options: conf.Options,
		}, nil
	case *config.SSEMCPClientConfig:
		var options []transport.ClientOption
		if len(v.Headers) > 0 {
			options = append(options, client.WithHeaders(v.Headers))
		}
		mcpClient, err := client.NewSSEMCPClient(v.URL, options...)
		if err != nil {
			return nil, err
		}
		return &Client{
			name:            name,
			needPing:        true,
			needManualStart: true,
			client:          mcpClient,
			options:         conf.Options,
		}, nil
	case *config.StreamableMCPClientConfig:
		var options []transport.StreamableHTTPCOption
		if len(v.Headers) > 0 {
			options = append(options, transport.WithHTTPHeaders(v.Headers))
		}
		if v.Timeout > 0 {
			options = append(options, transport.WithHTTPTimeout(v.Timeout))
		}
		mcpClient, err := client.NewStreamableHttpClient(v.URL, options...)
		if err != nil {
			return nil, err
		}
		return &Client{
			name:            name,
			needPing:        true,
			needManualStart: true,
			client:          mcpClient,
			options:         conf.Options,
		}, nil
	}
	return nil, errors.New("invalid client type")
}

func (c *Client) addToMCPServer(ctx context.Context, clientInfo mcp.Implementation, mcpServer *server.MCPServer) error {
	if c.needManualStart {
		err := c.client.Start(ctx)
		if err != nil {
			return err
		}
	}
	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = clientInfo
	initRequest.Params.Capabilities = mcp.ClientCapabilities{
		Experimental: make(map[string]any),
		Roots:        nil,
		Sampling:     nil,
	}
	_, err := c.client.Initialize(ctx, initRequest)
	if err != nil {
		return err
	}
	log.Printf("<%s> Successfully initialized MCP client", c.name)

	err = c.addToolsToServer(ctx, mcpServer)
	if err != nil {
		return err
	}
	// Prompt/resource/template listing is best-effort: many backends do not
	// support one or more of these capabilities and report “method not found”.
	// We deliberately ignore those listing errors in both standalone and
	// aggregate modes (matching the original behavior), with one exception: a
	// collisionError raised by ConflictModeError is a real config bug and must
	// always surface so startup fails.
	for _, addErr := range []error{
		c.addPromptsToServer(ctx, mcpServer),
		c.addResourcesToServer(ctx, mcpServer),
		c.addResourceTemplatesToServer(ctx, mcpServer),
	} {
		if addErr == nil {
			continue
		}
		var ce *collisionError
		if errors.As(addErr, &ce) {
			return addErr
		}
	}

	if c.needPing {
		go c.startPingTask(ctx)
	}
	return nil
}

func (c *Client) startPingTask(ctx context.Context) {
	interval := 30 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	failCount := 0
	for {
		select {
		case <-ctx.Done():
			log.Printf("<%s> Context done, stopping ping", c.name)
			return
		case <-ticker.C:
			if err := c.client.Ping(ctx); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}
				failCount++
				log.Printf("<%s> MCP Ping failed: %v (count=%d)", c.name, err, failCount)
			} else if failCount > 0 {
				log.Printf("<%s> MCP Ping recovered after %d failures", c.name, failCount)
				failCount = 0
			}
		}
	}
}

func (c *Client) addToolsToServer(ctx context.Context, mcpServer *server.MCPServer) error {
	toolsRequest := mcp.ListToolsRequest{}
	filterFunc := func(toolName string) bool {
		return true
	}

	if c.options != nil && c.options.ToolFilter != nil && len(c.options.ToolFilter.List) > 0 {
		filterSet := make(map[string]struct{})
		mode := config.ToolFilterMode(strings.ToLower(string(c.options.ToolFilter.Mode)))
		for _, toolName := range c.options.ToolFilter.List {
			filterSet[toolName] = struct{}{}
		}
		switch mode {
		case config.ToolFilterModeAllow:
			filterFunc = func(toolName string) bool {
				_, inList := filterSet[toolName]
				if !inList {
					log.Printf("<%s> Ignoring tool %s as it is not in allow list", c.name, toolName)
				}
				return inList
			}
		case config.ToolFilterModeBlock:
			filterFunc = func(toolName string) bool {
				_, inList := filterSet[toolName]
				if inList {
					log.Printf("<%s> Ignoring tool %s as it is in block list", c.name, toolName)
				}
				return !inList
			}
		default:
			log.Printf("<%s> Unknown tool filter mode: %s, skipping tool filter", c.name, mode)
		}
	}

	for {
		tools, err := c.client.ListTools(ctx, toolsRequest)
		if err != nil {
			return err
		}
		if tools == nil {
			return fmt.Errorf("<%s> ListTools returned nil response without error", c.name)
		}
		if len(tools.Tools) == 0 {
			break
		}
		log.Printf("<%s> Successfully listed %d tools", c.name, len(tools.Tools))
		for _, tool := range tools.Tools {
			if !filterFunc(tool.Name) {
				continue
			}
			ok, err := c.resourceConflict("tool", tool.Name)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			originalName := tool.Name
			tool.Name = c.applyName(originalName)
			handler := c.client.CallTool
			if c.namespaceActive() {
				handler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					req.Params.Name = originalName
					return c.client.CallTool(ctx, req)
				}
			}
			log.Printf("<%s> Adding tool %s", c.name, tool.Name)
			mcpServer.AddTool(tool, handler)
		}
		if tools.NextCursor == "" {
			break
		}
		toolsRequest.Params.Cursor = tools.NextCursor
	}

	return nil
}

func (c *Client) addPromptsToServer(ctx context.Context, mcpServer *server.MCPServer) error {
	promptsRequest := mcp.ListPromptsRequest{}
	for {
		prompts, err := c.client.ListPrompts(ctx, promptsRequest)
		if err != nil {
			return err
		}
		if prompts == nil {
			return fmt.Errorf("<%s> ListPrompts returned nil response without error", c.name)
		}
		if len(prompts.Prompts) == 0 {
			break
		}
		log.Printf("<%s> Successfully listed %d prompts", c.name, len(prompts.Prompts))
		for _, prompt := range prompts.Prompts {
			ok, err := c.resourceConflict("prompt", prompt.Name)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			originalName := prompt.Name
			prompt.Name = c.applyName(originalName)
			handler := c.client.GetPrompt
			if c.namespaceActive() {
				handler = func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
					req.Params.Name = originalName
					return c.client.GetPrompt(ctx, req)
				}
			}
			log.Printf("<%s> Adding prompt %s", c.name, prompt.Name)
			mcpServer.AddPrompt(prompt, handler)
		}
		if prompts.NextCursor == "" {
			break
		}
		promptsRequest.Params.Cursor = prompts.NextCursor
	}
	return nil
}

func (c *Client) addResourcesToServer(ctx context.Context, mcpServer *server.MCPServer) error {
	resourcesRequest := mcp.ListResourcesRequest{}
	for {
		resources, err := c.client.ListResources(ctx, resourcesRequest)
		if err != nil {
			return err
		}
		if resources == nil {
			return fmt.Errorf("<%s> ListResources returned nil response without error", c.name)
		}
		if len(resources.Resources) == 0 {
			break
		}
		log.Printf("<%s> Successfully listed %d resources", c.name, len(resources.Resources))
		for _, resource := range resources.Resources {
			ok, err := c.resourceConflict("resource", resource.URI)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			originalURI := resource.URI
			resource.URI = c.applyURI(originalURI)
			readHandler := func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
				request.Params.URI = originalURI
				readResource, e := c.client.ReadResource(ctx, request)
				if e != nil {
					return nil, e
				}
				return readResource.Contents, nil
			}
			log.Printf("<%s> Adding resource %s", c.name, resource.Name)
			mcpServer.AddResource(resource, readHandler)
		}
		if resources.NextCursor == "" {
			break
		}
		resourcesRequest.Params.Cursor = resources.NextCursor

	}
	return nil
}

func (c *Client) addResourceTemplatesToServer(ctx context.Context, mcpServer *server.MCPServer) error {
	resourceTemplatesRequest := mcp.ListResourceTemplatesRequest{}
	for {
		resourceTemplates, err := c.client.ListResourceTemplates(ctx, resourceTemplatesRequest)
		if err != nil {
			return err
		}
		if resourceTemplates == nil || len(resourceTemplates.ResourceTemplates) == 0 {
			break
		}
		log.Printf("<%s> Successfully listed %d resource templates", c.name, len(resourceTemplates.ResourceTemplates))
		for _, resourceTemplate := range resourceTemplates.ResourceTemplates {
			if resourceTemplate.URITemplate == nil {
				log.Printf("<%s> Skipping resource template with nil URITemplate", c.name)
				continue
			}
			originalRaw := resourceTemplate.URITemplate.Raw()
			ok, err := c.resourceConflict("resource-template", originalRaw)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			regTemplate := resourceTemplate
			stripPrefix := ""
			if c.namespaceActive() {
				stripPrefix = c.namespace + "/"
				appliedRaw := c.applyURI(originalRaw)
				ut, utErr := uritemplate.New(appliedRaw)
				if utErr != nil {
					log.Printf("<%s> Could not namespace resource template %q: %v (registering verbatim)", c.name, originalRaw, utErr)
					stripPrefix = ""
				} else {
					regTemplate.URITemplate = &mcp.URITemplate{Template: ut}
				}
			}
			readHandler := func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
				if stripPrefix != "" && strings.HasPrefix(request.Params.URI, stripPrefix) {
					request.Params.URI = request.Params.URI[len(stripPrefix):]
				}
				readResource, e := c.client.ReadResource(ctx, request)
				if e != nil {
					return nil, e
				}
				return readResource.Contents, nil
			}
			log.Printf("<%s> Adding resource template %s", c.name, regTemplate.Name)
			mcpServer.AddResourceTemplate(regTemplate, readHandler)
		}
		if resourceTemplates.NextCursor == "" {
			break
		}
		resourceTemplatesRequest.Params.Cursor = resourceTemplates.NextCursor
	}
	return nil
}

func (c *Client) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}
