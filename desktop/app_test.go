package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/menu"
)

func TestDesktopAppWaitsForStartupCompletion(t *testing.T) {
	app := newDesktopApp(nil, "test")
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if app.waitForStartup(cancelled) {
		t.Fatal("cancelled DOM context waited for an unfinished startup")
	}
	close(app.startupDone)
	if !app.waitForStartup(t.Context()) {
		t.Fatal("completed startup barrier was not observed")
	}
}

func TestProductionBundleAllowsLoopbackHTTP(t *testing.T) {
	contents, err := os.ReadFile("build/darwin/Info.plist")
	if err != nil {
		t.Fatal(err)
	}
	plist := string(contents)
	if !strings.Contains(plist, "<key>NSAppTransportSecurity</key>") ||
		!strings.Contains(plist, "<key>NSAllowsLocalNetworking</key>") {
		t.Fatal("production Info.plist does not allow the embedded loopback service")
	}
}

func TestWindowsInstallerPreservesUserData(t *testing.T) {
	contents, err := os.ReadFile("build/windows/installer/project.nsi")
	if err != nil {
		t.Fatal(err)
	}
	script := string(contents)
	for _, required := range []string{
		`InstallDir "$LOCALAPPDATA\Programs\${INFO_PRODUCTNAME}"`,
		`Resources\opencode\windows-x64`,
		`!insertmacro wails.writeUninstaller`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("installer missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(script), "rmdir /r") {
		t.Fatal("installer must not recursively remove user files")
	}
}

func TestApplicationMenuIncludesNativeAndDesktopActions(t *testing.T) {
	applicationMenu := newApplicationMenu(newDesktopApp(nil, "1.2.3"))
	if len(applicationMenu.Items) != 6 {
		t.Fatalf("menu items=%+v", applicationMenu.Items)
	}
	if applicationMenu.Items[0].Role != menu.AppMenuRole ||
		applicationMenu.Items[3].Role != menu.EditMenuRole ||
		applicationMenu.Items[4].Role != menu.WindowMenuRole {
		t.Fatalf("native roles=%+v", applicationMenu.Items)
	}
	fileMenu := applicationMenu.Items[1]
	if fileMenu.Label != "文件" || fileMenu.SubMenu == nil || len(fileMenu.SubMenu.Items) != 3 ||
		fileMenu.SubMenu.Items[0].Label != "打开数据目录" || fileMenu.SubMenu.Items[0].Accelerator == nil {
		t.Fatalf("file menu=%+v", fileMenu)
	}
	if fileMenu.SubMenu.Items[2].Label != "导出诊断信息…" {
		t.Fatalf("diagnostics menu=%+v", fileMenu.SubMenu.Items)
	}
	viewMenu := applicationMenu.Items[2]
	if viewMenu.Label != "显示" || viewMenu.SubMenu == nil || len(viewMenu.SubMenu.Items) != 2 {
		t.Fatalf("view menu=%+v", viewMenu)
	}
	helpMenu := applicationMenu.Items[5]
	if helpMenu.Label != "帮助" || helpMenu.SubMenu == nil || len(helpMenu.SubMenu.Items) != 3 ||
		helpMenu.SubMenu.Items[0].Label != "检查更新…" {
		t.Fatalf("help menu=%+v", helpMenu)
	}
}

func TestDataDirectoryURLPreservesSpaces(t *testing.T) {
	// The default data directory has no spaces, but a user-supplied
	// TEAMVA_LOCAL_DATA_DIR may, so the escape must keep working.
	if got := dataDirectoryURL("/Users/example/Team VA"); got != "file:///Users/example/Team%20VA" {
		t.Fatalf("data directory URL=%q", got)
	}
}
