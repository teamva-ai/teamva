package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/node/provider"
)

func TestSecurityAuthorizedRootAndAncestorReplacementAreRejected(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "ancestor"}[ancestor], func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(base, "parent")
			repo := filepath.Join(parent, "repo")
			if err := os.MkdirAll(repo, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, "ordinary.txt"), []byte("authorized-original"), 0o600); err != nil {
				t.Fatal(err)
			}
			c, cwd := fakeClient(t, provider.InstanceConfig{WritableRepositories: []string{repo}})
			config, _ := c.config()
			scope, err := newToolScope(c, config, provider.RunRequest{CWD: cwd})
			if err != nil {
				t.Fatal(err)
			}
			defer scope.close()
			outside := t.TempDir()
			target := outside
			if ancestor {
				target = filepath.Join(outside, "repo")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			outsideFile := filepath.Join(target, "ordinary.txt")
			if err := os.WriteFile(outsideFile, []byte("outside-root-canary"), 0o600); err != nil {
				t.Fatal(err)
			}
			replace := repo
			if ancestor {
				replace = parent
			}
			if err := os.Rename(replace, replace+"-old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, replace); err != nil {
				t.Fatal(err)
			}
			if value, err := scope.call(t.Context(), "read_file", map[string]any{"path": filepath.Join(repo, "ordinary.txt")}); err == nil {
				t.Errorf("rebound authorized root read succeeded: %q", value)
			}
			if _, err := scope.call(t.Context(), "write_file", map[string]any{"path": filepath.Join(repo, "ordinary.txt"), "content": "bad replacement"}); err == nil {
				t.Error("rebound authorized root write succeeded")
			}
			if data, _ := os.ReadFile(outsideFile); string(data) != "outside-root-canary" {
				t.Error("unauthorized outside inode was modified")
			}
		})
	}
}

func TestSecurityInitiallySymlinkedAuthorizedRootIsRejected(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.Symlink(outside, repo); err != nil {
		t.Fatal(err)
	}
	c, cwd := fakeClient(t, provider.InstanceConfig{WritableRepositories: []string{repo}})
	config, _ := c.config()
	scope, err := newToolScope(c, config, provider.RunRequest{CWD: cwd})
	if err == nil {
		scope.close()
		t.Fatal("scope accepted a symlink instead of the authorized root identity")
	}
}

func TestSecurityConcurrentRootRebindingNeverReadsOutside(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, held, outside := filepath.Join(base, "repo"), filepath.Join(base, "repo-held"), filepath.Join(base, "outside")
	for _, dir := range []string{repo, outside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(repo, "ordinary.txt"), []byte("original-identity"), 0o600)
	_ = os.WriteFile(filepath.Join(outside, "ordinary.txt"), []byte("unauthorized-identity-canary"), 0o600)
	c, cwd := fakeClient(t, provider.InstanceConfig{WritableRepositories: []string{repo}})
	config, _ := c.config()
	scope, err := newToolScope(c, config, provider.RunRequest{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	defer scope.close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if os.Rename(repo, held) != nil {
				return
			}
			if os.Symlink(outside, repo) != nil {
				return
			}
			_ = os.Remove(repo)
			if os.Rename(held, repo) != nil {
				return
			}
		}
	}()
	for i := 0; i < 200; i++ {
		text, err := scope.call(t.Context(), "read_file", map[string]any{"path": filepath.Join(repo, "ordinary.txt")})
		if err == nil && text != "original-identity" {
			t.Errorf("racing root rebound an authorized FD: %q", text)
			break
		}
	}
	wg.Wait()
}

func TestSecurityProviderErrorIsSanitizedBeforeFirstSinkEvent(t *testing.T) {
	c, cwd := fakeClient(t, provider.InstanceConfig{Model: "fixture-diagnostic-url"})
	events, sink := capture()
	_, err := c.Start(t.Context(), provider.RunRequest{RunID: "diagnostic", CWD: cwd}, sink)
	if err == nil {
		t.Fatal("fixture should fail")
	}
	firstError := false
	for _, event := range *events {
		if event.Type != "provider.event" || event.Payload["kind"] != "error" {
			continue
		}
		firstError = true
		data, _ := json.Marshal(event.Payload)
		for _, canary := range []string{"review-only-password", "review-only-token", "review-dsn-password"} {
			if strings.Contains(string(data), canary) {
				t.Errorf("first provider.error leaked %s", canary)
			}
		}
		if !strings.Contains(string(data), "useful-error-marker") || event.Payload["status_code"] != float64(401) {
			t.Fatal("actionable error code/message was lost")
		}
		break
	}
	if !firstError {
		t.Fatal("no native provider.error observed")
	}
	for _, canary := range []string{"review-only-password", "review-only-token", "review-dsn-password"} {
		if strings.Contains(err.Error(), canary) {
			t.Errorf("returned error leaked %s", canary)
		}
	}
}

