package opencode

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
)

func TestAuthProtectionResolvesExistingAndDanglingTargetsWithoutReadingContents(t *testing.T) {
	base := permissionFixtureDirectory(t, t.TempDir())
	actual := permissionFixtureDirectory(t, base, "actual", "nested")
	alias := filepath.Join(base, "directory-alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	// Relative link targets must resolve against the REAL parent directory,
	// not a lexical symlink parent followed by a cleaned ../ segment.
	link := filepath.Join(actual, "auth.json")
	if err := os.Symlink("../not-created.json", link); err != nil {
		t.Fatal(err)
	}
	configured := filepath.Join(alias, "auth.json")
	paths, err := resolveAuthProtectionPaths([]string{configured})
	if err != nil || !slices.Contains(paths, configured) || !slices.Contains(paths, filepath.Join(base, "actual", "not-created.json")) {
		t.Fatal("auth protection lost a dangling real target through an aliased parent")
	}
	loop := filepath.Join(base, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAuthProtectionPaths([]string{loop}); err == nil {
		t.Fatal("auth protection accepted an unverifiable symlink loop")
	}
}

func TestDiscoveryProbeCannotStartOrVerifyManagedSessions(t *testing.T) {
	isolateUserHomes(t)
	adapter, err := newModelAwareProbe("must-not-start-for-session-verification")
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if _, err := adapter.VerifySession(t.Context(), "ses_unowned"); err == nil {
		t.Fatal("a discovery probe accepted managed session verification")
	}
	if _, err := adapter.Start(t.Context(), provider.RunRequest{CWD: t.TempDir(), Prompt: "must not dispatch"}, nil); err == nil {
		t.Fatal("a discovery probe accepted model execution")
	}
	client := adapter.(*Client)
	if client.process != nil || client.currentBaseURL() != "" || client.writeSandbox != nil {
		t.Fatal("rootless discovery verification/execution started a service or invented write authority")
	}
}
