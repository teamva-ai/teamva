//go:build darwin

package opencode

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
)

const macOSSandboxExecutable = "/usr/bin/sandbox-exec"

func platformSandboxAvailable() error {
	if info, err := os.Stat(macOSSandboxExecutable); err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return errors.New("macOS sandbox-exec is unavailable; OpenCode Run cannot safely enable shell")
	}
	return nil
}

func platformSandboxCommand(profile, executable string, args ...string) (*exec.Cmd, error) {
	if err := platformSandboxAvailable(); err != nil {
		return nil, err
	}
	arguments := append([]string{"-f", profile, executable}, args...)
	return exec.Command(macOSSandboxExecutable, arguments...), nil
}

func platformValidateSandbox(ctx context.Context, sandbox *writeSandbox) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, macOSSandboxExecutable, "-f", sandbox.profilePath, "/usr/bin/true")
	cmd.Dir = sandbox.cwd
	// No credentials or native model bootstrap are involved in this preflight.
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("macOS OS write-sandbox initialization failed; OpenCode Run refused")
	}
	return nil
}

func platformSandboxToolEnvironment(ctx context.Context, private string) (map[string]string, error) {
	result := map[string]string{}
	git, err := exec.LookPath("git")
	if err != nil || git != "/usr/bin/git" {
		return result, nil // Respect an explicitly selected Homebrew/custom Git.
	}
	developer := os.Getenv("DEVELOPER_DIR")
	if developer == "" {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/usr/bin/xcode-select", "-p")
		// Read only the tool selection, not Provider credentials or account data.
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + os.Getenv("HOME")}
		output, err := cmd.Output()
		if err != nil {
			return result, nil // The documented no-cache shim remains available.
		}
		developer = strings.TrimSpace(string(output))
	}
	if !filepath.IsAbs(developer) || strings.ContainsAny(developer, "\r\n\x00") {
		return result, nil
	}
	actual := filepath.Join(developer, "usr", "bin", "git")
	info, err := os.Stat(actual)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return result, nil
	}
	bin, err := sandboxPrivateDirectory(private, "bin")
	if err != nil {
		return nil, err
	}
	// Avoid /usr/bin/git's xcrun shim repeatedly consulting a global cache or
	// spending several seconds on uncached lookup. No binary/config is copied.
	temp, err := os.CreateTemp(bin, ".git-link-*")
	if err != nil {
		return nil, errors.New("prepare private opencode Git lookup failed")
	}
	name := temp.Name()
	_ = temp.Close()
	_ = os.Remove(name)
	defer os.Remove(name)
	if os.Symlink(actual, name) != nil || os.Rename(name, filepath.Join(bin, "git")) != nil {
		return nil, errors.New("prepare private opencode Git lookup failed")
	}
	result["PATH"] = bin + string(os.PathListSeparator) + os.Getenv("PATH")
	return result, nil
}

// Closed-by-default Seatbelt policy. Reads/network are intentionally available
// for the model CLI and coding tools; only narrow platform IPC is enabled, not
// AppleEvents/launchd/task ports that would delegate arbitrary writes elsewhere.
const macOSWriteSandboxBase = `(version 1)
(deny default)
(allow file-read*)
(allow process-exec)
(allow process-fork)
(allow signal (target same-sandbox))
(allow process-info* (target same-sandbox))
(allow sysctl-read)
(allow sysctl-write (sysctl-name "kern.grade_cputype"))
(allow network*)
(allow system-socket (require-all (socket-domain AF_SYSTEM) (socket-protocol 2)))
(allow mach-lookup
  (global-name "com.apple.system.opendirectoryd.libinfo")
  (global-name "com.apple.system.opendirectoryd.membership")
  (global-name "com.apple.bsd.dirhelper")
  (global-name "com.apple.SecurityServer")
  (global-name "com.apple.networkd")
  (global-name "com.apple.ocspd")
  (global-name "com.apple.trustd.agent")
  (global-name "com.apple.SystemConfiguration.DNSConfiguration")
  (global-name "com.apple.SystemConfiguration.configd")
  (global-name "com.apple.FSEvents")
  (global-name "com.apple.cfprefsd.daemon")
  (global-name "com.apple.cfprefsd.agent")
  (local-name "com.apple.cfprefsd.agent")
  (global-name "com.apple.PowerManagement.control"))
(allow user-preference-read)
(allow ipc-posix-shm-read* (ipc-posix-name-prefix "apple.cfprefs."))
(allow ipc-posix-sem)
(allow pseudo-tty)
(allow file-read* file-write* file-ioctl (literal "/dev/ptmx"))
(allow file-read* file-write*
  (require-all (regex #"^/dev/ttys[0-9]+") (extension "com.apple.sandbox.pty")))
(allow file-ioctl (regex #"^/dev/ttys[0-9]+"))
(allow file-write-data (require-all (literal "/dev/null") (vnode-type CHARACTER-DEVICE)))
`

