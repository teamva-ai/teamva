package domain

import (
	"slices"
	"testing"
)

func TestFeatureTourIDsAreFixedAndIndependent(t *testing.T) {
	want := []string{"overview", "issues", "conversations", "resources", "memories", "schedules", "changes"}
	if !slices.Equal(FeatureTourIDs(), want) {
		t.Fatalf("feature tour order=%v", FeatureTourIDs())
	}
	for _, id := range want {
		if !IsFeatureTourID(id) {
			t.Fatalf("known tour rejected: %s", id)
		}
	}
	for _, id := range []string{"", "unknown", "Overview", "overview ", "overview.issues"} {
		if IsFeatureTourID(id) {
			t.Fatalf("unknown tour accepted: %q", id)
		}
	}
	ids := FeatureTourIDs()
	ids[0] = "changed"
	if !slices.Equal(FeatureTourIDs(), want) {
		t.Fatal("caller's slice mutated the fixed tour set")
	}
}
