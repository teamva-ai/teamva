package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
)

func usageFixtureTokens(input, output, reasoning, read, write int64) map[string]any {
	return map[string]any{"input": input, "output": output, "reasoning": reasoning,
		"cache": map[string]any{"read": read, "write": write}}
}

func TestUsageCountersRequireCompleteValidNativeSchema(t *testing.T) {
	for _, raw := range []string{
		``, `null`, `{}`, `[]`,
		`{"input":1,"output":2,"reasoning":3}`,
		`{"input":1,"output":2,"reasoning":3,"cache":{"read":4}}`,
		`{"input":null,"output":2,"reasoning":3,"cache":{"read":4,"write":5}}`,
		`{"input":-1,"output":2,"reasoning":3,"cache":{"read":4,"write":5}}`,
		`{"input":1.5,"output":2,"reasoning":3,"cache":{"read":4,"write":5}}`,
		`{"input":"1","output":2,"reasoning":3,"cache":{"read":4,"write":5}}`,
		`{"input":9223372036854775808,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}`,
		`{"input":9223372036854775807,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}`,
	} {
		if _, err := decodeUsageTotals(json.RawMessage(raw)); err == nil {
			t.Fatalf("unknown/malformed usage accepted: %q", raw)
		}
	}
	for _, counts := range []usageTotals{{}, {input: 1, output: 2, reasoning: 3, cacheRead: 4, cacheWrite: 5}, {input: math.MaxInt64}} {
		raw, _ := json.Marshal(usageFixtureTokens(counts.input, counts.output, counts.reasoning, counts.cacheRead, counts.cacheWrite))
		got, err := decodeUsageTotals(raw)
		if err != nil || got != counts {
			t.Fatalf("valid native counts=%+v decoded=%+v err=%v", counts, got, err)
		}
	}
}

func TestUsageResumeBaselineDoesNotRebillPreviousWork(t *testing.T) {
	client := testClient(t)
	baselineRaw, _ := json.Marshal(usageFixtureTokens(25524, 2945, 1029, 144896, 0))
	baseline, err := decodeUsageTotals(baselineRaw)
	if err != nil {
		t.Fatal(err)
	}
	state := &runState{request: provider.RunRequest{RunID: "continuation"}, session: "shared", usage: baseline}
	events, sink := captureSink()
	for _, tokens := range []map[string]any{
		usageFixtureTokens(25524, 2945, 1029, 144896, 0), // Duplicate baseline: no billing event.
		usageFixtureTokens(34511, 3152, 1039, 155776, 0),
		usageFixtureTokens(39774, 6592, 1591, 402560, 0),
	} {
		if err := client.handleEvent(t.Context(), state, sink, v2Event{Type: "session.usage.updated", Data: map[string]any{"tokens": tokens}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(*events) != 2 {
		t.Fatalf("expected two nonzero deltas, got %d", len(*events))
	}
	var continuation int64
	for _, event := range *events {
		if event.Type != "usage.updated" || event.Payload["accounting_mode"] != "delta" {
			t.Fatalf("unexpected usage event: %+v", event)
		}
		continuation += event.Payload["total_tokens"].(int64)
	}
	if continuation != 276123 || 174394+continuation != 450517 {
		t.Fatalf("usage repeated work: continuation=%d goal=%d", continuation, 174394+continuation)
	}
}

func TestUsageResumeDeltasIncludeEveryNativeCounter(t *testing.T) {
	state := &runState{usage: usageTotals{input: 100, output: 10, reasoning: 5, cacheRead: 20, cacheWrite: 2}}
	events, sink := captureSink()
	err := testClient(t).handleEvent(t.Context(), state, sink, v2Event{Type: "session.usage.updated", Data: map[string]any{
		"tokens": usageFixtureTokens(107, 13, 7, 31, 3),
	}})
	if err != nil || len(*events) != 1 {
		t.Fatalf("complete usage update failed: err=%v events=%d", err, len(*events))
	}
	for key, want := range map[string]int64{"input_tokens": 7, "output_tokens": 3, "reasoning_tokens": 2,
		"cache_read_tokens": 11, "cache_write_tokens": 1, "total_tokens": 24} {
		if (*events)[0].Payload[key] != want {
			t.Fatalf("%s=%v want=%d", key, (*events)[0].Payload[key], want)
		}
	}
}

func TestUsageInvalidOrRegressedSnapshotFailsWithoutChangingBaseline(t *testing.T) {
	baseline := usageTotals{input: 10, output: 20, reasoning: 30, cacheRead: 40, cacheWrite: 50}
	for name, tokens := range map[string]any{
		"missing":                nil,
		"partial":                map[string]any{"input": 11},
		"invalid-number":         usageFixtureTokens(-1, 21, 31, 41, 51),
		"unsupported-number":     map[string]any{"input": math.NaN()},
		"input-regression":       usageFixtureTokens(9, 21, 31, 41, 51),
		"output-regression":      usageFixtureTokens(11, 19, 31, 41, 51),
		"reasoning-regression":   usageFixtureTokens(11, 21, 29, 41, 51),
		"cache-read-regression":  usageFixtureTokens(11, 21, 31, 39, 51),
		"cache-write-regression": usageFixtureTokens(11, 21, 31, 41, 49),
		"full-reset":             usageFixtureTokens(0, 0, 0, 0, 0),
	} {
		t.Run(name, func(t *testing.T) {
			state := &runState{usage: baseline}
			events, sink := captureSink()
			err := testClient(t).handleEvent(t.Context(), state, sink, v2Event{Type: "session.usage.updated", Data: map[string]any{"tokens": tokens}})
			if err == nil || !strings.Contains(err.Error(), "usage") || len(*events) != 0 || state.usage != baseline {
				t.Fatalf("unknown usage/reset was silently billed or changed baseline: err=%v events=%+v baseline=%+v", err, *events, state.usage)
			}
		})
	}
}

func TestUsageConcurrentIndependentSessionsKeepPerRunBaselines(t *testing.T) {
	client := testClient(t)
	var wg sync.WaitGroup
	failures := make(chan string, 32)
	for i := int64(1); i <= 32; i++ {
		wg.Add(1)
		go func(i int64) {
			defer wg.Done()
			state := &runState{session: fmt.Sprintf("session-%d", i), usage: usageTotals{input: i * 100}}
			events, sink := captureSink()
			err := client.handleEvent(context.Background(), state, sink, v2Event{Type: "session.usage.updated", Data: map[string]any{
				"tokens": usageFixtureTokens(i*100+1, 2, 3, 4, 5),
			}})
			if err != nil || len(*events) != 1 || (*events)[0].Payload["total_tokens"] != int64(15) {
				failures <- fmt.Sprintf("session %d baseline leaked: err=%v events=%+v", i, err, *events)
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}
