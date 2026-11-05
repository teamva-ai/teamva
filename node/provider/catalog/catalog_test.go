package catalog_test

import (
	"github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/catalog"
	"testing"
)

func TestCatalogMetadataIsNotMutableByCallers(t *testing.T) {
	first, ok := catalog.Lookup("codex")
	if !ok {
		t.Fatal("built-in provider missing")
	}
	first.Metadata.Features[0] = "modified"
	first.Metadata.DiagnosticSteps[0].ID = "modified"
	second, _ := catalog.Lookup("codex")
	if second.Metadata.Features[0] == "modified" || second.Metadata.DiagnosticSteps[0].ID == "modified" {
		t.Fatal("registry metadata leaked mutable storage")
	}
}

func TestClaudeDirectoryIsDiscoveredWithHonestCapabilities(t *testing.T) {
	definition, ok := catalog.Lookup("claude")
	if !ok || definition.Metadata.Command != "claude" || definition.Metadata.DisplayName != "Claude Code" {
		t.Fatalf("Claude Code directory was not discovered: %+v", definition.Metadata)
	}
	if err := definition.Validate(); err != nil { t.Fatal(err) }
	for _, feature := range []string{"streaming", "resume", "interrupt", "read_only", "file_bridge"} {
		if !definition.Metadata.Supports(feature) { t.Errorf("missing implemented capability %s", feature) }
	}
	for _, feature := range []string{"approval", "steer", "images", "skills", "computer_use"} {
		if definition.Metadata.Supports(feature) { t.Errorf("advertised unsupported capability %s", feature) }
	}
	project := catalog.NewOutputProjection(100)
	answer := project("output.snapshot", map[string]any{"provider_id":"claude", "id":"m", "text":"answer", "final":true})
	if answer.Ignore || !answer.Final || !answer.Replace || answer.Text != "answer" { t.Fatalf("Claude semantic output not projected: %+v", answer) }
	if !project("provider.error", map[string]any{"provider_id":"claude","message":"API error"}).Ignore { t.Fatal("Claude API failure became assistant answer") }
}

func TestCapabilitiesKeepCatalogFlagsForKnownProviders(t *testing.T) {
	manifest := provider.CapabilityManifest{
		Provider: "codex", Available: true, Authenticated: true,
		Streaming: true, Models: []string{"m"}, UnavailableReasonCode: "probe_failed",
	}
	capabilities := catalog.Capabilities("codex", manifest)
	for _, key := range []string{"streaming", "read_only", "file_bridge", "skills", "computer_use", "display_name", "command", "description", "diagnostic_steps", "provider_api_version", "unavailable_reason_code"} {
		if _, ok := capabilities[key]; !ok {
			t.Fatalf("missing %q in %+v", key, capabilities)
		}
	}
	if capabilities["read_only"] != true || capabilities["file_bridge"] != true || capabilities["display_name"] != "Codex" {
		t.Fatalf("wrong catalog projection: %+v", capabilities)
	}
	unknown := catalog.Capabilities("futurecli", manifest)
	if _, ok := unknown["display_name"]; ok {
		t.Fatal("unknown provider received catalog metadata")
	}
	if unknown["streaming"] != true || unknown["unavailable_reason_code"] != "probe_failed" {
		t.Fatalf("base fields not preserved for unknown provider: %+v", unknown)
	}
}

func TestStandardOutputDoesNotRequireNativeProviderCodec(t *testing.T) {
	project := catalog.NewOutputProjection(100)
	delta := project("output.delta", map[string]any{"provider_id": "futurecli", "message_id": "m1", "text": "hello "})
	if delta.Ignore || delta.Replace || delta.ID != "m1" || delta.Text != "hello " {
		t.Fatalf("standard delta: %+v", delta)
	}
	snapshot := project("output.snapshot", map[string]any{"provider_id": "futurecli", "id": "m1", "text": "hello world", "final": true})
	if snapshot.Ignore || !snapshot.Replace || !snapshot.Final || snapshot.Text != "hello world" {
		t.Fatalf("standard snapshot: %+v", snapshot)
	}
	if !project("reasoning.snapshot", map[string]any{"text": "not an answer"}).Ignore {
		t.Fatal("reasoning became an answer")
	}
}

func TestNativeOutputProjectionHonorsProviderOwnership(t *testing.T) {
	project := catalog.NewOutputProjection(100)
	if !project("provider.item", map[string]any{"item": map[string]any{"type": "agentMessage", "text": "untagged"}}).Ignore ||
		!project("output.delta", map[string]any{"text": "untagged"}).Ignore {
		t.Fatal("untagged output was decoded")
	}
	foreign := project("provider.item", map[string]any{"provider_id": "futurecli", "item": map[string]any{"type": "agentMessage", "text": "not Codex"}})
	if !foreign.Ignore {
		t.Fatal("foreign native item was decoded as Codex")
	}
	// v2 OpenCode reasoning/tool events are never assistant text; only the
	// finalized text part replaces the reply.
	if !project("provider.event", map[string]any{"provider_id": "opencode", "kind": "reasoning", "part_id": "reasoning:m:0", "delta": "private"}).Ignore {
		t.Fatal("reasoning leaked into output")
	}
	text := project("provider.event", map[string]any{"provider_id": "opencode", "kind": "text", "part_id": "text:m:0", "text": "answer"})
	if text.Ignore || !text.Replace || text.ID != "text:m:0" || text.Text != "answer" {
		t.Fatalf("final OpenCode text part: %+v", text)
	}
	delta := project("output.delta", map[string]any{"provider_id": "opencode", "part_id": "text:m:0", "text": "chunk"})
	if delta.Ignore || delta.Replace || delta.ID != "text:m:0" || delta.Text != "chunk" {
		t.Fatalf("OpenCode text delta: %+v", delta)
	}
	if catalog.TerminalStatus(map[string]any{"provider_id": "futurecli", "turn": map[string]any{"status": "completed"}}) != "" {
		t.Fatal("foreign terminal was decoded as Codex")
	}
}

func TestCompactionSummaryCannotBecomeFinalAnswer(t *testing.T) {
	project := catalog.NewOutputProjection(100)
	// v2 emits compaction as its own context.compaction event; summary text is
	// never projected into the assistant reply.
	compaction := project("context.compaction", map[string]any{"provider_id": "opencode", "id": "summary", "status": "running"})
	if !compaction.Ignore {
		t.Fatal("compaction summary leaked into final answer")
	}
	reasoning := project("provider.event", map[string]any{"provider_id": "opencode", "kind": "reasoning", "part_id": "reasoning:m:0", "delta": "internal"})
	if !reasoning.Ignore {
		t.Fatal("reasoning leaked into final answer")
	}
	tool := project("provider.event", map[string]any{"provider_id": "opencode", "kind": "tool", "call_id": "call_1", "name": "bash"})
	if !tool.Ignore {
		t.Fatal("tool output leaked into final answer")
	}
}
