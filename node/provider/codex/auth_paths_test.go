package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func TestAuthCarveoutProtectsNecessaryAncestorsAndRefusesEqualWriteConflicts(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "nested-home")
	auth := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(auth), 0o700); err != nil {
		t.Fatal(err)
	}
	carveouts, err := codexPrivateCarveouts([]string{auth}, []string{root})
	if err != nil || !reflect.DeepEqual(carveouts, []string{home}) {
		t.Fatalf("carveouts=%v err=%v", carveouts, err)
	}
	if _, err := codexPrivateCarveouts([]string{auth}, []string{root, home}); err == nil {
		t.Fatal("native same-path write can override an ancestor read guard")
	}
	git := filepath.Join(root, ".git")
	if err := os.Mkdir(git, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := codexPrivateCarveouts([]string{filepath.Join(git, "auth.json")}, []string{root}); err == nil {
		t.Fatal("native .git write can override auth protection")
	}
	if _, err := codexPrivateCarveouts([]string{root}, []string{root}); err == nil {
		t.Fatal("private directory accepted as a writable root")
	}
	if _, err := codexPrivateCarveouts([]string{root}, []string{home}); err == nil {
		t.Fatal("private subtree accepted as a writable root")
	}
}

func TestFactoryProtectsFakeAuthLogicalAndCanonicalTargetsWithoutCopying(t *testing.T) {
	business := t.TempDir()
	home := filepath.Join(business, "home")
	actual := filepath.Join(business, "private-auth", "credential.json")
	writeProviderFile(t, actual, `{"fixture":true}`)
	source := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, source); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	item, err := newInstance(adapter.InstanceOptions{ConfigDir: t.TempDir(), DataDir: t.TempDir(), Config: adapter.InstanceConfig{WritableRepositories: []string{business}}})
	if err != nil {
		t.Fatal(err)
	}
	defer item.Close()
	c := item.(*Client)
	carveouts, err := codexPrivateCarveouts(c.protectedPaths, c.writableRepositories)
	if err != nil {
		t.Fatal(err)
	}
	canonicalBusiness, err := filepath.EvalSymlinks(business)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(canonicalBusiness, "home"), filepath.Join(canonicalBusiness, "private-auth")}
	if !reflect.DeepEqual(carveouts, want) {
		t.Fatalf("fake auth aliases not both protected: %v", carveouts)
	}
	if target, err := os.Readlink(filepath.Join(c.configDir, "auth.json")); err != nil || target != source {
		t.Fatal("factory must preserve the original auth-link mechanism")
	}
}

func TestUnresolvedAuthSymlinkFailsClosed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	source := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), source); err != nil {
		t.Fatal(err)
	}
	if _, err := newInstance(adapter.InstanceOptions{ConfigDir: t.TempDir()}); err == nil {
		t.Fatal("unresolved auth target silently accepted")
	}
}
