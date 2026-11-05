package codex

import "testing"

func TestDiagnoseExplainsMissingExecutable(t *testing.T) {
	client := New(t.TempDir() + "/missing-codex")
	manifest, steps, err := client.Diagnose(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Available || manifest.UnavailableReasonCode != "executable_not_found" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if len(steps) != 5 || steps[0].ID != "executable" || steps[0].Status != "failed" {
		t.Fatalf("steps = %+v", steps)
	}
	for _, step := range steps[1:] {
		if step.Status != "skipped" {
			t.Fatalf("step %s status = %s, want skipped", step.ID, step.Status)
		}
	}
}
