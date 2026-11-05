package codex

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func TestReadLoopFailsEveryPendingRPCOnEOF(t *testing.T) {
	client := New("codex")
	first := make(chan rpcResult, 1)
	second := make(chan rpcResult, 1)
	client.mu.Lock()
	client.processEpoch = 1
	client.pending[1] = pendingRPC{epoch: 1, response: first}
	client.pending[2] = pendingRPC{epoch: 1, response: second}
	client.mu.Unlock()
	client.readLoop(strings.NewReader(""), 1)
	for index, response := range []chan rpcResult{first, second} {
		select {
		case result := <-response:
			if result.err == nil || !strings.Contains(result.err.Error(), io.EOF.Error()) {
				t.Fatalf("pending RPC %d error=%v", index, result.err)
			}
		default:
			t.Fatalf("pending RPC %d was left blocked", index)
		}
	}
}

func TestOldTransportFailureCannotFailNewEpochRPC(t *testing.T) {
	client := New("codex")
	response := make(chan rpcResult, 1)
	client.mu.Lock()
	client.processEpoch = 2
	client.pending[1] = pendingRPC{epoch: 2, response: response}
	client.mu.Unlock()
	client.failTransport(1, errors.New("old transport exited"))
	select {
	case result := <-response:
		t.Fatalf("old transport failed new pending RPC: %v", result.err)
	default:
	}
}

func TestRealCodexTextRun(t *testing.T) {
	if os.Getenv("TEAMVA_TEST_REAL_CODEX") != "1" {
		t.Skip("TEAMVA_TEST_REAL_CODEX=1 is required")
	}
	client := New("codex")
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	manifest, err := client.Probe(ctx)
	if err != nil || !manifest.Available || !manifest.Authenticated {
		t.Fatalf("probe real Codex: manifest=%+v err=%v", manifest, err)
	}
	var mu sync.Mutex
	var output strings.Builder
	events := make([]string, 0)
	session, err := client.Start(ctx, adapter.RunRequest{
		RunID: "run_real_codex", CWD: t.TempDir(), ReadOnly: true,
		Prompt: "Reply with exactly CODEX_ADAPTER_OK. Do not use tools or inspect files.",
	}, func(_ context.Context, event adapter.Event) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event.Type)
		if event.Type == "output.delta" {
			text, _ := event.Payload["text"].(string)
			output.WriteString(text)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("real Codex run: %v events=%v", err, events)
	}
	mu.Lock()
	firstOutput := output.String()
	firstEvents := append([]string(nil), events...)
	mu.Unlock()
	if session.ID == "" || !strings.Contains(firstOutput, "CODEX_ADAPTER_OK") ||
		!contains(firstEvents, "run.started") || !contains(firstEvents, "run.completed") {
		t.Fatalf("unexpected Codex result: session=%+v output=%q events=%v", session, firstOutput, firstEvents)
	}
	exists, err := client.VerifySession(ctx, session.ID)
	if err != nil || !exists {
		t.Fatalf("verify Codex session: exists=%v err=%v", exists, err)
	}
	var resumedOutput strings.Builder
	resumed, err := client.Resume(ctx, adapter.RunRequest{
		RunID: "run_real_codex_resume", CWD: t.TempDir(), ReadOnly: true,
		NativeSessionID: session.ID,
		Prompt:          "Reply with exactly CODEX_RESUME_OK. Do not use tools or inspect files.",
	}, func(_ context.Context, event adapter.Event) error {
		if event.Type == "output.delta" {
			text, _ := event.Payload["text"].(string)
			resumedOutput.WriteString(text)
		}
		return nil
	})
	if err != nil || resumed.ID != session.ID || !strings.Contains(resumedOutput.String(), "CODEX_RESUME_OK") {
		t.Fatalf("resume Codex: session=%+v output=%q err=%v", resumed, resumedOutput.String(), err)
	}
}

func TestRealCodexAutoCompaction(t *testing.T) {
	if os.Getenv("TEAMVA_TEST_REAL_CODEX_COMPACTION") != "1" {
		t.Skip("TEAMVA_TEST_REAL_CODEX_COMPACTION=1 is required")
	}
	executable := os.Getenv("TEAMVA_TEST_CODEX_EXECUTABLE")
	if executable == "" {
		executable = "codex"
	}
	client := New(executable)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	firstPrompt := strings.Repeat("stable-context-token ", 2_600) +
		"\nReply with exactly CODEX_COMPACTION_FIRST_OK. Do not use tools or inspect files."
	first, err := client.Start(ctx, adapter.RunRequest{
		RunID: "run_real_codex_compaction_first", CWD: t.TempDir(), ReadOnly: true,
		Prompt: firstPrompt,
	}, func(_ context.Context, _ adapter.Event) error { return nil })
	if err != nil || first.ID == "" {
		t.Fatalf("first Codex compaction turn: session=%+v err=%v", first, err)
	}

	var mu sync.Mutex
	events := make([]adapter.Event, 0)
	var output strings.Builder
	resumed, err := client.Resume(ctx, adapter.RunRequest{
		RunID: "run_real_codex_compaction_second", CWD: t.TempDir(), ReadOnly: true,
		NativeSessionID: first.ID,
		Prompt:          "Reply with exactly CODEX_COMPACTION_SECOND_OK. Do not use tools or inspect files.",
	}, func(_ context.Context, event adapter.Event) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		if event.Type == "output.delta" {
			text, _ := event.Payload["text"].(string)
			output.WriteString(text)
		}
		return nil
	})
	if err != nil || resumed.ID != first.ID {
		t.Fatalf("resume compacted Codex session: session=%+v err=%v", resumed, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(output.String(), "CODEX_COMPACTION_SECOND_OK") {
		t.Fatalf("missing second response: output=%q events=%+v", output.String(), events)
	}
	for _, event := range events {
		if event.Type == "context.compaction" && event.Payload["status"] == "completed" {
			return
		}
	}
	t.Fatalf("Codex did not emit a completed context compaction: %+v", events)
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
