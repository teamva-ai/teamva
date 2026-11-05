package runtime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type resourceOpenerSpec struct {
	nodeprotocol.ResourceOpener
	program string
	args    func(string) []string
}

type executableLookup func(string) (string, error)
type pathLookup func(string) bool

func discoverResourceOpeners() (nodeprotocol.ResourceOpeners, []resourceOpenerSpec) {
	home, _ := os.UserHomeDir()
	return discoverResourceOpenersFor(goruntime.GOOS, home, exec.LookPath, func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

func discoverResourceOpenersFor(
	goos, home string, lookPath executableLookup, pathExists pathLookup,
) (nodeprotocol.ResourceOpeners, []resourceOpenerSpec) {
	specs := make([]resourceOpenerSpec, 0, 4)
	resolve := func(command string) string {
		path, err := lookPath(command)
		if err != nil {
			return ""
		}
		return path
	}
	add := func(id, kind, name, program string, args func(string) []string) {
		specs = append(specs, resourceOpenerSpec{
			ResourceOpener: nodeprotocol.ResourceOpener{
				ID: id, Kind: kind, Name: name, Available: program != "",
			},
			program: program, args: args,
		})
	}
	findApp := func(names ...string) string {
		for _, name := range names {
			for _, base := range []string{"/Applications", filepath.Join(home, "Applications")} {
				if pathExists(filepath.Join(base, name+".app")) {
					return name
				}
			}
		}
		return ""
	}
	addIDE := func(id, name, command string, macApps ...string) {
		if program := resolve(command); program != "" {
			add(id, "ide", name, program, func(path string) []string { return []string{path} })
			return
		}
		if goos == "darwin" {
			if appName := findApp(macApps...); appName != "" {
				program := resolve("open")
				add(id, "ide", name, program, func(path string) []string {
					return []string{"-a", appName, path}
				})
				return
			}
		}
		add(id, "ide", name, "", nil)
	}

	switch goos {
	case "darwin":
		open := resolve("open")
		add("finder", "file_manager", "Finder", open, func(path string) []string { return []string{path} })
		add("terminal", "terminal", "Terminal", open, func(path string) []string {
			return []string{"-a", "Terminal", path}
		})
		addIDE("zed", "Zed", "zed", "Zed", "Zed Preview")
		addIDE("vscode", "Visual Studio Code", "code", "Visual Studio Code")
	case "windows":
		explorer := resolve("explorer.exe")
		if explorer == "" {
			explorer = resolve("explorer")
		}
		add("explorer", "file_manager", "File Explorer", explorer, func(path string) []string { return []string{path} })
		if terminal := resolve("wt.exe"); terminal != "" {
			add("windows-terminal", "terminal", "Windows Terminal", terminal, func(path string) []string {
				return []string{"-d", path}
			})
		} else {
			cmd := resolve("cmd.exe")
			add("command-prompt", "terminal", "Command Prompt", cmd, func(string) []string { return []string{"/K"} })
		}
		zedProgram := resolve("zed.exe")
		if zedProgram == "" {
			candidate := filepath.Join(home, "AppData", "Local", "Programs", "Zed", "Zed.exe")
			if pathExists(candidate) {
				zedProgram = candidate
			}
		}
		add("zed", "ide", "Zed", zedProgram, func(path string) []string { return []string{path} })
		codeProgram := resolve("code.exe")
		if codeProgram == "" {
			candidate := filepath.Join(home, "AppData", "Local", "Programs", "Microsoft VS Code", "Code.exe")
			if pathExists(candidate) {
				codeProgram = candidate
			}
		}
		add("vscode", "ide", "Visual Studio Code", codeProgram, func(path string) []string { return []string{path} })
	default:
		fileManagers := []struct {
			id, name, command string
		}{
			{"nautilus", "Files (Nautilus)", "nautilus"},
			{"dolphin", "Dolphin", "dolphin"},
			{"nemo", "Nemo", "nemo"},
			{"thunar", "Thunar", "thunar"},
			{"pcmanfm", "PCManFM", "pcmanfm"},
			{"caja", "Caja", "caja"},
			{"file-manager", "System file manager", "xdg-open"},
		}
		managerFound := false
		for _, item := range fileManagers {
			if program := resolve(item.command); program != "" {
				add(item.id, "file_manager", item.name, program, func(path string) []string { return []string{path} })
				managerFound = true
				break
			}
		}
		if !managerFound {
			add("file-manager", "file_manager", "System file manager", "", nil)
		}

		terminals := []struct {
			id, name, command string
			args              func(string) []string
		}{
			{"gnome-terminal", "GNOME Terminal", "gnome-terminal", func(path string) []string { return []string{"--working-directory", path} }},
			{"konsole", "Konsole", "konsole", func(path string) []string { return []string{"--workdir", path} }},
			{"xfce4-terminal", "Xfce Terminal", "xfce4-terminal", func(path string) []string { return []string{"--working-directory", path} }},
			{"kitty", "Kitty", "kitty", func(path string) []string { return []string{"--directory", path} }},
			{"alacritty", "Alacritty", "alacritty", func(path string) []string { return []string{"--working-directory", path} }},
			{"xterm", "XTerm", "xterm", func(string) []string { return nil }},
		}
		terminalFound := false
		for _, item := range terminals {
			if program := resolve(item.command); program != "" {
				add(item.id, "terminal", item.name, program, item.args)
				terminalFound = true
				break
			}
		}
		if !terminalFound {
			add("terminal", "terminal", "Terminal", "", nil)
		}
		addIDE("zed", "Zed", "zed")
		addIDE("vscode", "Visual Studio Code", "code")
	}

	items := make([]nodeprotocol.ResourceOpener, 0, len(specs))
	for _, spec := range specs {
		items = append(items, spec.ResourceOpener)
	}
	return nodeprotocol.ResourceOpeners{Platform: goos, Items: items}, specs
}

func openResource(path, openerID string) (nodeprotocol.ResourceOpener, error) {
	if !filepath.IsAbs(path) {
		return nodeprotocol.ResourceOpener{}, errors.New("resource path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nodeprotocol.ResourceOpener{}, fmt.Errorf("resource path is unavailable: %w", err)
	}
	if !info.IsDir() {
		return nodeprotocol.ResourceOpener{}, errors.New("resource path is not a directory")
	}
	_, specs := discoverResourceOpeners()
	for _, spec := range specs {
		if spec.ID != openerID {
			continue
		}
		if !spec.Available || spec.program == "" {
			return nodeprotocol.ResourceOpener{}, fmt.Errorf("%s is not installed", spec.Name)
		}
		var args []string
		if spec.args != nil {
			args = spec.args(path)
		}
		command := exec.Command(spec.program, args...)
		// All launchers inherit the code directory as CWD. For terminals this is
		// the primary guarantee; provider-specific flags above preserve it when a
		// terminal reuses an existing process.
		command.Dir = path
		if err := command.Start(); err != nil {
			return nodeprotocol.ResourceOpener{}, fmt.Errorf("start %s: %w", spec.Name, err)
		}
		if err := command.Process.Release(); err != nil {
			return nodeprotocol.ResourceOpener{}, fmt.Errorf("release %s: %w", spec.Name, err)
		}
		return spec.ResourceOpener, nil
	}
	return nodeprotocol.ResourceOpener{}, errors.New("unsupported resource opener")
}
