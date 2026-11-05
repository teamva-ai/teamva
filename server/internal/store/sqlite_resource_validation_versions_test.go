package store

import (
	"encoding/json"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteExplicitResourceRevalidationAdvancesDurableApprovalVersion(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	resource, err := f.repository.CreateResource(t.Context(), f.user.ID, domain.ProjectResource{
		ProjectID: f.project.ID, Kind: "code_directory", Name: "Code", Path: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var previousApprovalVersion int64
	for range 2 {
		pending, err := f.repository.RequestResourceValidation(t.Context(), f.user.ID, resource.ID, resource.Version)
		if err != nil {
			t.Fatal(err)
		}
		if pending.Version != resource.Version+1 || pending.Version <= previousApprovalVersion || pending.Status != "validation_pending" {
			t.Fatal("explicit revalidation did not advance the control-plane approval version")
		}
		command, ok, err := f.repository.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
		if err != nil || !ok || command.Name != "resource.validate" {
			t.Fatalf("validation command claim: ok=%t name=%s err=%v", ok, command.Name, err)
		}
		var payload nodeprotocol.ValidateResourcePayload
		if err := json.Unmarshal(command.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ResourceID != resource.ID || payload.Path != resource.Path || payload.ValidationVersion != pending.Version {
			t.Fatal("validation command lost its original canonical path or new approval version")
		}
		result, err := json.Marshal(nodeprotocol.ValidateResourceResult{
			ResourceID: resource.ID, ValidationVersion: payload.ValidationVersion, Valid: true, GitDetected: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.repository.CompleteLocalCommand(t.Context(), command.ID, command.ClaimToken, result); err != nil {
			t.Fatal(err)
		}
		items, err := f.repository.ListResources(t.Context(), f.user.ID, f.project.ID)
		if err != nil || len(items) != 1 {
			t.Fatal("completed validation did not retain the fixture Resource")
		}
		resource = items[0]
		if resource.Status != "ready" || resource.Version != pending.Version+1 || !resource.GitDetected {
			t.Fatal("validation completion did not commit a fresh ready Resource version")
		}
		previousApprovalVersion = payload.ValidationVersion
	}
}
