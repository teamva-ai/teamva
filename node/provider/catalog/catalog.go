// Package catalog contains only provider-neutral lookup logic. Imports and
// metadata are generated from ../*/provider.json; never hand-edit the registry.
package catalog

//go:generate go run ../../cmd/provider-gen -root .. -out providers_generated.go

import (
	"encoding/json"
	"github.com/teamva-ai/teamva/node/provider"
	"slices"
)

func Lookup(id string) (provider.Definition, bool) {
	for _, definition := range definitions {
		if definition.Metadata.ID == id {
			return cloneDefinition(definition), true
		}
	}
	return provider.Definition{}, false
}

func cloneDefinition(value provider.Definition) provider.Definition {
	value.Metadata.Features = slices.Clone(value.Metadata.Features)
	value.Metadata.DiagnosticSteps = slices.Clone(value.Metadata.DiagnosticSteps)
	return value
}

func Definitions() []provider.Definition {
	result := make([]provider.Definition, len(definitions))
	for index, value := range definitions {
		result[index] = cloneDefinition(value)
	}
	return result
}

// Capabilities projects one provider manifest into the capability map stored
// with each AgentService. Probe and diagnose must produce the same shape: the
// frontend filters providers by the catalog flags (for example read_only and
// file_bridge), so a diagnose update must not drop them.
func Capabilities(providerID string, manifest provider.CapabilityManifest) map[string]any {
	capabilities := map[string]any{
		"streaming": manifest.Streaming, "sessions": manifest.Resume,
		"interrupt": manifest.Interrupt, "approvals": manifest.Approval,
		"models": manifest.Models, "max_parallel_runs": manifest.MaxParallelRuns,
	}
	if definition, ok := Lookup(providerID); ok {
		capabilities["display_name"] = definition.Metadata.DisplayName
		capabilities["command"] = definition.Metadata.Command
		capabilities["description"] = definition.Metadata.Description
		capabilities["diagnostic_steps"] = definition.Metadata.DiagnosticSteps
		capabilities["provider_api_version"] = definition.Metadata.APIVersion
		for _, feature := range []string{"steer", "images", "read_only", "no_tools", "skills", "computer_use", "file_bridge"} {
			capabilities[feature] = definition.Metadata.Supports(feature)
		}
	}
	if manifest.UnavailableReasonCode != "" {
		capabilities["unavailable_reason_code"] = manifest.UnavailableReasonCode
	}
	return capabilities
}

// NewOutputProjection is scoped to one Run, never shared between users/Runs.
// Native decoders live with their provider. Standard events need no decoder.
func NewOutputProjection(metadataLimit int) func(string, map[string]any) provider.OutputUpdate {
	type projectorEntry struct {
		id        string
		projector provider.OutputProjector
	}
	var projectors []projectorEntry
	for _, definition := range definitions {
		if definition.NewOutputProjector != nil {
			projectors = append(projectors, projectorEntry{definition.Metadata.ID, definition.NewOutputProjector(metadataLimit)})
		}
	}
	return func(eventType string, payload map[string]any) provider.OutputUpdate {
		owner, _ := payload["provider_id"].(string)
		if owner == "" {
			return provider.OutputUpdate{Ignore: true}
		}
		for _, projector := range projectors {
			if projector.id != owner {
				continue
			}
			if output, handled := projector.projector.ProjectOutput(eventType, payload); handled {
				return output
			}
		}
		if eventType != "output.delta" && eventType != "output.snapshot" {
			return provider.OutputUpdate{Ignore: true}
		}
		text, _ := payload["text"].(string)
		id, _ := payload["message_id"].(string)
		if id == "" {
			id, _ = payload["id"].(string)
		}
		return provider.OutputUpdate{ID: id, Text: text, Replace: eventType == "output.snapshot", Final: payload["final"] == true}
	}
}

func TerminalStatus(payload map[string]any) string {
	owner, _ := payload["provider_id"].(string)
	for _, definition := range definitions {
		if definition.Metadata.ID != owner {
			continue
		}
		if definition.TerminalStatus != nil {
			if status, handled := definition.TerminalStatus(payload); handled {
				return status
			}
		}
	}
	status, _ := payload["status"].(string)
	return status
}

func ProjectFileChange(eventType string, payload map[string]any) (provider.FileChange, bool) {
	owner, _ := payload["provider_id"].(string)
	for _, definition := range definitions {
		if definition.Metadata.ID != owner {
			continue
		}
		if definition.FileChange != nil {
			if result, handled := definition.FileChange(eventType, payload); handled {
				return result, true
			}
		}
	}
	if eventType != "workspace.diff" && eventType != "files.changed" {
		return provider.FileChange{}, false
	}
	return provider.FileChange{Source: eventType, Owner: payload,
		ItemID: provider.FirstString(payload, "id", "itemId", "item_id"), TurnID: provider.FirstString(payload, "turnId", "turn_id")}, true
}

func definitionFromJSON(metadata string, factory func(provider.Metadata) provider.Definition) provider.Definition {
	var value provider.Metadata
	if err := json.Unmarshal([]byte(metadata), &value); err != nil {
		panic(err)
	}
	definition := factory(value)
	if err := definition.Validate(); err != nil {
		panic(err)
	}
	return definition
}
