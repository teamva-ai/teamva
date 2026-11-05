package catalog_test

import (
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/catalog"
)

func TestStrictNoToolsCapabilitiesAreExplicitAndHonest(t *testing.T) {
	for id, want := range map[string]bool{"opencode": true, "claude": true, "codex": false} {
		definition, ok := catalog.Lookup(id)
		if !ok || definition.Metadata.Supports("no_tools") != want {
			t.Fatalf("%s has incorrect strict NoTools manifest", id)
		}
		capabilities := catalog.Capabilities(id, provider.CapabilityManifest{Provider: id})
		if capabilities["no_tools"] != want {
			t.Fatalf("%s lost no_tools projection", id)
		}
	}
	if catalog.Capabilities("unknown", provider.CapabilityManifest{})["no_tools"] == true {
		t.Fatal("unknown Provider acquired strict NoTools")
	}
}
