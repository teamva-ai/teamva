package embedded

import (
	"os"
	"path/filepath"
	"runtime"
)

// bundledExecutable resolves a provider runtime that ships with the app, or the
// repository's vendor directory during development, before falling back to a
// PATH lookup. Known layouts, in priority order:
//
//	<App>/Contents/Resources/<id>/<platform>/<command>
//	<App>/Contents/Resources/<id>/<command>
//	<repo>/vendor/<id>/<platform>/<command>   (walking up from CWD)
//	<dataDir>/providers/<id>/<platform>/<command>
//
// The returned path is absolute and executable, or empty when nothing is
// bundled. Bundled runtimes are never installed into PATH or the system.
// Release payloads name 64-bit x86 as x64 on every OS, unlike GOARCH's amd64.
func bundledPlatform() string {
	return bundledPlatformName(runtime.GOOS, runtime.GOARCH)
}

func bundledPlatformName(goos, goarch string) string {
	if goarch == "amd64" {
		goarch = "x64"
	}
	return goos + "-" + goarch
}

func bundledExecutable(id, command, dataDir string) string {
	if id == "" || command == "" {
		return ""
	}
	platform := bundledPlatform()
	candidates := make([]string, 0, 8)
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		candidates = append(candidates,
			filepath.Join(dir, "..", "Resources", id, platform, command),
			filepath.Join(dir, "..", "Resources", id, command),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for depth := 0; depth < 6; depth++ {
			candidates = append(candidates, filepath.Join(dir, "vendor", id, platform, command))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if dataDir != "" {
		candidates = append(candidates, filepath.Join(dataDir, "providers", id, platform, command))
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		return absolute
	}
	return ""
}