func platformWriteProfile(ctx context.Context, c *Client, request provider.RunRequest, config, data string) (string, error) {
	roots := []string{}
	if !(request.ReadOnly || c.readOnly || request.DisableTools || c.disableTools) {
		roots = append(roots, c.writableRoots...)
	}
	privateRoots := []string{data}
	if request.ToolRequestDir != "" && !request.DisableTools && !c.disableTools {
		ipc, err := permissionDirectory(request.ToolRequestDir)
		if err != nil {
			return "", errors.New("opencode OS write sandbox requires a real host IPC directory")
		}
		privateRoots = append(privateRoots, ipc)
	}
	var out strings.Builder
	out.WriteString(macOSWriteSandboxBase)
	pathRule := func(effect, matcher, path string) {
		out.WriteString("(" + effect + " file-write* (" + matcher + " " + strconv.Quote(path) + "))\n")
	}
	for _, value := range roots {
		root, err := permissionDirectory(value)
		if err != nil || root != value {
			return "", errors.New("opencode OS write root changed after authorization")
		}
		pathRule("allow", "subpath", root)
	}
	// Exclusions apply even when a host repository encompasses private config,
	// Skills or another Run's IPC. Private data may live beneath config only in
	// the public API's provider-local fallback, so its exception follows config.
	pathRule("deny", "subpath", config)
	pathRule("deny", "subpath", filepath.Join(request.CWD, ".teamva", "tool-requests"))
	for _, root := range privateRoots {
		pathRule("allow", "subpath", root)
	}
	for _, value := range c.readOnlySkillDirs {
		skill, err := permissionDirectory(value)
		if err != nil {
			return "", errors.New("opencode OS write sandbox cannot verify a bound Skill directory")
		}
		pathRule("deny", "subpath", skill)
	}
	// Seatbelt resolves symlinks, and creation of cross-boundary hardlinks is
	// denied, but a PREEXISTING hardlink has an allowed pathname too. Exclude
	// every preexisting multiply-linked file before spawning any agent code.
	// Local, external actors racing new links after this snapshot are outside
	// the supported threat model; no inode/openat-style guarantee is claimed.
	linked := map[string]bool{}
	readOnlyDirectories := []string{config, filepath.Join(request.CWD, ".teamva", "tool-requests")}
	readOnlyDirectories = append(readOnlyDirectories, c.readOnlySkillDirs...)
	for _, root := range append(roots, privateRoots...) {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() && path != root {
				for _, protected := range readOnlyDirectories {
					if path == protected {
						return fs.SkipDir // Explicit private roots are walked separately.
					}
				}
			}
			if !entry.Type().IsRegular() {
				return nil // Do not follow symlink directories or credential links.
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if stat, ok := info.Sys().(*syscall.Stat_t); !ok {
				return errors.New("unavailable inode metadata")
			} else if stat.Nlink > 1 {
				linked[path] = true
			}
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", errors.New("opencode OS write sandbox could not verify existing hardlinks")
		}
	}
	paths := make([]string, 0, len(linked))
	for path := range linked {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		pathRule("deny", "literal", path)
	}
	authPaths, err := c.protectedAuthPaths()
	if err != nil {
		return "", err
	}
	for _, path := range authPaths {
		// Original account targets are immutable even when they happen to lie
		// INSIDE an explicitly writable business root or private native data.
		pathRule("deny", "literal", path)
	}
	// A literal file exclusion alone is not enough: renaming one of its parent
	// directories would give the same hardlink a new allowed pathname. Prevent
	// unlink/rename of those ancestors, without denying ordinary child creation.
	ancestors := map[string]bool{}
	for _, path := range authPaths {
		for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
			ancestors[parent] = true
			if filepath.Dir(parent) == parent {
				break
			}
		}
	}
	for _, path := range paths {
		for _, root := range append(roots, privateRoots...) {
			if !sandboxContains(root, path) {
				continue
			}
			for parent := filepath.Dir(path); sandboxContains(root, parent); parent = filepath.Dir(parent) {
				ancestors[parent] = true
				if parent == root {
					break
				}
			}
		}
	}
	directories := make([]string, 0, len(ancestors))
	for directory := range ancestors {
		directories = append(directories, directory)
	}
	sort.Strings(directories)
	for _, directory := range directories {
		out.WriteString("(deny file-write-unlink (literal " + strconv.Quote(directory) + "))\n")
	}
	return out.String(), nil
}
