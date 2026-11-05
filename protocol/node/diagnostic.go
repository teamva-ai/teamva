package nodeprotocol

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

const diagnosticRedacted = "[REDACTED]"

var (
	diagnosticURL        = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s<>"'\x60]+`)
	diagnosticAuth       = regexp.MustCompile(`(?i)\b((?:proxy[-_ ]?)?authorization["']?\s*[=:]\s*["']?)(?:(?:Bearer|Basic)\s+)?[^\s,;"'\\]+`)
	diagnosticCookie     = regexp.MustCompile(`(?im)\b((?:set[-_ ]?)?cookie["']?\s*[=:]\s*["']?)[^\r\n"']+`)
	diagnosticAssignment = regexp.MustCompile(`(?i)(["']?(?:[a-z0-9]+[-_])*(?:api[-_ ]?key(?:\s+(?:provided|used|value))?|access[-_ ]?key|access[-_ ]?token|refresh[-_ ]?token|id[-_ ]?token|client[-_ ]?secret|secret|password|passwd|pwd|token|connection[-_ ]?string|database[-_ ]?url|dsn)["']?\s*[=:]\s*)("[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}&"'\\]+)`)
	diagnosticToken      = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]{12,}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)
)

func diagnosticSecretKey(key string) bool {
	key = strings.ToLower(key)
	key = strings.NewReplacer("-", "", "_", "", " ", "", ".", "").Replace(key)
	for _, suffix := range []string{"authorization", "cookie", "apikey", "accesskey", "password", "passwd", "secret", "accesstoken", "refreshtoken", "idtoken", "connectionstring", "connectionurl", "databaseurl", "datasourceurl", "credentials"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return key == "token" || key == "pwd" || key == "dsn"
}

// SanitizeDiagnosticText keeps actionable provider/gateway messages but removes
// credentials. It does not inspect the process environment or private files.
func SanitizeDiagnosticText(text string) string {
	text = diagnosticURL.ReplaceAllStringFunc(text, func(raw string) string {
		address := strings.TrimRight(raw, ".,;:)]}")
		suffix := strings.TrimPrefix(raw, address)
		u, err := url.Parse(address)
		if err != nil {
			return "[connection URL omitted]"
		}
		if u.User != nil {
			u.User = url.User(diagnosticRedacted)
		}
		query := u.Query()
		changed := false
		for key := range query {
			if diagnosticSecretKey(key) || key == "key" || key == "auth" || key == "signature" || key == "sig" {
				query.Set(key, diagnosticRedacted)
				changed = true
			}
		}
		if changed {
			u.RawQuery = query.Encode()
		}
		return u.String() + suffix
	})
	text = diagnosticAuth.ReplaceAllString(text, "${1}"+diagnosticRedacted)
	text = diagnosticCookie.ReplaceAllString(text, "${1}"+diagnosticRedacted)
	text = diagnosticAssignment.ReplaceAllString(text, "${1}"+diagnosticRedacted)
	return diagnosticToken.ReplaceAllString(text, diagnosticRedacted)
}

// SanitizeDiagnosticPayload returns detached JSON-compatible diagnostics. Raw
// native errors must pass this boundary BEFORE either runtime or app SQLite.
// Structured credential values are also removed from free-form message echoes.
func SanitizeDiagnosticPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	var secrets []string
	var visit func(any, int)
	visit = func(value any, depth int) {
		if depth > 32 {
			return
		}
		switch v := value.(type) {
		case map[string]any:
			for _, field := range []string{"name", "key"} {
				if name, ok := v[field].(string); ok && diagnosticSecretKey(name) {
					if s, ok := v["value"].(string); ok && len(s) >= 4 {
						secrets = append(secrets, s)
					}
				}
			}
			for key, item := range v {
				if diagnosticSecretKey(key) {
					if s, ok := item.(string); ok && len(s) >= 4 {
						secrets = append(secrets, s)
					}
				}
				visit(item, depth+1)
			}
		case []any:
			for _, item := range v {
				visit(item, depth+1)
			}
		case json.RawMessage:
			var decoded any
			if json.Unmarshal(v, &decoded) == nil {
				visit(decoded, depth+1)
			}
		case map[string]string:
			decoded := make(map[string]any, len(v))
			for key, item := range v {
				decoded[key] = item
			}
			visit(decoded, depth+1)
		}
	}
	visit(payload, 0)
	var clean func(any, int) any
	clean = func(value any, depth int) any {
		if depth > 32 {
			return "[diagnostic nesting omitted]"
		}
		switch v := value.(type) {
		case map[string]any:
			result := make(map[string]any, len(v))
			for key, item := range v {
				if diagnosticSecretKey(key) {
					result[key] = diagnosticRedacted
				} else {
					result[key] = clean(item, depth+1)
				}
			}
			// Header arrays can encode secrets as {name, value} rather than keys.
			for _, field := range []string{"name", "key"} {
				if name, ok := v[field].(string); ok && diagnosticSecretKey(name) {
					if _, ok := v["value"]; ok {
						result["value"] = diagnosticRedacted
					}
				}
			}
			return result
		case []any:
			result := make([]any, len(v))
			for i, item := range v {
				result[i] = clean(item, depth+1)
			}
			return result
		case json.RawMessage:
			var decoded any
			if json.Unmarshal(v, &decoded) == nil {
				return clean(decoded, depth+1)
			}
			return "[invalid diagnostic JSON omitted]"
		case []byte:
			var decoded any
			if json.Unmarshal(v, &decoded) == nil {
				return clean(decoded, depth+1)
			}
			return clean(string(v), depth+1)
		case map[string]string:
			result := make(map[string]any, len(v))
			for key, item := range v {
				result[key] = item
			}
			return clean(result, depth+1)
		case string:
			for _, secret := range secrets {
				v = strings.ReplaceAll(v, secret, diagnosticRedacted)
			}
			return SanitizeDiagnosticText(v)
		case nil, bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
			return value
		default:
			// Typed JSON containers/structs cannot bypass redaction by using a
			// different in-memory representation than native decoded map/[]any.
			raw, err := json.Marshal(value)
			if err != nil {
				return "[unsupported diagnostic value omitted]"
			}
			var decoded any
			if json.Unmarshal(raw, &decoded) != nil {
				return "[invalid diagnostic value omitted]"
			}
			return clean(decoded, depth+1)
		}
	}
	return clean(payload, 0).(map[string]any)
}

