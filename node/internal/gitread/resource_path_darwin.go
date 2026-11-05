package gitread

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// macOS ships these root-owned, fixed aliases. They are the only symlinks
// accepted in an already-approved Resource path; arbitrary user aliases are
// resolved only at explicit registration, outside this read boundary.
func normalizeTrustedResourceAlias(value string) (string, error) {
	for _, alias := range []string{"/var", "/tmp", "/etc"} {
		if value != alias && !strings.HasPrefix(value, alias+"/") {
			continue
		}
		info, err := os.Lstat(alias)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return "", errors.New("system resource alias is unavailable")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return "", errors.New("system resource alias is not trusted")
		}
		target, err := os.Readlink(alias)
		if !filepath.IsAbs(target) {
			target = filepath.Join("/", target)
		}
		if err != nil || filepath.Clean(target) != "/private"+alias {
			return "", errors.New("system resource alias changed")
		}
		return "/private" + value, nil
	}
	return value, nil
}
