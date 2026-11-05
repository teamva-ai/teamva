package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
)

func TestKnownCredentialArgumentsCannotReachToolSideEffects(t *testing.T) {
	for _, test := range []struct {
		name, tool, path           string
		args                       map[string]any
		unicode, secretID, allowed bool
	}{
		{name: "file-content", tool: "write_file", path: "new.txt", args: map[string]any{"path": "new.txt", "content": "prefix " + fakeSecret + " suffix"}},
		{name: "file-nested-ignored-argument", tool: "write_file", path: "existing.txt", args: map[string]any{"path": "existing.txt", "content": "ordinary replacement", "metadata": map[string]any{"nested": []any{false, 7, map[string]any{"value": fakeSecret}}}}},
		{name: "file-unicode-escaped", tool: "write_file", path: "new.txt", unicode: true, args: map[string]any{"path": "new.txt", "content": fakeSecret}},
		{name: "file-key-in-map-name", tool: "write_file", path: "new.txt", unicode: true, args: map[string]any{"path": "new.txt", "content": "ordinary content", "metadata": []any{map[string]any{"nested": map[string]any{fakeSecret: true}}}}},
		{name: "metadata-id-does-not-exempt-arguments", tool: "write_file", path: "new.txt", secretID: true, args: map[string]any{"path": "new.txt", "content": fakeSecret}},
		{name: "file-path", tool: "write_file", path: fakeSecret + ".txt", args: map[string]any{"path": fakeSecret + ".txt", "content": "ordinary content"}},
		{name: "issue-body", tool: "issue_comment_create", args: map[string]any{"body": "summary " + fakeSecret}},
		{name: "issue-nested-argument", tool: "issue_comment_create", args: map[string]any{"body": "ordinary summary", "metadata": []any{map[string]any{"nested": map[string]any{"value": "prefix " + fakeSecret + " suffix"}}}}},
		{name: "issue-unicode-escaped", tool: "issue_comment_create", unicode: true, args: map[string]any{"body": fakeSecret}},
		{name: "issue-key-in-map-name", tool: "issue_comment_create", unicode: true, args: map[string]any{"body": "ordinary summary", "metadata": []any{map[string]any{"nested": map[string]any{fakeSecret: false}}}}},
		{name: "ordinary-file-preserved", tool: "write_file", path: "new.txt", allowed: true, args: map[string]any{"path": "new.txt", "content": "api_key=an-ordinary-nonprojected-example\ninherited-unused-credential-canary"}},
		{name: "ordinary-large-file-preserved", tool: "write_file", path: "new.txt", allowed: true, args: map[string]any{"path": "new.txt", "content": strings.Repeat("甲", 24000) + " ordinary-tail"}},
		{name: "ordinary-issue-preserved", tool: "issue_comment_create", allowed: true, args: map[string]any{"body": "ordinary public summary"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, cwd := fakeClient(t, provider.InstanceConfig{Model: "fixture-credential-arguments"})
			t.Setenv("ANTHROPIC_API_KEY", "inherited-unused-credential-canary")
			existing := filepath.Join(cwd, "existing.txt")
			if err := os.WriteFile(existing, []byte("original file remains unchanged"), 0o600); err != nil {
				t.Fatal(err)
			}
			ipc := t.TempDir()
			tool := filepath.Join(ipc, "issue_comment_create")
			if err := os.Mkdir(tool, 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			requests := make(chan []byte, 1)
			hostDone := make(chan struct{})
			go func() {
				defer close(hostDone)
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					entries, _ := os.ReadDir(tool)
					for _, entry := range entries {
						if !strings.HasPrefix(entry.Name(), "req-") || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".response.json") {
							continue
						}
						path := filepath.Join(tool, entry.Name())
						body, _ := os.ReadFile(path)
						requests <- body
						_ = os.WriteFile(path+".response.json.tmp", []byte(`{"ok":true,"data":{"id":"synthetic-comment"}}`), 0o600)
						_ = os.Rename(path+".response.json.tmp", path+".response.json")
						return
					}
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			}()
			fixture := map[string]any{"id": "ordinary-mcp-id", "name": test.tool, "arguments": test.args, "unicode": test.unicode}
			if test.secretID {
				fixture["id"] = fakeSecret
			}
			prompt, _ := json.Marshal(fixture)
			events, sink := capture()
			_, err := c.Start(ctx, provider.RunRequest{RunID: "credential-arguments", CWD: cwd, Prompt: string(prompt), ToolRequestDir: ipc}, sink)
			cancel()
			<-hostDone
			if err != nil {
				t.Fatal(err)
			}
			want := "MCP_ARGUMENTS_DENIED"
			if test.allowed {
				want = "MCP_ARGUMENTS_ALLOWED"
			}
			outcome := ""
			for _, event := range *events {
				if event.Type == "output.snapshot" && event.Payload["final"] == true {
					outcome = stringValue(event.Payload["text"])
				}
			}
			if outcome != want {
				t.Errorf("tool receipt=%q want=%q", outcome, want)
			}
			encoded, _ := json.Marshal(*events)
			if strings.Contains(string(encoded), fakeSecret) {
				t.Error("credential leaked into captured events")
			}
			if test.tool == "write_file" {
				data, readErr := os.ReadFile(filepath.Join(cwd, test.path))
				if test.allowed {
					if readErr != nil || string(data) != test.args["content"].(string) {
						t.Error("ordinary file content was rejected or silently rewritten")
					}
				} else if test.path == "existing.txt" {
					if readErr != nil || string(data) != "original file remains unchanged" {
						t.Error("denied arguments overwrote an existing user file")
					}
				} else if !os.IsNotExist(readErr) {
					t.Error("denied arguments created a user file")
				}
			} else {
				entries, readErr := os.ReadDir(tool)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if test.allowed {
					select {
					case request := <-requests:
						var args map[string]any
						if json.Unmarshal(request, &args) != nil || args["body"] != test.args["body"] {
							t.Error("ordinary public comment arguments changed")
						}
					default:
						t.Error("ordinary host request did not reach FileBridge")
					}
				} else {
					if len(entries) != 0 {
						t.Error("known credential reached host IPC before the tool was denied")
					}
					select {
					case request := <-requests:
						if strings.Contains(string(request), fakeSecret) {
							t.Error("known credential appeared in a business request before response redaction")
						}
					default:
					}
				}
			}
		})
	}
}
