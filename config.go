package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/go-sphere/confstore"
	"github.com/go-sphere/confstore/codec"
	"github.com/go-sphere/confstore/provider"
	"github.com/go-sphere/confstore/provider/file"
	"github.com/go-sphere/confstore/provider/http"
	"github.com/tbxark/optional-go"
)

type StdioMCPClientConfig struct {
	Command string            `json:"command"`
	Env     map[string]string `json:"env"`
	Args    []string          `json:"args"`
}

type SSEMCPClientConfig struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type StreamableMCPClientConfig struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Timeout time.Duration     `json:"timeout"`
}

type MCPClientType string

const (
	MCPClientTypeStdio      MCPClientType = "stdio"
	MCPClientTypeSSE        MCPClientType = "sse"
	MCPClientTypeStreamable MCPClientType = "streamable-http"
)

type MCPServerType string

const (
	MCPServerTypeSSE        MCPServerType = "sse"
	MCPServerTypeStreamable MCPServerType = "streamable-http"
)

// ---- V2 ----

type ConflictMode string

const (
	// ConflictModePrefix namespaces every tool/prompt/resource registered by a
	// grouped client so that two backends exposing the same name cannot
	// collide. Tools/prompt names become "<client>.<name>" and resource URIs
	// become "<client>/<uri>"; the proxy transparently strips the namespace
	// before delegating to the backend.
	ConflictModePrefix ConflictMode = "prefix"
	// ConflictModeError registers names verbatim and fails startup if two
	// grouped clients expose the same tool/prompt/resource.
	ConflictModeError ConflictMode = "error"
	// ConflictModeFirstWins registers names verbatim and keeps whichever
	// client registered the name first, silently skipping later duplicates.
	ConflictModeFirstWins ConflictMode = "first-wins"
)

type ToolFilterMode string

const (
	ToolFilterModeAllow ToolFilterMode = "allow"
	ToolFilterModeBlock ToolFilterMode = "block"
)

type ToolFilterConfig struct {
	Mode ToolFilterMode `json:"mode,omitempty"`
	List []string       `json:"list,omitempty"`
}

type OptionsV2 struct {
	PanicIfInvalid optional.Field[bool] `json:"panicIfInvalid"`
	LogEnabled     optional.Field[bool] `json:"logEnabled"`
	AuthTokens     []string             `json:"authTokens,omitempty"`
	ToolFilter     *ToolFilterConfig    `json:"toolFilter,omitempty"`
	Disabled       bool                 `json:"disabled,omitempty"`
}

type MCPProxyConfigV2 struct {
	BaseURL string        `json:"baseURL"`
	Addr    string        `json:"addr"`
	Name    string        `json:"name"`
	Version string        `json:"version"`
	Type    MCPServerType `json:"type,omitempty"`
	Options *OptionsV2    `json:"options,omitempty"`
}

// GroupConfig merges several mcpServers entries under a single URL/route.
// Every entry named in Servers is registered into one shared MCP server
// exposed at path "baseURL/<groupName>/". Entries referenced by a group are
// no longer published on their own standalone route.
type GroupConfig struct {
	Servers      []string     `json:"servers"`
	ConflictMode ConflictMode `json:"conflictMode,omitempty"`
	Options      *OptionsV2   `json:"options,omitempty"`
}

type MCPClientConfigV2 struct {
	TransportType MCPClientType `json:"transportType,omitempty"`

	// Stdio
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// SSE or Streamable HTTP
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Timeout time.Duration     `json:"timeout,omitempty"`

	Options *OptionsV2 `json:"options,omitempty"`
}

func parseMCPClientConfigV2(conf *MCPClientConfigV2) (any, error) {
	if conf.Command != "" || conf.TransportType == MCPClientTypeStdio {
		if conf.Command == "" {
			return nil, errors.New("command is required for stdio transport")
		}
		return &StdioMCPClientConfig{
			Command: conf.Command,
			Env:     conf.Env,
			Args:    conf.Args,
		}, nil
	}
	if conf.URL != "" {
		if conf.TransportType == MCPClientTypeStreamable {
			return &StreamableMCPClientConfig{
				URL:     conf.URL,
				Headers: conf.Headers,
				Timeout: conf.Timeout,
			}, nil
		} else {
			return &SSEMCPClientConfig{
				URL:     conf.URL,
				Headers: conf.Headers,
			}, nil
		}
	}
	return nil, errors.New("invalid server type")
}

// ---- Config ----

type Config struct {
	McpProxy   *MCPProxyConfigV2             `json:"mcpProxy"`
	McpServers map[string]*MCPClientConfigV2 `json:"mcpServers"`
	Groups     map[string]*GroupConfig       `json:"groups,omitempty"`
}

type FullConfig struct {
	DeprecatedServerV1  *MCPProxyConfigV1             `json:"server"`
	DeprecatedClientsV1 map[string]*MCPClientConfigV1 `json:"clients"`

	McpProxy   *MCPProxyConfigV2             `json:"mcpProxy"`
	McpServers map[string]*MCPClientConfigV2 `json:"mcpServers"`
	Groups     map[string]*GroupConfig       `json:"groups,omitempty"`
}

