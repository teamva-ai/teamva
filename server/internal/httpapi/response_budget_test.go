package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestBoundedJSONUsesUnescapedHTMLSize(t *testing.T) {
	patch := strings.Repeat("<>&", 1024)
	payload, err := encodeBoundedJSON(map[string]string{"patch": patch}, len(patch)+128)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte("<>&")) || bytes.Contains(payload, []byte(`\u003c`)) ||
		bytes.Contains(payload, []byte(`\u003e`)) || bytes.Contains(payload, []byte(`\u0026`)) {
		t.Fatalf("HTML-sensitive bytes were expanded: %q", payload[:min(len(payload), 128)])
	}
	oversized := map[string]string{"patch": strings.Repeat("<", maxGitJSONResponseBytes+1)}
	if _, err := encodeBoundedJSON(oversized, maxGitJSONResponseBytes); !errors.Is(err, errJSONResponseBudget) {
		t.Fatalf("oversized response error=%v", err)
	}
}

func TestRunSnapshotNormalizationUsesOneAggregatePatchBudget(t *testing.T) {
	items := normalizeRunGitSnapshots([]nodeprotocol.GitRunWorkspaceSnapshot{
		{RepositoryID: "repo", Phase: "start", Areas: []nodeprotocol.GitRunPatchArea{{
			Area: "staged", Patch: strings.Repeat("a", 6<<20), Paths: []string{"src/a.go"},
		}}},
		{RepositoryID: "repo", Phase: "end", Areas: []nodeprotocol.GitRunPatchArea{{
			Area: "unstaged", Patch: strings.Repeat("b", 6<<20), Paths: []string{"src/b.go"},
		}}},
	})
	patchBytes := 0
	truncated := false
	for _, snapshot := range items {
		truncated = truncated || snapshot.Truncated
		for _, area := range snapshot.Areas {
			patchBytes += len(area.Patch)
			truncated = truncated || area.Truncated
		}
	}
	if patchBytes > maxRunGitSnapshotPatchBytes || !truncated {
		t.Fatalf("patch bytes=%d truncated=%t", patchBytes, truncated)
	}
}

func TestRunSnapshotNormalizationKeepsHundredRepositoryPhases(t *testing.T) {
	input := make([]nodeprotocol.GitRunWorkspaceSnapshot, 0, maxRunGitSnapshots)
	for index := 0; index < nodeprotocol.MaxGitRepositoriesPerRun; index++ {
		for _, phase := range []string{"start", "end"} {
			input = append(input, nodeprotocol.GitRunWorkspaceSnapshot{
				RepositoryID: fmt.Sprintf("repository-%03d", index), Phase: phase,
				Areas: []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
				Warnings: []nodeprotocol.GitRepositoryWarning{},
			})
		}
	}
	items := normalizeRunGitSnapshots(input)
	if len(items) != maxRunGitSnapshots || items[len(items)-1].Truncated {
		t.Fatalf("normalized snapshots=%d last_truncated=%t", len(items), items[len(items)-1].Truncated)
	}
}
