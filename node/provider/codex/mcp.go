package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/teamva-ai/teamva/node/provider"
)

// Managed entries use the OpenCode V2 wire shape. Translate only fields that
// Codex actually implements; protocol/codemode/catalog timeout are not portable.
type managedMCPEntry struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	CWD         string            `json:"cwd"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	Disabled    bool              `json:"disabled"`
	Timeout     struct {
		Startup   int64 `json:"startup"`
		Execution int64 `json:"execution"`
	} `json:"timeout"`
	OAuth json.RawMessage `json:"oauth"`
}

type managedMCPOAuth struct {
	// Codex 0.163.0 explicitly supports these fields in McpServerOAuthConfig:
	// https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/core/config.schema.json
	// redirect_uri is our input name; the native TOML name is callback_url.
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Scope        string `json:"scope"`
	CallbackPort int    `json:"callback_port"`
	RedirectURI  string `json:"redirect_uri"`
}

// JSON's string escapes are valid TOML basic-string escapes except for the
// optional escaped slash (encoding/json never emits it). Unlike strconv.Quote,
// this never emits Go-only \xNN/\a/\v escapes for control characters.
func tomlString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func tomlStrings(values []string) string {
	encoded := make([]string, len(values))
	for i, value := range values {
		encoded[i] = tomlString(value)
	}
	return "[" + strings.Join(encoded, ", ") + "]"
}

func writeMCPConfig(cfg *bytes.Buffer, config provider.InstanceConfig) error {
	servers := make(map[string]json.RawMessage)
	if !config.ReadOnly && !config.DisableTools {
		for name, raw := range config.MCPServers {
			if name != "open-computer-use" {
				servers[name] = raw
			}
		}
	}
	if config.ComputerUse && !config.DisableTools {
		servers["open-computer-use"] = json.RawMessage(`{"type":"local","command":["open-computer-use","mcp"]}`)
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var entry managedMCPEntry
		if json.Unmarshal(servers[name], &entry) != nil ||
			(entry.Type != "local" && entry.Type != "remote") ||
			(entry.Type == "local" && (len(entry.Command) == 0 || entry.Command[0] == "")) ||
			(entry.Type == "remote" && entry.URL == "") {
			return errors.New("invalid managed MCP server configuration")
		}
		section := "mcp_servers." + tomlString(name)
		cfg.WriteString("\n[" + section + "]\n")
		cfg.WriteString("enabled = " + strconv.FormatBool(!entry.Disabled) + "\n")
		if entry.Type == "local" {
			cfg.WriteString("command = " + tomlString(entry.Command[0]) + "\n")
			cfg.WriteString("args = " + tomlStrings(entry.Command[1:]) + "\n")
			if entry.CWD != "" {
				cfg.WriteString("cwd = " + tomlString(entry.CWD) + "\n")
			}
		} else {
			cfg.WriteString("url = " + tomlString(entry.URL) + "\n")
		}
		if entry.Timeout.Startup > 0 {
			cfg.WriteString("startup_timeout_sec = " + strconv.FormatFloat(float64(entry.Timeout.Startup)/1000, 'f', -1, 64) + "\n")
		}
		if entry.Timeout.Execution > 0 {
			cfg.WriteString("tool_timeout_sec = " + strconv.FormatFloat(float64(entry.Timeout.Execution)/1000, 'f', -1, 64) + "\n")
		}
		if entry.Type == "remote" && len(entry.OAuth) > 0 && string(entry.OAuth) != "false" {
			var oauth managedMCPOAuth
			if json.Unmarshal(entry.OAuth, &oauth) != nil {
				return errors.New("invalid managed MCP OAuth configuration")
			}
			if oauth.Scope != "" {
				cfg.WriteString("scopes = " + tomlStrings(strings.Fields(oauth.Scope)) + "\n")
			}
			if oauth.ClientID != "" || oauth.CallbackPort != 0 || oauth.RedirectURI != "" {
				cfg.WriteString("\n[" + section + ".oauth]\n")
				for _, field := range []struct{ key, value string }{{"client_id", oauth.ClientID}, {"client_secret", oauth.ClientSecret}, {"callback_url", oauth.RedirectURI}} {
					if field.value != "" {
						cfg.WriteString(field.key + " = " + tomlString(field.value) + "\n")
					}
				}
				if oauth.CallbackPort != 0 {
					cfg.WriteString("callback_port = " + strconv.Itoa(oauth.CallbackPort) + "\n")
				}
			}
		}
		values, suffix := entry.Environment, "env"
		if entry.Type == "remote" {
			values, suffix = entry.Headers, "http_headers"
		}
		if len(values) > 0 {
			cfg.WriteString("\n[" + section + "." + suffix + "]\n")
			keys := make([]string, 0, len(values))
			for key := range values {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				cfg.WriteString(tomlString(key) + " = " + tomlString(values[key]) + "\n")
			}
		}
	}
	return nil
}
