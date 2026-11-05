package embedded

import (
	"os"
	"path/filepath"
	"testing"
)

func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

func TestBundledExecutableFromVendorDirectory(t *testing.T) {
	root := t.TempDir()
	platform := bundledPlatform()
	want := writeExecutable(t, filepath.Join(root, "vendor", "opencode", platform, "opencode"))
	// 嵌套目录验证向上查找。
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	if got := bundledExecutable("opencode", "opencode", ""); got != want {
		t.Fatalf("bundledExecutable = %q, want %q", got, want)
	}
}

func TestBundledExecutableFromDataDir(t *testing.T) {
	dataDir := t.TempDir()
	platform := bundledPlatform()
	want := writeExecutable(t, filepath.Join(dataDir, "providers", "opencode", platform, "opencode"))
	t.Chdir(t.TempDir()) // 不在仓库树内，避免命中仓库 vendor 目录
	if got := bundledExecutable("opencode", "opencode", dataDir); got != want {
		t.Fatalf("bundledExecutable = %q, want %q", got, want)
	}
}

func TestBundledPlatformMatchesReleasePayloadNames(t *testing.T) {
	for _, item := range []struct{ goos, goarch, want string }{
		{"darwin", "arm64", "darwin-arm64"},
		{"darwin", "amd64", "darwin-x64"},
		{"linux", "arm64", "linux-arm64"},
		{"linux", "amd64", "linux-x64"},
	} {
		if got := bundledPlatformName(item.goos, item.goarch); got != item.want {
			t.Errorf("bundledPlatformName(%s, %s) = %q, want %q", item.goos, item.goarch, got, item.want)
		}
	}
}

func TestBundledExecutableMissing(t *testing.T) {
	t.Chdir(t.TempDir())
	if got := bundledExecutable("opencode", "opencode", ""); got != "" {
		t.Fatalf("bundledExecutable = %q, want empty", got)
	}
	if got := bundledExecutable("", "opencode", ""); got != "" {
		t.Fatalf("empty provider must not resolve: %q", got)
	}
}
