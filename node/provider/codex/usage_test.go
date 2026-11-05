package codex

import "testing"

func TestCompactionContextEstimateIsNotBillableUsage(t *testing.T) {
	if usage, ok := codexTurnUsage(map[string]any{"tokenUsage": map[string]any{"last": map[string]any{
		"totalTokens": float64(28370), "inputTokens": float64(0), "outputTokens": float64(0),
		"cachedInputTokens": float64(0), "reasoningOutputTokens": float64(0),
	}}}); ok {
		t.Fatalf("context estimate counted as usage: %v", usage)
	}
}

func TestCodexTurnUsageUsesLastDelta(t *testing.T) {
	payload, ok := codexTurnUsage(map[string]any{
		"tokenUsage": map[string]any{
			"last": map[string]any{
				"inputTokens": 120.0, "cachedInputTokens": 80.0,
				"outputTokens": 30.0, "reasoningOutputTokens": 10.0,
				"totalTokens": 150.0,
			},
			"total": map[string]any{"totalTokens": 9_999.0},
		},
	})
	if !ok {
		t.Fatal("expected normalized usage")
	}
	if got := payload["total_tokens"]; got != int64(150) {
		t.Fatalf("total_tokens = %v, want 150", got)
	}
	if got := payload["cached_input_tokens"]; got != int64(80) {
		t.Fatalf("cached_input_tokens = %v, want 80", got)
	}
}

func TestCodexTurnUsageRejectsEmptyPayload(t *testing.T) {
	if _, ok := codexTurnUsage(map[string]any{}); ok {
		t.Fatal("empty usage unexpectedly accepted")
	}
}
