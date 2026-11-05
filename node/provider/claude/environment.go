package claude

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var projectedEnvironment = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL", "CLAUDE_CODE_EFFORT_LEVEL",
	"CLAUDE_CODE_AUTO_COMPACT_WINDOW",
}

var compactWindowTokens = regexp.MustCompile(`^[1-9][0-9]{5,6}$`)

// Only API env values are read; no OAuth/keychain access, helper command, user
// hooks, shell env, project settings, or arbitrary env injection is supported.
func accountEnvironment() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.New("resolve claude authentication home failed")
	}
	path := filepath.Join(home, ".claude", "settings.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1<<20 {
		return nil, errors.New("claude account settings must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("read claude account settings failed")
	}
	defer file.Close()
	var settings struct {
		Env map[string]json.RawMessage `json:"env"`
	}
	if err := json.NewDecoder(io.LimitReader(file, (1<<20)+1)).Decode(&settings); err != nil {
		return nil, errors.New("invalid claude account settings")
	}
	values := map[string]string{}
	for _, name := range projectedEnvironment {
		var value string
		raw := settings.Env[name]
		if len(raw) != 0 && json.Unmarshal(raw, &value) != nil {
			return nil, errors.New("invalid claude account environment value")
		}
		if len(value) > 16384 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("invalid claude account environment value")
		}
		if name == "CLAUDE_CODE_AUTO_COMPACT_WINDOW" && len(raw) != 0 {
			// Official env semantics are literal token counts (unlike the
			// /autocompact command's bare 100..1000 shorthand). Intentionally
			// accept only canonical decimal integers in the documented 100k..1M
			// range, never CLI clamp/ignore behavior, units, floats or arbitrary env.
			tokens, err := strconv.ParseUint(value, 10, 32)
			if !compactWindowTokens.MatchString(value) || err != nil || tokens < 100000 || tokens > 1000000 {
				return nil, errors.New("invalid claude auto-compact window: expected an integer token count from 100000 to 1000000")
			}
		}
		if value != "" {
			values[name] = value
		}
	}
	if base := values["ANTHROPIC_BASE_URL"]; base != "" {
		u, err := url.Parse(base)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
			return nil, errors.New("invalid claude Anthropic endpoint")
		}
	}
	return values, nil
}

func (c *Client) environment(account map[string]string) []string {
	values := map[string]string{
		"HOME": filepath.Join(c.dataDir, "home"), "CLAUDE_CONFIG_DIR": filepath.Join(c.dataDir, "claude"),
		"TMPDIR": filepath.Join(c.dataDir, "tmp"), "TMP": filepath.Join(c.dataDir, "tmp"), "TEMP": filepath.Join(c.dataDir, "tmp"),
		"CLAUDE_CODE_ENTRYPOINT": "sdk-ts", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_AUTOUPDATER": "1",
	}
	// Do not inherit API credentials, NODE_OPTIONS, shell initializers, proxy
	// credentials, custom CA commands, XDG or any CLAUDE/ANTHROPIC setting.
	for _, name := range []string{"PATH", "SystemRoot", "WINDIR", "LANG", "LC_ALL"} {
		if value := os.Getenv(name); value != "" {
			values[name] = value
		}
	}
	for name, value := range account {
		values[name] = value
	}
	// --bare intentionally ignores AUTH_TOKEN in current official Claude Code.
	// Preserve the user's Anthropic-compatible credential without weakening the
	// isolation mode: project the token as API_KEY in the child only, never both.
	if values["ANTHROPIC_API_KEY"] == "" && values["ANTHROPIC_AUTH_TOKEN"] != "" {
		values["ANTHROPIC_API_KEY"] = values["ANTHROPIC_AUTH_TOKEN"]
	}
	delete(values, "ANTHROPIC_AUTH_TOKEN")
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

var secretPattern = regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9_.~+/=-]+|\b(sk-(?:ant-)?[a-z0-9_-]{8,})\b|((?:api[_-]?key|auth[_-]?token|authorization|password)\s*["']?\s*[:=]\s*["']?)(?:bearer\s+)?[^\s,}"']+`)
var sensitiveKey = regexp.MustCompile(`(?i)^(api.?key|auth.?token|authorization|password|secret|cookie)$`)

type redactor struct{ secrets []string }

func newRedactor(account map[string]string) redactor {
	r := redactor{}
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if value := account[name]; value != "" {
			r.secrets = append(r.secrets, value)
		}
	}
	return r
}

func (r redactor) containsCredential(value any) bool {
	switch v := value.(type) {
	case string:
		for _, secret := range r.secrets {
			if strings.Contains(v, secret) {
				return true
			}
		}
	case map[string]any:
		for key, item := range v {
			if r.containsCredential(key) || r.containsCredential(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if r.containsCredential(item) {
				return true
			}
		}
	}
	return false
}

func (r redactor) text(value string) string {
	for _, secret := range r.secrets {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	value = secretPattern.ReplaceAllStringFunc(value, func(s string) string {
		match := secretPattern.FindStringSubmatch(s)
		if match[1] != "" {
			return match[1] + "[REDACTED]"
		}
		if match[3] != "" {
			return match[3] + "[REDACTED]"
		}
		return "[REDACTED]"
	})
	return value
}
func (r redactor) value(value any) any {
	switch v := value.(type) {
	case string:
		return r.text(v)
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			if sensitiveKey.MatchString(key) {
				result[key] = "[REDACTED]"
			} else {
				result[key] = r.value(item)
			}
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = r.value(item)
		}
		return result
	default:
		return value
	}
}
