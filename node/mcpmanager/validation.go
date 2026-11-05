package mcpmanager

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var namePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]{0,126}[a-z0-9])?$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var secretKeyPattern = regexp.MustCompile(`(?i)(secret|password|passwd|token|api[-_]?key|authorization|credential|signature|access[-_]?key|session|cookie|jwt|^sig$|^sid$|^code$)`)
var embeddedCredentialPattern = regexp.MustCompile(`(?i)(\bbearer\s+|\bbasic\s+|(?:authorization|api[-_]?key|password|passwd|client[-_]?secret|access[-_]?token)\s*[:=]\s*\S)`)
var jwtPattern = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)

func validateIdentity(scope, project, name string) error {
	if !namePattern.MatchString(name) || (scope != "user" && scope != "project") || (scope == "user" && project != "") || (scope == "project" && (strings.TrimSpace(project) == "" || len(project) > 256 || strings.ContainsAny(project, "\x00\r\n"))) {
		return ErrInvalid
	}
	return nil
}
func decodeConfig(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > 1024*1024 {
		return nil, ErrInvalid
	}
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	if dec.Decode(&obj) != nil || obj == nil {
		return nil, ErrInvalid
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, ErrInvalid
	}
	return obj, nil
}
func safeURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" || strings.ContainsAny(s, "\x00\r\n") {
		return ErrInvalid
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ErrInvalid
	}
	for k := range q {
		if secretKeyPattern.MatchString(k) || strings.EqualFold(k, "key") || strings.EqualFold(k, "auth") {
			return ErrInvalid
		}
	}
	return nil
}
func publicString(s string) bool {
	return !strings.ContainsAny(s, "\x00\r\n") && !strings.Contains(s, KeepSecret)
}
func validateConfig(c map[string]any) error {
	allowed := map[string]bool{"type": true, "command": true, "cwd": true, "environment": true, "url": true, "headers": true, "oauth": true, "disabled": true, "protocol": true, "codemode": true, "timeout": true}
	for k := range c {
		if !allowed[k] {
			return ErrInvalid
		}
	}
	typ, _ := c["type"].(string)
	if typ != "local" && typ != "remote" {
		return ErrInvalid
	}
	for _, k := range []string{"disabled", "codemode"} {
		if v, ok := c[k]; ok {
			if _, ok = v.(bool); !ok {
				return ErrInvalid
			}
		}
	}
	if v, ok := c["protocol"]; ok && v != "legacy" && v != "auto" && v != "2026-07-28" {
		return ErrInvalid
	}
	if v, ok := c["timeout"]; ok {
		t, ok := v.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		for k, v := range t {
			n, ok := v.(float64)
			if (k != "startup" && k != "catalog" && k != "execution") || !ok || n < 1 || n > 3600000 || n != float64(int64(n)) {
				return ErrInvalid
			}
		}
	}
	for _, k := range []string{"environment", "headers"} {
		if v, ok := c[k]; ok {
			vals, ok := v.(map[string]any)
			if !ok || len(vals) > 256 {
				return ErrInvalid
			}
			for name, v := range vals {
				s, ok := v.(string)
				if !ok || len(s) > 65536 || s == KeepSecret || strings.ContainsRune(s, 0) {
					return ErrInvalid
				}
				if k == "environment" {
					if !envPattern.MatchString(name) {
						return ErrInvalid
					}
				} else {
					if !headerName(name) || strings.ContainsAny(s, "\r\n") {
						return ErrInvalid
					}
					switch strings.ToLower(name) {
					case "host", "content-length", "transfer-encoding", "connection", "cookie", "set-cookie":
						return ErrInvalid
					}
				}
			}
		}
	}
	if typ == "local" {
		args, ok := c["command"].([]any)
		if !ok || len(args) == 0 || len(args) > 256 {
			return ErrInvalid
		}
		for i, v := range args {
			s, ok := v.(string)
			if !ok || !publicString(s) || len(s) > 8192 || embeddedCredentialPattern.MatchString(s) || jwtPattern.MatchString(s) || (i == 0 && strings.TrimSpace(s) == "") {
				return ErrInvalid
			}
			if strings.HasPrefix(s, "-") && secretKeyPattern.MatchString(strings.SplitN(s, "=", 2)[0]) {
				return ErrInvalid
			}
			if strings.Contains(s, "=") && secretKeyPattern.MatchString(strings.SplitN(s, "=", 2)[0]) {
				return ErrInvalid
			}
			if strings.Contains(s, "://") && safeURL(s) != nil {
				return ErrInvalid
			}
		}
		if v, ok := c["cwd"]; ok {
			s, ok := v.(string)
			if !ok || !filepath.IsAbs(s) || !publicString(s) {
				return ErrInvalid
			}
		}
		for _, k := range []string{"url", "headers", "oauth"} {
			if _, ok := c[k]; ok {
				return ErrInvalid
			}
		}
	} else {
		s, ok := c["url"].(string)
		if !ok || safeURL(s) != nil {
			return ErrInvalid
		}
		for _, k := range []string{"command", "cwd"} {
			if _, ok := c[k]; ok {
				return ErrInvalid
			}
		}
	}
	if v, ok := c["oauth"]; ok && v != false {
		o, ok := v.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		for k, v := range o {
			switch k {
			case "client_id", "client_secret", "scope":
				s, ok := v.(string)
				if !ok || !publicString(s) {
					return ErrInvalid
				}
			case "redirect_uri", "auth_server_metadata_url":
				s, ok := v.(string)
				if !ok || safeURL(s) != nil {
					return ErrInvalid
				}
				if k == "redirect_uri" {
					u, _ := url.Parse(s)
					if u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.RawQuery != "" {
						return ErrInvalid
					}
				}
			case "callback_port":
				n, ok := v.(float64)
				if !ok || n < 1 || n > 65535 || n != float64(int64(n)) {
					return ErrInvalid
				}
			default:
				return ErrInvalid
			}
		}
	}
	return nil
}
func headerName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", c) {
			return false
		}
	}
	return true
}
func maskSecrets(c map[string]any) {
	for _, k := range []string{"environment", "headers"} {
		if v, ok := c[k].(map[string]any); ok {
			for name := range v {
				v[name] = KeepSecret
			}
		}
	}
	if o, ok := c["oauth"].(map[string]any); ok {
		if _, ok := o["client_secret"]; ok {
			o["client_secret"] = KeepSecret
		}
	}
}
func restoreSecrets(c, old map[string]any) error {
	for _, k := range []string{"environment", "headers", "oauth"} {
		vals, ok := c[k].(map[string]any)
		if !ok {
			continue
		}
		prev, _ := old[k].(map[string]any)
		for name, v := range vals {
			if v == KeepSecret {
				if k == "oauth" && name != "client_secret" {
					return ErrInvalid
				}
				p, ok := prev[name]
				if !ok {
					return ErrInvalid
				}
				vals[name] = p
			}
		}
	}
	return nil
}
func redactKnown(b []byte, e *entry) []byte {
	c, _ := decodeConfig(e.Config)
	var secrets []string
	for _, k := range []string{"environment", "headers"} {
		vals, _ := c[k].(map[string]any)
		for _, v := range vals {
			if s, ok := v.(string); ok && s != "" {
				secrets = append(secrets, s)
			}
		}
	}
	if o, ok := c["oauth"].(map[string]any); ok {
		if s, ok := o["client_secret"].(string); ok && s != "" {
			secrets = append(secrets, s)
		}
	}
	if e.Token != nil {
		secrets = append(secrets, e.Token.Token.AccessToken, e.Token.Token.RefreshToken, e.Token.ClientSecret)
	}
	var data any
	if json.Unmarshal(b, &data) != nil {
		return []byte(`{}`)
	}
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case string:
			for _, s := range secrets {
				if s != "" {
					x = strings.ReplaceAll(x, s, KeepSecret)
				}
			}
			return x
		case []any:
			for i := range x {
				x[i] = clean(x[i])
			}
		case map[string]any:
			for k, val := range x {
				x[k] = clean(val)
			}
		}
		return v
	}
	out, err := json.Marshal(clean(data))
	if err != nil {
		return []byte(`{}`)
	}
	return out
}
