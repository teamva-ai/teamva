package claude

import (
	"strings"
	"unicode/utf8"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

const maxDiagnosticTextBytes = 64 << 10

// secretPieces withholds any unresolved known-credential prefix. A later
// snapshot cannot retract a delta already committed to the host database.
// The held state is bounded by the longest projected credential (<=16 KiB).
type secretPieces struct {
	secrets []string
	pending string
}

func (f *secretPieces) push(text string, final bool) string {
	text = f.pending + text
	f.pending = ""
	var safe strings.Builder
	for pos := 0; pos < len(text); {
		remaining := text[pos:]
		matched := 0
		for _, secret := range f.secrets {
			if strings.HasPrefix(remaining, secret) && len(secret) > matched {
				matched = len(secret)
			}
		}
		if matched > 0 {
			safe.WriteString("[REDACTED]")
			pos += matched
			continue
		}
		partial := false
		for _, secret := range f.secrets {
			if strings.HasPrefix(secret, remaining) {
				partial = true
				break
			}
		}
		if partial {
			if final {
				safe.WriteString("[REDACTED]")
			} else {
				f.pending = remaining
			}
			break
		}
		_, width := utf8.DecodeRuneInString(remaining)
		safe.WriteString(remaining[:width])
		pos += width
	}
	return safe.String()
}
func (r redactor) snapshot(text string, final bool) (string, string) {
	f := secretPieces{secrets: r.secrets}
	safe := f.push(text, final)
	return r.text(safe), f.pending
}
func diagnosticBudget(text string) string {
	if len(text) <= maxDiagnosticTextBytes {
		return text
	}
	end := maxDiagnosticTextBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "…[diagnostic truncated]"
}
func (r redactor) diagnosticText(text string) string {
	text = strings.ToValidUTF8(text, "�")
	text, _ = r.snapshot(text, true)
	return diagnosticBudget(nodeprotocol.SanitizeDiagnosticText(text))
}
func (r redactor) diagnostic(payload map[string]any) map[string]any {
	// Shared URL/userinfo/query/header/DSN handling must happen here, before the
	// first Sink call, not only when a later terminal event reaches Runtime.
	safe := nodeprotocol.NormalizeDiagnosticPayload(r.value(payload).(map[string]any))
	var bounded func(any) any
	bounded = func(value any) any {
		switch v := value.(type) {
		case string:
			return r.diagnosticText(v)
		case map[string]any:
			for key, item := range v {
				v[key] = bounded(item)
			}
			return v
		case []any:
			for i, item := range v {
				v[i] = bounded(item)
			}
			return v
		default:
			return value
		}
	}
	return bounded(safe).(map[string]any)
}

type eventBoundary struct {
	redactor redactor
	streams  map[string]*secretPieces
	send     func(string, map[string]any) error
}

func newEventBoundary(r redactor, send func(string, map[string]any) error) *eventBoundary {
	return &eventBoundary{redactor: r, streams: map[string]*secretPieces{}, send: send}
}
func (b *eventBoundary) emit(kind string, payload map[string]any) error {
	copyPayload := make(map[string]any, len(payload))
	for key, value := range payload {
		copyPayload[key] = value
	}
	payload = copyPayload
	switch kind {
	case "output.delta":
		id := stringValue(payload["message_id"])
		f := b.streams[id]
		if f == nil {
			f = &secretPieces{secrets: b.redactor.secrets}
			b.streams[id] = f
		}
		text := b.redactor.text(f.push(stringValue(payload["text"]), false))
		if text == "" {
			return nil
		}
		payload["text"] = text
	case "output.snapshot":
		id := stringValue(payload["id"])
		final := payload["final"] == true
		safe, pending := b.redactor.snapshot(stringValue(payload["text"]), final)
		payload["text"] = safe
		if final {
			delete(b.streams, id)
		} else {
			b.streams[id] = &secretPieces{secrets: b.redactor.secrets, pending: pending}
		}
	case "reasoning.snapshot":
		status := stringValue(payload["status"])
		text, _ := b.redactor.snapshot(stringValue(payload["text"]), status != "running")
		payload["text"] = text
	}
	diagnostic := kind == "provider.event" && (payload["kind"] == "error" || payload["willRetry"] == true)
	diagnostic = diagnostic || (kind == "run.finished" && (payload["status"] == "failed" || payload["status"] == "cancelled"))
	diagnostic = diagnostic || (kind == "context.compaction" && (payload["reason"] != nil || payload["status"] == "failed" || payload["status"] == "cancelled"))
	if diagnostic {
		payload = b.redactor.diagnostic(payload)
	} else {
		payload = b.redactor.value(payload).(map[string]any)
	}
	return b.send(kind, payload)
}
