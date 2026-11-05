package computeruse

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeFakeBinary(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectFindsBinaryInCommonBinDirWhenPATHMissesIt(t *testing.T) {
	t.Setenv("PATH", "/nonexistent-for-test")
	binDir := t.TempDir()
	writeFakeBinary(t, binDir, ServerName, "#!/bin/sh\necho 'v0.3.1'\n")

	originalDirs := CommonBinDirs
	originalNpm := CommonNpmBins
	t.Cleanup(func() { CommonBinDirs, CommonNpmBins = originalDirs, originalNpm })
	CommonBinDirs = []string{binDir}
	CommonNpmBins = nil

	status := Detect()
	if !status.Binary.Installed || status.Binary.Version != "v0.3.1" || status.Binary.Path != filepath.Join(binDir, ServerName) {
		t.Fatalf("status=%+v", status)
	}
	if !status.Supported {
		t.Fatalf("platform should be supported on %s: %+v", runtime.GOOS, status)
	}
}

func TestDetectReportsMissingBinary(t *testing.T) {
	t.Setenv("PATH", "/nonexistent-for-test")
	originalDirs := CommonBinDirs
	t.Cleanup(func() { CommonBinDirs = originalDirs })
	CommonBinDirs = []string{t.TempDir()}

	status := Detect()
	if status.Binary.Installed {
		t.Fatalf("unexpectedly installed: %+v", status)
	}
	if status.Binary.Error == "" {
		t.Fatalf("missing binary error is empty: %+v", status)
	}
}

func TestInstallRunsNpmGlobalInstallAndRefreshesStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("npm fake script is POSIX-only")
	}
	t.Setenv("PATH", "/nonexistent-for-test")
	binDir := t.TempDir()
	npmDir := t.TempDir()
	writeFakeBinary(t, npmDir, "npm", "#!/bin/sh\necho installing >&2\nexit 0\n")
	writeFakeBinary(t, binDir, ServerName, "#!/bin/sh\necho 'v1.2.3'\n")

	originalDirs := CommonBinDirs
	originalNpm := CommonNpmBins
	t.Cleanup(func() { CommonBinDirs, CommonNpmBins = originalDirs, originalNpm })
	CommonBinDirs = []string{binDir}
	CommonNpmBins = []string{filepath.Join(npmDir, "npm")}

	status, output, err := Install(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Binary.Installed || status.Binary.Version != "v1.2.3" {
		t.Fatalf("status=%+v", status)
	}
	if !strings.Contains(output, "installing") {
		t.Fatalf("output=%q", output)
	}
}

func TestInstallFailsWithoutNPM(t *testing.T) {
	t.Setenv("PATH", "/nonexistent-for-test")
	originalNpm := CommonNpmBins
	originalBin := CommonBinDirs
	t.Cleanup(func() { CommonNpmBins, CommonBinDirs = originalNpm, originalBin })
	CommonNpmBins = nil
	CommonBinDirs = nil
	// 同时把 PATH 里的 npm 也挡掉：通过临时 PATH 环境检测不了，直接断言错误即可。
	_, _, err := Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "npm") {
		t.Fatalf("expected npm-missing error, got %v", err)
	}
}