func TestSecurityStreamSecretBoundaryNeverReachesSink(t *testing.T) {
	c, cwd := fakeClient(t, provider.InstanceConfig{Model: "fixture-split-key"})
	events, sink := capture()
	_, err := c.Start(t.Context(), provider.RunRequest{RunID: "split-key", CWD: cwd}, sink)
	if err != nil {
		t.Fatal(err)
	}
	var deltas strings.Builder
	prefix := fakeSecret[:len(fakeSecret)/2]
	for _, event := range *events {
		if event.Type == "output.delta" {
			deltas.WriteString(stringValue(event.Payload["text"]))
		}
		if event.Type == "output.snapshot" || event.Type == "reasoning.snapshot" {
			if text := stringValue(event.Payload["text"]); strings.Contains(text, prefix) {
				t.Errorf("unfinished known secret prefix reached %s", event.Type)
			}
		}
	}
	if strings.Contains(deltas.String(), fakeSecret) || strings.Contains(deltas.String(), prefix) {
		t.Fatal("persisted delta pieces can reconstruct a known API credential")
	}
}

func TestSecurityEveryKnownSecretSplitAndCumulativeSnapshotIsSafe(t *testing.T) {
	for split := 1; split < len(fakeSecret); split++ {
		t.Run(string(rune('A'+split)), func(t *testing.T) {
			type frame struct {
				kind    string
				payload map[string]any
			}
			frames := []frame{}
			boundary := newEventBoundary(newRedactor(map[string]string{"ANTHROPIC_API_KEY": fakeSecret}), func(kind string, payload map[string]any) error {
				frames = append(frames, frame{kind, payload})
				return nil
			})
			_ = boundary.emit("output.delta", map[string]any{"message_id": "message", "text": "visible|" + fakeSecret[:split]})
			_ = boundary.emit("output.snapshot", map[string]any{"id": "message", "text": "visible|" + fakeSecret[:split]})
			_ = boundary.emit("reasoning.snapshot", map[string]any{"id": "thinking", "text": fakeSecret[:split], "status": "running"})
			for _, event := range frames {
				text := stringValue(event.payload["text"])
				if event.kind == "reasoning.snapshot" {
					if text != "" {
						t.Fatalf("unfinished reasoning prefix persisted: %q", text)
					}
				} else if text != "visible|" {
					t.Fatalf("unfinished text prefix persisted: %q", text)
				}
			}
			_ = boundary.emit("output.delta", map[string]any{"message_id": "message", "text": fakeSecret[split:] + "|tail"})
			_ = boundary.emit("output.snapshot", map[string]any{"id": "message", "text": "visible|" + fakeSecret + "|tail", "final": true})
			var deltas strings.Builder
			for _, event := range frames {
				if event.kind == "output.delta" {
					deltas.WriteString(stringValue(event.payload["text"]))
				}
			}
			if deltas.String() != "visible|[REDACTED]|tail" {
				t.Fatalf("wrong sanitized cross-frame stream: %q", deltas.String())
			}
		})
	}
}

func TestSecurityDiagnosticBudgetIsExplicitAndUTF8Safe(t *testing.T) {
	text := strings.Repeat("甲", 24000) + " error-tail"
	r := newRedactor(nil)
	if got := r.text(text); got != text {
		t.Fatal("business redaction applied a diagnostic size limit")
	}
	diagnostic := r.diagnosticText(text)
	if !utf8.ValidString(diagnostic) || !strings.HasSuffix(diagnostic, "[diagnostic truncated]") || len(diagnostic) > maxDiagnosticTextBytes+32 {
		t.Fatal("diagnostic budget is implicit or corrupts UTF-8")
	}
	if !utf8.ValidString(r.diagnosticText(string([]byte{'x', 0xff, 'y'}))) {
		t.Fatal("binary stderr produced invalid diagnostic UTF-8")
	}
}

