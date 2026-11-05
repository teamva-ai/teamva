//go:build darwin || linux

package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func TestWebSearchLifecycleReachesSinkBeforeTurnCompletion(t *testing.T) {
	const started = `{"method":"item/started","params":{"threadId":"synthetic-thread","turnId":"synthetic-turn","item":{"type":"webSearch","id":"synthetic-search","action":null,"query":null,"results":null}}}`
	const completed = `{"method":"item/completed","params":{"threadId":"synthetic-thread","turnId":"synthetic-turn","item":{"type":"webSearch","id":"synthetic-search","action":{"type":"search","queries":["synthetic alpha","synthetic beta"],"query":null},"query":null,"results":[]}}}`
	directory := t.TempDir()
	executable := filepath.Join(directory, "fake-codex-search")
	script := strings.NewReplacer("__SEARCH_STARTED__", started, "__SEARCH_COMPLETED__", completed).Replace(`#!/bin/sh
phase=0
while IFS= read -r line; do
  id=${line#*\"id\":}
  id=${id%%,*}
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"id":%s,"result":{"userAgent":"teamva/0.163.0"}}\n' "$id" ;;
    *'"method":"thread/start"'*)
      printf '{"id":%s,"result":{"thread":{"id":"synthetic-thread"},"activePermissionProfile":{"id":"teamva"},"runtimeWorkspaceRoots":[]}}\n' "$id" ;;
    *'"method":"turn/start"'*)
      printf '{"id":%s,"result":{"turn":{"id":"synthetic-turn"}}}\n' "$id"
      printf '%s\n' '__SEARCH_STARTED__' ;;
    *'"method":"turn/steer"'*)
      printf '{"id":%s,"result":{}}\n' "$id"
      if [ "$phase" -eq 0 ]; then
        printf '%s\n' '__SEARCH_COMPLETED__'
        phase=1
      else
        printf '{"method":"turn/completed","params":{"turn":{"id":"synthetic-turn","status":"completed"}}}\n'
      fi ;;
  esac
done
`)
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client := New(executable).WithConfigDir(directory)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	events := make(chan adapter.Event, 4)
	done := make(chan error, 1)
	go func() {
		_, err := client.Start(ctx, adapter.RunRequest{RunID: "synthetic-run", CWD: directory, Prompt: "synthetic search", ReadOnly: true},
			func(_ context.Context, event adapter.Event) error { events <- event; return nil })
		done <- err
	}()
	for index, want := range []struct{ method, raw string }{{"item/started", started}, {"item/completed", completed}} {
		select {
		case event := <-events:
			if event.Type != "provider.item" || event.Payload["method"] != want.method || event.ProviderEventType != want.method {
				t.Fatalf("lifecycle %d lost native method: %+v", index, event)
			}
			if !bytes.Equal(event.ProviderRaw, []byte(want.raw)) {
				t.Fatalf("lifecycle %d changed native wire", index)
			}
		case err := <-done:
			t.Fatalf("turn ended before search lifecycle %d: %v", index, err)
		case <-ctx.Done():
			t.Fatalf("search lifecycle %d did not reach sink: %v", index, ctx.Err())
		}
		select {
		case err := <-done:
			t.Fatalf("turn ended before explicit release after lifecycle %d: %v", index, err)
		default:
		}
		// The fake transport releases search completion, then turn completion,
		// separately. Neither lifecycle can be deferred until the terminal event.
		if err := client.call(ctx, "turn/steer", map[string]any{"threadId": "synthetic-thread", "expectedTurnId": "synthetic-turn", "input": []any{}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("released fake turn did not complete")
	}
}

func TestUnsupportedNativePermissionsFailBeforeAnyTurn(t *testing.T) {
	for _, tc := range []struct{ name, userAgent, confirmation string }{
		{"old-serde-may-ignore-fields", "teamva/0.162.0", `"activePermissionProfile":{"id":"teamva"},"runtimeWorkspaceRoots":[]`},
		{"missing-confirmation", "teamva/0.163.0", `"sandbox":{"type":"workspaceWrite"}`},
		{"unexpected-root", "teamva/0.163.0", `"activePermissionProfile":{"id":"teamva"},"runtimeWorkspaceRoots":["/unexpected"]`},
		{"wrong-profile", "teamva/0.163.0", `"activePermissionProfile":{"id":":workspace"},"runtimeWorkspaceRoots":[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "unsafe-turn")
			executable := filepath.Join(dir, "fake-codex")
			script := fmt.Sprintf(`#!/bin/sh
while IFS= read -r line; do
  id=${line#*\"id\":}
  id=${id%%%%,*}
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":%%s,"result":{"userAgent":"%s"}}\n' "$id" ;;
    *'"method":"thread/start"'*) printf '{"id":%%s,"result":{"thread":{"id":"unsupported"},%s}}\n' "$id" ;;
    *'"method":"turn/start"'*) printf unsafe > '%s'; exit 1 ;;
  esac
done
`, tc.userAgent, tc.confirmation, marker)
			if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			client := New(executable)
			defer client.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err := client.Start(ctx, adapter.RunRequest{RunID: "must-not-run", CWD: dir, ReadOnly: true, Prompt: "fixture"}, nil)
			want := "did not confirm the exact filesystem permission profile"
			if tc.name == "old-serde-may-ignore-fields" {
				want = "Codex 0.163.0 or later is required"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("unsupported native policy result=%v want=%q", err, want)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("unsupported server reached a model turn")
			}
		})
	}
}

func TestSinkFailureReapsCodexTreeBeforeNextRun(t *testing.T) {
	directory := t.TempDir()
	processCountPath := filepath.Join(directory, "process-count")
	oldEventMarkerPath := filepath.Join(directory, "old-event-marker")
	executable := filepath.Join(directory, "fake-codex")
	script := `#!/bin/sh
count=0
if [ -r "$TEAMVA_FAKE_CODEX_PROCESS_COUNT" ]; then
  IFS= read -r count < "$TEAMVA_FAKE_CODEX_PROCESS_COUNT"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$TEAMVA_FAKE_CODEX_PROCESS_COUNT"
turn=0
while IFS= read -r line; do
  id=${line#*\"id\":}
  id=${id%%,*}
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"id":%s,"result":{"userAgent":"teamva/0.163.0"}}\n' "$id"
      ;;
    *'"method":"thread/start"'*)
      printf '{"id":%s,"result":{"thread":{"id":"thread-%s"},"activePermissionProfile":{"id":"teamva"},"runtimeWorkspaceRoots":[]}}\n' "$id" "$count"
      ;;
    *'"method":"turn/start"'*)
      turn=$((turn + 1))
      printf '{"id":%s,"result":{"turn":{"id":"turn-%s-%s"}}}\n' "$id" "$count" "$turn"
      if [ "$count" -eq 1 ] && [ "$turn" -eq 1 ]; then
        printf '{"method":"turn/started","params":{"turn":{"id":"turn-1-1"}}}\n'
        (
          sleep 1
          printf 'old process survived\n' > "$TEAMVA_FAKE_CODEX_OLD_MARKER"
          printf '{"method":"item/agentMessage/delta","params":{"delta":"OLD_EVENT","itemId":"old"}}\n'
          sleep 1
          printf '{"method":"turn/completed","params":{"turn":{"id":"turn-1-1","status":"completed"}}}\n'
        ) &
      elif [ "$count" -eq 1 ]; then
        # This branch is only reachable with the regression: the failed first
        # turn reused its still-running app-server for the next run.
        (
          sleep 2
          printf '{"method":"turn/completed","params":{"turn":{"id":"turn-1-%s","status":"completed"}}}\n' "$turn"
        ) &
      else
        printf '{"method":"turn/started","params":{"turn":{"id":"turn-%s-%s"}}}\n' "$count" "$turn"
        printf '{"method":"turn/completed","params":{"turn":{"id":"turn-%s-%s","status":"completed"}}}\n' "$count" "$turn"
      fi
      ;;
  esac
done
wait
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEAMVA_FAKE_CODEX_PROCESS_COUNT", processCountPath)
	t.Setenv("TEAMVA_FAKE_CODEX_OLD_MARKER", oldEventMarkerPath)

	client := New(executable)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	sinkErr := errors.New("persist Codex event: injected failure")
	first, err := client.Start(ctx, adapter.RunRequest{
		RunID: "run-sink-failure", CWD: directory, Prompt: "first", ReadOnly: true,
	}, func(_ context.Context, event adapter.Event) error {
		if event.Type == "run.started" {
			return sinkErr
		}
		return nil
	})
	if !errors.Is(err, sinkErr) || first.ID != "thread-1" {
		t.Fatalf("first session=%+v err=%v", first, err)
	}
	client.mu.Lock()
	processAfterFailure := client.process
	client.mu.Unlock()
	if processAfterFailure != nil {
		t.Fatal("Codex process remained installed after sink failure returned")
	}

	var eventsMu sync.Mutex
	secondEvents := make([]adapter.Event, 0, 2)
	second, err := client.Start(ctx, adapter.RunRequest{
		RunID: "run-after-sink-failure", CWD: directory, Prompt: "second", ReadOnly: true,
	}, func(_ context.Context, event adapter.Event) error {
		eventsMu.Lock()
		secondEvents = append(secondEvents, event)
		eventsMu.Unlock()
		return nil
	})
	if err != nil || second.ID != "thread-2" {
		t.Fatalf("second session=%+v err=%v", second, err)
	}
	processCount, err := os.ReadFile(processCountPath)
	if err != nil || strings.TrimSpace(string(processCount)) != "2" {
		t.Fatalf("Codex process count=%q err=%v, want a fresh second process", processCount, err)
	}

	// The first process scheduled a delayed descendant write and notification.
	// Tree.Close is synchronous, so neither may appear after the failed run has
	// returned or while the replacement process owns active.
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(oldEventMarkerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old Codex process survived sink failure: %v", err)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	for _, event := range secondEvents {
		if text, _ := event.Payload["text"].(string); text == "OLD_EVENT" {
			t.Fatalf("replacement run received an old turn event: %+v", secondEvents)
		}
	}
}

func TestCloseReapsCodexProcessDuringStartupHandshake(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "slow-codex")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nsleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	client := New(executable)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	startDone := make(chan error, 1)
	go func() {
		_, err := client.Start(ctx, adapter.RunRequest{
			RunID: "run-close-startup", CWD: directory, Prompt: "work", ReadOnly: true,
		}, func(context.Context, adapter.Event) error { return nil })
		startDone <- err
	}()
	var processDone <-chan struct{}
	deadline := time.Now().Add(5 * time.Second)
	for processDone == nil && time.Now().Before(deadline) {
		client.mu.Lock()
		if client.process != nil {
			processDone = client.process.Done()
		}
		client.mu.Unlock()
		if processDone == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if processDone == nil {
		t.Fatal("Codex process was not installed during startup")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-processDone:
	default:
		t.Fatal("Codex process tree survived Close")
	}
	select {
	case err := <-startDone:
		if err == nil {
			t.Fatal("Codex startup unexpectedly succeeded after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Codex startup did not unblock after Close")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.process != nil {
		t.Fatal("Codex process was installed after Close returned")
	}
}
