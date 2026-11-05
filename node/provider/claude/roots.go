package claude

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// A scope authorizes directory identities, not names that can later be rebound.
// Keep the complete ancestor chain open and verify each name -> inode binding.
// Operations clone only the pinned descriptor; they never OpenRoot(path) again.
type directoryBinding struct {
	parent   *os.Root
	name     string
	identity os.FileInfo
}
type pinnedDirectory struct {
	path     string
	root     *os.Root
	handles  []*os.Root
	bindings []directoryBinding
}

func absoluteScopePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("directory path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("directory path is invalid")
	}
	path = filepath.Clean(path)
	// macOS's fixed OS aliases precede user-controlled workspace components.
	// Resolve only these exact system-owned aliases, never an arbitrary root or
	// ancestor supplied by a project. Go's TempDir commonly returns /var/....
	if runtime.GOOS == "darwin" {
		for _, alias := range []string{"/var", "/tmp", "/etc"} {
			if path == alias || strings.HasPrefix(path, alias+"/") {
				if target, err := os.Readlink(alias); err == nil && target == "private"+alias {
					path = "/private" + path
				}
				break
			}
		}
	}
	return path, nil
}

func pinDirectory(path string) (pinned *pinnedDirectory, err error) {
	path, err = absoluteScopePath(path)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(path)
	root, err := os.OpenRoot(volume + string(filepath.Separator))
	if err != nil {
		return nil, errors.New("open filesystem root failed")
	}
	p := &pinnedDirectory{path: path, root: root, handles: []*os.Root{root}}
	defer func() {
		if err != nil {
			p.close()
		}
	}()
	for _, part := range strings.Split(strings.TrimPrefix(path, volume+string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		before, statErr := p.root.Lstat(part)
		if statErr != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("authorized directory ancestor is unavailable or is a symlink")
		}
		next, openErr := openChildRoot(p.root, part)
		if openErr != nil {
			return nil, errors.New("pin authorized directory ancestor failed")
		}
		p.handles = append(p.handles, next)
		identity, identityErr := next.Stat(".")
		after, afterErr := p.root.Lstat(part)
		if identityErr != nil || afterErr != nil || !identity.IsDir() || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, identity) || !os.SameFile(after, identity) {
			return nil, errors.New("authorized directory identity changed while pinning")
		}
		p.bindings = append(p.bindings, directoryBinding{parent: p.root, name: part, identity: identity})
		p.root = next
	}
	if err = p.validate(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *pinnedDirectory) validate() error {
	for _, binding := range p.bindings {
		current, err := binding.parent.Lstat(binding.name)
		if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(current, binding.identity) {
			return errors.New("authorized directory or ancestor identity changed")
		}
	}
	return nil
}
func (p *pinnedDirectory) clone() (*os.Root, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	root, err := openChildRoot(p.root, ".")
	if err != nil {
		return nil, errors.New("clone authorized directory descriptor failed")
	}
	return root, nil
}
func (p *pinnedDirectory) close() {
	for i := len(p.handles) - 1; i >= 0; i-- {
		_ = p.handles[i].Close()
	}
	p.handles = nil
}