// DiagnosticMessage only traverses known diagnostic envelopes, never request
// bodies, headers or arbitrary metadata. The caller should sanitize first.
func DiagnosticMessage(payload map[string]any) string {
	var parts []string
	seen := map[string]bool{}
	add := func(value any) {
		if s, ok := value.(string); ok {
			for _, line := range strings.Split(s, "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !seen[line] {
					parts = append(parts, line)
					seen[line] = true
				}
			}
		}
	}
	var visit func(any, int)
	visit = func(value any, depth int) {
		if depth > 8 {
			return
		}
		switch v := value.(type) {
		case string:
			add(v)
		case []any:
			for index, item := range v {
				if index >= 100 {
					break
				}
				visit(item, depth+1)
			}
		case map[string]any:
			for _, key := range []string{"message", "detail", "details", "additionalDetails", "responseBody"} {
				if s, ok := v[key].(string); ok {
					add(s)
				}
			}
			for _, key := range []string{"error", "data", "cause", "turn", "defect"} {
				visit(v[key], depth+1)
			}
			if response, ok := v["response"].(map[string]any); ok {
				add(response["body"])
			}
			if len(parts) == 0 {
				add(v["reason"])
				add(v["code"])
			}
		}
	}
	visit(payload, 0)
	return strings.Join(parts, "\n")
}

func NormalizeDiagnosticPayload(payload map[string]any) map[string]any {
	result := SanitizeDiagnosticPayload(payload)
	if result == nil {
		result = map[string]any{}
	}
	if message := DiagnosticMessage(result); message != "" {
		result["message"] = message
	}
	return result
}
