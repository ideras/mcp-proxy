package main

import (
	"strings"
	"testing"

	"github.com/tbxark/optional-go"
)

func newProxyForTest() *MCPProxyConfigV2 {
	return &MCPProxyConfigV2{
		BaseURL: "https://mcp.example.com",
		Addr:    ":9090",
		Name:    "MCP Proxy",
		Version: "1.0.0",
		Options: &OptionsV2{
			AuthTokens:     []string{"Default"},
			PanicIfInvalid: optional.Field[bool]{},
			LogEnabled:     optional.Field[bool]{},
		},
	}
}

func stdioClient() *MCPClientConfigV2 {
	return &MCPClientConfigV2{
		TransportType: MCPClientTypeStdio,
		Command:       "echo",
		Args:          []string{"hi"},
		Options:       &OptionsV2{},
	}
}

func TestValidateGroupsDefaults(t *testing.T) {
	conf := &FullConfig{
		McpProxy: newProxyForTest(),
		McpServers: map[string]*MCPClientConfigV2{
			"github": stdioClient(),
			"fetch":  stdioClient(),
			"amap":   stdioClient(),
		},
		Groups: map[string]*GroupConfig{
			"all": {
				Servers: []string{"github", "fetch"},
				Options: &OptionsV2{LogEnabled: optional.NewField(true)},
			},
		},
	}

	if err := validateAndDefaultGroups(conf); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	g := conf.Groups["all"]
	if g.ConflictMode != ConflictModePrefix {
		t.Fatalf("default conflict mode = %q, want %q", g.ConflictMode, ConflictModePrefix)
	}
	// authTokens should inherit from proxy when unset
	if len(g.Options.AuthTokens) != 1 || g.Options.AuthTokens[0] != "Default" {
		t.Fatalf("group authTokens not inherited: %v", g.Options.AuthTokens)
	}
	// explicit group logEnabled should be preserved
	if !g.Options.LogEnabled.OrElse(false) {
		t.Fatalf("group logEnabled should be preserved as true")
	}
}

func TestValidateGroupsUnknownServer(t *testing.T) {
	conf := &FullConfig{
		McpProxy: newProxyForTest(),
		McpServers: map[string]*MCPClientConfigV2{
			"github": stdioClient(),
		},
		Groups: map[string]*GroupConfig{
			"all": {Servers: []string{"github", "nope"}},
		},
	}
	err := validateAndDefaultGroups(conf)
	if err == nil {
		t.Fatal("unknown server reference should be rejected")
	}
	if !strings.Contains(err.Error(), "unknown server") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error message should mention unknown server \"nope\": %v", err)
	}
}

func TestValidateGroupsServerInMultipleGroups(t *testing.T) {
	conf := &FullConfig{
		McpProxy: newProxyForTest(),
		McpServers: map[string]*MCPClientConfigV2{
			"github": stdioClient(),
		},
		Groups: map[string]*GroupConfig{
			"a": {Servers: []string{"github"}},
			"b": {Servers: []string{"github"}},
		},
	}
	err := validateAndDefaultGroups(conf)
	if err == nil {
		t.Fatal("a server in multiple groups should be rejected")
	}
	if !strings.Contains(err.Error(), "multiple groups") {
		t.Fatalf("error should mention multiple groups: %v", err)
	}
}

func TestValidateGroupsNameCollidesWithStandaloneServer(t *testing.T) {
	conf := &FullConfig{
		McpProxy: newProxyForTest(),
		McpServers: map[string]*MCPClientConfigV2{
			"github": stdioClient(),
			"all":    stdioClient(), // standalone route /all/ would collide with group /all/
		},
		Groups: map[string]*GroupConfig{
			"all": {Servers: []string{"github"}},
		},
	}
	err := validateAndDefaultGroups(conf)
	if err == nil {
		t.Fatal("group name colliding with a standalone server should be rejected")
	}
	if !strings.Contains(err.Error(), "collides with a standalone server") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A group whose name matches one of its own members is allowed: that member
// is grouped (no standalone route), so there is no route collision.
func TestValidateGroupsNameMatchingMemberIsAllowed(t *testing.T) {
	conf := &FullConfig{
		McpProxy: newProxyForTest(),
		McpServers: map[string]*MCPClientConfigV2{
			"all":    stdioClient(),
			"github": stdioClient(),
		},
		Groups: map[string]*GroupConfig{
			"all": {Servers: []string{"all", "github"}},
		},
	}
	if err := validateAndDefaultGroups(conf); err != nil {
		t.Fatalf("group name matching its own member should be allowed: %v", err)
	}
}

func TestValidateGroupsEmptyServers(t *testing.T) {
	conf := &FullConfig{
		McpProxy:   newProxyForTest(),
		McpServers: map[string]*MCPClientConfigV2{"github": stdioClient()},
		Groups: map[string]*GroupConfig{
			"all": {Servers: nil},
		},
	}
	if err := validateAndDefaultGroups(conf); err == nil {
		t.Fatal("group with no servers should be rejected")
	}
}
