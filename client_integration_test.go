package main

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// backendMCP builds an in-process MCP server that exposes a single tool named
// "search" whose CallTool result echoes `marker` as text.
func backendMCP(t *testing.T, marker string) (*client.Client, *server.MCPServer) {
	t.Helper()
	srv := server.NewMCPServer("backend-"+marker, "1.0.0", server.WithResourceCapabilities(true, true))
	srv.AddTool(
		mcp.NewTool("search", mcp.WithDescription("search tool")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Type: "text", Text: marker}},
			}, nil
		},
	)
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	return c, srv
}

// TestAggregatePrefixNamespacesAndDispatches wires two backends that both
// expose a tool named "search" into one shared MCPServer through the proxy's
// own Client.addToMCPServer, and verifies:
//   - both register namespaced (alpha.search, beta.search) without colliding,
//   - a CallTool for each name dispatches to the correct backend handler.
//
// This exercises the real mcp-go AddTool + wrapped-handler path end to end.
func TestAggregatePrefixNamespacesAndDispatches(t *testing.T) {
	alphaClient, alphaBackend := backendMCP(t, "ALPHA")
	defer alphaClient.Close() //nolint:errcheck
	betaClient, betaBackend := backendMCP(t, "BETA")
	defer betaClient.Close() //nolint:errcheck
	_ = alphaBackend
	_ = betaBackend

	merged := server.NewMCPServer("merged", "1.0.0", server.WithResourceCapabilities(true, true))
	registry := newNameRegistry()
	info := mcp.Implementation{Name: "proxy-test"}

	alpha := &Client{name: "alpha", client: alphaClient, options: &OptionsV2{}}
	alpha.configureAggregate("alpha", ConflictModePrefix, registry)
	if err := alpha.addToMCPServer(context.Background(), info, merged); err != nil {
		t.Fatalf("alpha addToMCPServer: %v", err)
	}

	beta := &Client{name: "beta", client: betaClient, options: &OptionsV2{}}
	beta.configureAggregate("beta", ConflictModePrefix, registry)
	if err := beta.addToMCPServer(context.Background(), info, merged); err != nil {
		t.Fatalf("beta addToMCPServer: %v", err)
	}

	// Inspect the merged server as a client would.
	probe, err := client.NewInProcessClient(merged)
	if err != nil {
		t.Fatalf("NewInProcessClient(merged): %v", err)
	}
	defer probe.Close() //nolint:errcheck
	if err := probe.Start(context.Background()); err != nil {
		t.Fatalf("probe start: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = info
	if _, err := probe.Initialize(context.Background(), initReq); err != nil {
		t.Fatalf("probe initialize: %v", err)
	}

	listed, err := probe.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{"alpha-search": false, "beta-search": false}
	for _, tool := range listed.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected namespaced tool %q in merged server, not found", name)
		}
	}

	// Dispatch: alpha.search -> ALPHA, beta.search -> BETA.
	names := map[string]string{
		"alpha-search": "ALPHA",
		"beta-search":  "BETA",
	}
	for name, expect := range names {
		call := mcp.CallToolRequest{}
		call.Params.Name = name
		res, err := probe.CallTool(context.Background(), call)
		if err != nil {
			t.Fatalf("CallTool(%s): %v", name, err)
		}
		if len(res.Content) == 0 {
			t.Fatalf("CallTool(%s): empty content", name)
		}
		var text string
		switch content := res.Content[0].(type) {
		case *mcp.TextContent:
			text = content.Text
		case mcp.TextContent:
			text = content.Text
		default:
			t.Fatalf("CallTool(%s): first content is %T, want TextContent", name, res.Content[0])
		}
		if text != expect {
			t.Errorf("CallTool(%s) routed to wrong backend: got %q want %q", name, text, expect)
		}
	}
}

// TestAggregateErrorModeFailsOnCollision verifies that two backends exposing
// the same tool name under ConflictModeError abort registration with a
// collisionError (the proxy must not silently overwrite one with the other).
func TestAggregateErrorModeFailsOnCollision(t *testing.T) {
	alphaClient, _ := backendMCP(t, "ALPHA")
	defer alphaClient.Close() //nolint:errcheck
	betaClient, _ := backendMCP(t, "BETA")
	defer betaClient.Close() //nolint:errcheck

	merged := server.NewMCPServer("merged", "1.0.0", server.WithResourceCapabilities(true, true))
	registry := newNameRegistry()
	info := mcp.Implementation{Name: "proxy-test"}

	alpha := &Client{name: "alpha", client: alphaClient, options: &OptionsV2{}}
	alpha.configureAggregate("alpha", ConflictModeError, registry)
	if err := alpha.addToMCPServer(context.Background(), info, merged); err != nil {
		t.Fatalf("alpha addToMCPServer: %v", err)
	}

	beta := &Client{name: "beta", client: betaClient, options: &OptionsV2{}}
	beta.configureAggregate("beta", ConflictModeError, registry)
	err := beta.addToMCPServer(context.Background(), info, merged)
	if err == nil {
		t.Fatal("beta should have failed to register a duplicate \"search\" tool in error mode")
	}
}

// TestStandaloneRegistersVerbatim confirms that a non-grouped (standalone)
// client still registers tool names verbatim, preserving the original behavior.
func TestStandaloneRegistersVerbatim(t *testing.T) {
	backendClient, _ := backendMCP(t, "ALPHA")
	defer backendClient.Close() //nolint:errcheck

	merged := server.NewMCPServer("merged", "1.0.0", server.WithResourceCapabilities(true, true))
	info := mcp.Implementation{Name: "proxy-test"}

	standalone := &Client{name: "alpha", client: backendClient, options: &OptionsV2{}}
	// no configureAggregate -> standalone mode (registry nil, namespace "")
	if err := standalone.addToMCPServer(context.Background(), info, merged); err != nil {
		t.Fatalf("addToMCPServer: %v", err)
	}

	probe, err := client.NewInProcessClient(merged)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	defer probe.Close() //nolint:errcheck
	if err := probe.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = info
	if _, err := probe.Initialize(context.Background(), initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	listed, err := probe.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	found := false
	for _, tool := range listed.Tools {
		if tool.Name == "search" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("standalone client should register the tool as \"search\" (verbatim), not found")
	}
}
