package runtime

import (
	"errors"
	"reflect"
	"testing"
)

func fakeExecutableLookup(commands map[string]string) executableLookup {
	return func(command string) (string, error) {
		if path := commands[command]; path != "" {
			return path, nil
		}
		return "", errors.New("not found")
	}
}

func openerSpecByID(t *testing.T, specs []resourceOpenerSpec, id string) resourceOpenerSpec {
	t.Helper()
	for _, spec := range specs {
		if spec.ID == id {
			return spec
		}
	}
	t.Fatalf("opener %q not found in %+v", id, specs)
	return resourceOpenerSpec{}
}

func TestDiscoverDarwinResourceOpenersIncludesSystemAndInstalledIDEs(t *testing.T) {
	result, specs := discoverResourceOpenersFor(
		"darwin", "/Users/test",
		fakeExecutableLookup(map[string]string{"open": "/usr/bin/open"}),
		func(path string) bool { return path == "/Applications/Zed.app" },
	)
	if result.Platform != "darwin" || len(result.Items) != 4 {
		t.Fatalf("result = %+v", result)
	}
	for _, id := range []string{"finder", "terminal", "zed"} {
		if item := openerSpecByID(t, specs, id); !item.Available {
			t.Fatalf("%s should be available: %+v", id, item)
		}
	}
	if item := openerSpecByID(t, specs, "vscode"); item.Available {
		t.Fatalf("vscode should be unavailable: %+v", item)
	}
	zed := openerSpecByID(t, specs, "zed")
	if zed.program != "/usr/bin/open" || !reflect.DeepEqual(zed.args("/repo"), []string{"-a", "Zed", "/repo"}) {
		t.Fatalf("zed launch = %s %+v", zed.program, zed.args("/repo"))
	}
	terminal := openerSpecByID(t, specs, "terminal")
	if !reflect.DeepEqual(terminal.args("/repo"), []string{"-a", "Terminal", "/repo"}) {
		t.Fatalf("terminal args = %+v", terminal.args("/repo"))
	}
}

func TestDiscoverLinuxResourceOpenersUsesInstalledDesktopTools(t *testing.T) {
	result, specs := discoverResourceOpenersFor(
		"linux", "/home/test",
		fakeExecutableLookup(map[string]string{
			"dolphin": "/usr/bin/dolphin", "konsole": "/usr/bin/konsole", "code": "/usr/bin/code",
		}),
		func(string) bool { return false },
	)
	if result.Platform != "linux" || len(result.Items) != 4 {
		t.Fatalf("result = %+v", result)
	}
	if item := openerSpecByID(t, specs, "dolphin"); !item.Available {
		t.Fatalf("dolphin = %+v", item)
	}
	konsole := openerSpecByID(t, specs, "konsole")
	if !reflect.DeepEqual(konsole.args("/repo"), []string{"--workdir", "/repo"}) {
		t.Fatalf("konsole args = %+v", konsole.args("/repo"))
	}
	if item := openerSpecByID(t, specs, "vscode"); !item.Available {
		t.Fatalf("vscode = %+v", item)
	}
	if item := openerSpecByID(t, specs, "zed"); item.Available {
		t.Fatalf("zed = %+v", item)
	}
}

func TestDiscoverWindowsResourceOpenersFallsBackToCommandPrompt(t *testing.T) {
	result, specs := discoverResourceOpenersFor(
		"windows", `C:\Users\test`,
		fakeExecutableLookup(map[string]string{
			"explorer.exe": `C:\Windows\explorer.exe`, "cmd.exe": `C:\Windows\System32\cmd.exe`,
		}),
		func(string) bool { return false },
	)
	if result.Platform != "windows" || len(result.Items) != 4 {
		t.Fatalf("result = %+v", result)
	}
	if item := openerSpecByID(t, specs, "explorer"); !item.Available {
		t.Fatalf("explorer = %+v", item)
	}
	terminal := openerSpecByID(t, specs, "command-prompt")
	if terminal.program == "" || !reflect.DeepEqual(terminal.args(`C:\repo`), []string{"/K"}) {
		t.Fatalf("command prompt = %+v", terminal)
	}
}

func TestOpenResourceRejectsUntrustedPathBeforeLaunching(t *testing.T) {
	if _, err := openResource("relative/path", "finder"); err == nil {
		t.Fatal("relative path was accepted")
	}
	if _, err := openResource(t.TempDir(), "not-supported"); err == nil {
		t.Fatal("unsupported opener was accepted")
	}
}
