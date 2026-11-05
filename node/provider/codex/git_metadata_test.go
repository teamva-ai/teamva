package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func TestLinkedMetadataRequiresAuthorizedPrimaryAndPinnedBidirectionalIdentity(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(base, "primary")
	worktree := filepath.Join(primary, ".qa-worktrees", "linked")
	metadata := filepath.Join(primary, ".git", "worktrees", "linked")
	for _, path := range []string{worktree, metadata} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pointer := filepath.Join(worktree, ".git")
	writeProviderFile(t, pointer, "gitdir: "+metadata+"\n")
	writeProviderFile(t, filepath.Join(metadata, "gitdir"), pointer+"\n")
	writeProviderFile(t, filepath.Join(metadata, "commondir"), "../..\n")
	request := adapter.RunRequest{}
	roots := []string{primary, worktree}
	got, err := codexLinkedMetadataWrites(request, roots, nil)
	if err != nil || !reflect.DeepEqual(got, []string{metadata}) {
		t.Fatalf("authorized linked metadata=%v err=%v", got, err)
	}
	for _, tc := range []struct {
		name      string
		request   adapter.RunRequest
		roots     []string
		private   []string
		wantError bool
	}{
		{"worktree-only", request, []string{worktree}, nil, false},
		{"readonly", adapter.RunRequest{ReadOnly: true}, roots, nil, false},
		{"no-tools", adapter.RunRequest{DisableTools: true}, roots, nil, false},
		{"private-ancestor", request, roots, []string{filepath.Join(primary, ".git")}, true},
		{"private-descendant", request, roots, []string{filepath.Join(metadata, "auth.json")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := codexLinkedMetadataWrites(tc.request, tc.roots, tc.private)
			if len(got) != 0 || (err != nil) != tc.wantError {
				t.Fatalf("unsafe metadata grant: %v err=%v", got, err)
			}
		})
	}
	writeProviderFile(t, filepath.Join(metadata, "gitdir"), filepath.Join(primary, ".git", "HEAD")+"\n")
	if got, err := codexLinkedMetadataWrites(request, roots, nil); err != nil || len(got) != 0 {
		t.Fatal("unrelated .git pointer forged a metadata grant")
	}
	writeProviderFile(t, filepath.Join(metadata, "gitdir"), pointer+"\n")
	if err := os.Remove(filepath.Join(metadata, "commondir")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside-pointer")
	writeProviderFile(t, outside, "../..\n")
	if err := os.Symlink(outside, filepath.Join(metadata, "commondir")); err != nil {
		t.Fatal(err)
	}
	if got, err := codexLinkedMetadataWrites(request, roots, nil); err != nil || len(got) != 0 {
		t.Fatal("symlinked metadata control file was trusted")
	}
}

func TestArbitraryGitdirInsideBusinessRootNeverCreatesAGrant(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	primary, worktree, decoy := filepath.Join(base, "primary"), filepath.Join(base, "worktree"), filepath.Join(base, "primary", "private-decoy")
	for _, path := range []string{filepath.Join(primary, ".git"), worktree, decoy} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pointer := filepath.Join(worktree, ".git")
	writeProviderFile(t, pointer, "gitdir: "+decoy+"\n")
	writeProviderFile(t, filepath.Join(decoy, "gitdir"), pointer+"\n")
	writeProviderFile(t, filepath.Join(decoy, "commondir"), filepath.Join(primary, ".git")+"\n")
	if got, err := codexLinkedMetadataWrites(adapter.RunRequest{}, []string{primary, worktree}, nil); err != nil || len(got) != 0 {
		t.Fatal("arbitrary .git text was treated as trusted worktree metadata")
	}
}
