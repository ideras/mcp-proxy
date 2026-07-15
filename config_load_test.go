package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempConfig(t *testing.T, body string) (path string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

func TestLoadAcceptsGroupsConfig(t *testing.T) {
	p := writeTempConfig(t, `{
		"mcpProxy": {
			"baseURL": "https://mcp.example.com",
			"addr": ":9090",
			"name": "MCP Proxy",
			"version": "1.0.0",
			"type": "streamable-http",
			"options": { "logEnabled": true, "authTokens": ["Default"] }
		},
		"mcpServers": {
			"github": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"] },
			"fetch":  { "command": "uvx", "args": ["mcp-server-fetch"] }
		},
		"groups": {
			"all": { "servers": ["github", "fetch"], "conflictMode": "error" }
		}
	}`)
	cfg, err := load(p, false, false, "", 10)
	if err != nil {
		t.Fatalf("load with groups failed: %v", err)
	}
	if cfg.Groups == nil || cfg.Groups["all"] == nil {
		t.Fatal("group \"all\" not present after load")
	}
	if cfg.Groups["all"].ConflictMode != ConflictModeError {
		t.Fatalf("conflictMode = %q, want %q", cfg.Groups["all"].ConflictMode, ConflictModeError)
	}
	// Group options should have inherited authTokens from the proxy.
	if len(cfg.Groups["all"].Options.AuthTokens) != 1 || cfg.Groups["all"].Options.AuthTokens[0] != "Default" {
		t.Fatalf("group authTokens not inherited: %v", cfg.Groups["all"].Options.AuthTokens)
	}
}

func TestLoadStillAcceptsConfigWithoutGroups(t *testing.T) {
	// Existing configs (no groups) must continue to load unchanged.
	p := writeTempConfig(t, `{
		"mcpProxy": {
			"baseURL": "https://mcp.example.com",
			"addr": ":9090",
			"name": "MCP Proxy",
			"version": "1.0.0",
			"type": "sse",
			"options": { "logEnabled": true, "authTokens": ["Default"] }
		},
		"mcpServers": {
			"fetch": { "command": "uvx", "args": ["mcp-server-fetch"] }
		}
	}`)
	cfg, err := load(p, false, false, "", 10)
	if err != nil {
		t.Fatalf("load without groups failed: %v", err)
	}
	if len(cfg.Groups) != 0 {
		t.Fatalf("expected no groups, got %d", len(cfg.Groups))
	}
	if cfg.McpServers["fetch"] == nil {
		t.Fatal("mcpServers entry missing after load")
	}
}

func TestLoadRejectsInvalidGroup(t *testing.T) {
	p := writeTempConfig(t, `{
		"mcpProxy": {
			"baseURL": "https://mcp.example.com", "addr": ":9090", "name": "MCP Proxy", "version": "1.0.0"
		},
		"mcpServers": {
			"github": { "command": "npx", "args": ["x"] }
		},
		"groups": {
			"all": { "servers": ["github", "nope"] }
		}
	}`)
	if _, err := load(p, false, false, "", 10); err == nil {
		t.Fatal("load should reject group referencing unknown server")
	}
}