func newConfProvider(path string, insecure, expandEnv bool, httpHeaders string, httpTimeout int) (provider.Provider, error) {
	if http.IsRemoteURL(path) {
		var opts []http.Option
		httpClient := nethttp.DefaultClient
		if insecure {
			transport := nethttp.DefaultTransport.(*nethttp.Transport).Clone()
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			httpClient = &nethttp.Client{Transport: transport}
		}
		if httpTimeout > 0 {
			httpClient.Timeout = time.Duration(httpTimeout) * time.Second
		}
		opts = append(opts, http.WithClient(httpClient))
		if httpHeaders != "" {
			// format: 'Key1:Value1;Key2:Value2'
			headers := make(nethttp.Header)
			for kv := range strings.SplitSeq(httpHeaders, ";") {
				parts := strings.SplitN(kv, ":", 2)
				if len(parts) == 2 {
					key := strings.TrimSpace(parts[0])
					value := strings.TrimSpace(parts[1])
					if key != "" && value != "" {
						headers.Add(key, value)
					}
				}
			}
			if len(headers) > 0 {
				opts = append(opts, http.WithHeaders(headers))
			}
		}
		pro := http.New(path, opts...)
		if expandEnv {
			return provider.NewExpandEnv(pro), nil
		} else {
			return pro, nil
		}
	}
	if file.IsLocalPath(path) {
		if expandEnv {
			return provider.NewExpandEnv(file.New(path, file.WithExpandEnv())), nil
		} else {
			return file.New(path), nil
		}
	}
	return nil, errors.New("unsupported config path")
}

func load(path string, insecure, expandEnv bool, httpHeaders string, httpTimeout int) (*Config, error) {
	pro, err := newConfProvider(path, insecure, expandEnv, httpHeaders, httpTimeout)
	if err != nil {
		return nil, err
	}
	conf, err := confstore.Load[FullConfig](pro, codec.JsonCodec())
	if err != nil {
		return nil, err
	}
	adaptMCPClientConfigV1ToV2(conf)

	if conf.McpProxy == nil {
		return nil, errors.New("mcpProxy is required")
	}
	if conf.McpProxy.Options == nil {
		conf.McpProxy.Options = &OptionsV2{}
	}
	for _, clientConfig := range conf.McpServers {
		if clientConfig.Options == nil {
			clientConfig.Options = &OptionsV2{}
		}
		if clientConfig.Options.AuthTokens == nil {
			clientConfig.Options.AuthTokens = conf.McpProxy.Options.AuthTokens
		}
		if !clientConfig.Options.PanicIfInvalid.Present() {
			clientConfig.Options.PanicIfInvalid = conf.McpProxy.Options.PanicIfInvalid
		}
		if !clientConfig.Options.LogEnabled.Present() {
			clientConfig.Options.LogEnabled = conf.McpProxy.Options.LogEnabled
		}
	}

	if conf.McpProxy.Type == "" {
		conf.McpProxy.Type = MCPServerTypeSSE // default to SSE
	}

	if err := validateAndDefaultGroups(conf); err != nil {
		return nil, err
	}

	return &Config{
		McpProxy:   conf.McpProxy,
		McpServers: conf.McpServers,
		Groups:     conf.Groups,
	}, nil
}

// validateAndDefaultGroups applies inherited option defaults to every group,
// picks a default conflict mode, and verifies that every group references an
// existing mcpServers entry and that no server belongs to more than one group.
func validateAndDefaultGroups(conf *FullConfig) error {
	owner := make(map[string]string, len(conf.Groups))
	for gname, group := range conf.Groups {
		if group.Options == nil {
			group.Options = &OptionsV2{}
		}
		if group.Options.AuthTokens == nil {
			group.Options.AuthTokens = conf.McpProxy.Options.AuthTokens
		}
		if !group.Options.PanicIfInvalid.Present() {
			group.Options.PanicIfInvalid = conf.McpProxy.Options.PanicIfInvalid
		}
		if !group.Options.LogEnabled.Present() {
			group.Options.LogEnabled = conf.McpProxy.Options.LogEnabled
		}
		if group.ConflictMode == "" {
			group.ConflictMode = ConflictModePrefix
		}

		if len(group.Servers) == 0 {
			return fmt.Errorf("group %q has no servers", gname)
		}
		for _, sname := range group.Servers {
			if _, ok := conf.McpServers[sname]; !ok {
				return fmt.Errorf("group %q references unknown server %q", gname, sname)
			}
			if other, dup := owner[sname]; dup {
				return fmt.Errorf("server %q is assigned to multiple groups: %q and %q", sname, other, gname)
			}
			owner[sname] = gname
		}
	}
	// A group is mounted at "<baseURL>/<groupName>/" — the same route shape as a
	// standalone (non-grouped) server with that key. Reject the collision so
	// startup surfaces a clear error instead of an http.ServeMux duplicate
	// registration panic at request time.
	for gname := range conf.Groups {
		if _, exists := conf.McpServers[gname]; exists {
			if _, isGrouped := owner[gname]; !isGrouped {
				return fmt.Errorf("group %q collides with a standalone server of the same name; either add %q to the group's servers or rename the group", gname, gname)
			}
		}
	}
	return nil
}