func TestSecurityWorkspaceInstructionsLoadContentAndRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(path, []byte("workspace-rule-one-canary"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, cwd := fakeClient(t, provider.InstanceConfig{Model: "fixture-workspace-instructions", WorkspaceInstructions: path})
	_, sink := capture()
	if _, err := c.Start(t.Context(), provider.RunRequest{RunID: "instructions-one", CWD: cwd, Prompt: "workspace-rule-one-canary"}, sink); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("workspace-rule-two-canary"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, _ := c.config()
	if err := WriteConfig(c.configDir, config); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(t.Context(), provider.RunRequest{RunID: "instructions-two", CWD: cwd, Prompt: "workspace-rule-two-canary"}, sink); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityWorkspaceInstructionsFailureIsClosed(t *testing.T) {
	for _, mode := range []string{"missing", "directory", "symlink", "oversized", "invalid-utf8"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "AGENTS.md")
			switch mode {
			case "directory":
				_ = os.Mkdir(path, 0o700)
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				_ = os.WriteFile(target, []byte("outside instructions"), 0o600)
				_ = os.Symlink(target, path)
			case "oversized":
				_ = os.WriteFile(path, []byte(strings.Repeat("x", (1<<20)+1)), 0o600)
			case "invalid-utf8":
				_ = os.WriteFile(path, []byte{0xff}, 0o600)
			}
			c, cwd := fakeClient(t, provider.InstanceConfig{WorkspaceInstructions: path})
			events, sink := capture()
			if _, err := c.Start(t.Context(), provider.RunRequest{RunID: "bad-instructions", CWD: cwd}, sink); err == nil {
				t.Fatal("invalid host instruction file was silently ignored")
			}
			for _, event := range *events {
				if event.Type == "run.started" {
					t.Fatal("started a native Run without required host instructions")
				}
			}
		})
	}
}

func TestSecurityBusinessTextDoesNotUseDiagnosticTruncation(t *testing.T) {
	c, cwd := fakeClient(t, provider.InstanceConfig{})
	config, _ := c.config()
	scope, err := newToolScope(c, config, provider.RunRequest{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	defer scope.close()
	text := strings.Repeat("甲", 24000) + " business-tail-canary"
	if err := os.WriteFile(filepath.Join(cwd, "large.txt"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	response := scope.mcp(context.Background(), map[string]any{"id": 1, "method": "tools/call", "params": map[string]any{"name": "read_file", "arguments": map[string]any{"path": "large.txt"}}})
	clean := newRedactor(nil).value(response).(map[string]any)
	result := clean["result"].(map[string]any)
	content := result["content"].([]any)
	received := content[0].(map[string]any)["text"].(string)
	if received != text || !utf8.ValidString(received) {
		t.Fatal("bounded normal read_file text was truncated or UTF-8 corrupted by redaction")
	}
	bridgeJSON, _ := json.Marshal(map[string]any{"ok": true, "data": map[string]any{"body": text, "tail": "receipt-tail-canary"}})
	cleanReceipt := newRedactor(nil).text(string(bridgeJSON))
	if !json.Valid([]byte(cleanReceipt)) || !strings.Contains(cleanReceipt, "receipt-tail-canary") {
		t.Fatal("normal JSON receipt was silently truncated")
	}
}

func TestSecurityLargeFinalOutputPreservesUTF8AndTail(t *testing.T) {
	c, cwd := fakeClient(t, provider.InstanceConfig{Model: "fixture-large-output"})
	events, sink := capture()
	if _, err := c.Start(t.Context(), provider.RunRequest{RunID: "large-final", CWD: cwd}, sink); err != nil {
		t.Fatal(err)
	}
	for _, event := range *events {
		if event.Type == "output.snapshot" && event.Payload["final"] == true {
			text := stringValue(event.Payload["text"])
			if !utf8.ValidString(text) || !strings.HasSuffix(text, " business-tail-canary") {
				t.Fatal("final assistant snapshot was truncated/corrupted by diagnostic budget")
			}
			return
		}
	}
	t.Fatal("missing final snapshot")
}

func TestSecurityLargeFileBridgeReceiptIsNotSilentlyTruncated(t *testing.T) {
	c, cwd := fakeClient(t, provider.InstanceConfig{Model: "fixture-large-bridge"})
	ipc := t.TempDir()
	tool := filepath.Join(ipc, "issue_comment_create")
	if err := os.Mkdir(tool, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	response, _ := json.Marshal(map[string]any{"ok": true, "data": map[string]any{"body": strings.Repeat("甲", 24000), "tail": "receipt-tail-canary"}})
	go func() {
		for {
			entries, _ := os.ReadDir(tool)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "req-") && strings.HasSuffix(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), ".response.json") {
					path := filepath.Join(tool, entry.Name()) + ".response.json"
					_ = os.WriteFile(path+".tmp", response, 0o600)
					_ = os.Rename(path+".tmp", path)
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	_, sink := capture()
	if _, err := c.Start(ctx, provider.RunRequest{RunID: "large-receipt", CWD: cwd, ToolRequestDir: ipc}, sink); err != nil {
		t.Fatal(err)
	}
}
