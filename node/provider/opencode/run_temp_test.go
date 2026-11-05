package opencode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunTempPinnedCreationAndReplacementRejection(t *testing.T) {
	base := permissionFixtureDirectory(t, t.TempDir())
	ipc := permissionFixtureDirectory(t, base, "transport", "run")
	temp, err := prepareRunTempDirectory(ipc)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(temp.path)
	if err != nil || info.Mode().Perm() != 0o700 || filepath.Dir(temp.path) != ipc {
		t.Fatal("temp was not a private child of the exact IPC")
	}
	if err := temp.validate(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Dir(ipc), filepath.Join(base, "transport-moved")); err != nil {
		t.Fatal(err)
	}
	permissionFixtureDirectory(t, base, "transport", "run", ".tmp")
	if err := temp.validate(); err == nil {
		t.Fatal("replaced IPC ancestor was accepted")
	}
}

func TestRunTempRejectsSymlinkFileAndDirectoryReplacement(t *testing.T) {
	for _, kind := range []string{"symlink", "file", "tmp-replacement", "ipc-alias"} {
		t.Run(kind, func(t *testing.T) {
			base := permissionFixtureDirectory(t, t.TempDir())
			ipc := permissionFixtureDirectory(t, base, "run")
			outside := permissionFixtureDirectory(t, base, "outside")
			switch kind {
			case "symlink":
				if err := os.Symlink(outside, filepath.Join(ipc, ".tmp")); err != nil {
					t.Fatal(err)
				}
			case "file":
				writeFile(t, filepath.Join(ipc, ".tmp"), "unchanged")
			case "ipc-alias":
				alias := filepath.Join(base, "alias")
				if err := os.Symlink(ipc, alias); err != nil {
					t.Fatal(err)
				}
				ipc = alias
			case "tmp-replacement":
				temp, err := prepareRunTempDirectory(ipc)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(temp.path, filepath.Join(ipc, "old-tmp")); err != nil {
					t.Fatal(err)
				}
				permissionFixtureDirectory(t, ipc, ".tmp")
				if err := temp.validate(); err == nil {
					t.Fatal("replaced .tmp inode was accepted")
				}
				return
			}
			if _, err := prepareRunTempDirectory(ipc); err == nil {
				t.Fatal("unsafe IPC/temp was accepted")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatal("temp preparation followed an alias outside IPC")
			}
		})
	}
}
