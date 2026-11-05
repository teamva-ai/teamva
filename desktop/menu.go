package main

import (
	"context"
	"net/url"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func newApplicationMenu(app *desktopApp) *menu.Menu {
	applicationMenu := menu.NewMenuFromItems(menu.AppMenu())

	fileMenu := applicationMenu.AddSubmenu("文件")
	fileMenu.AddText("打开数据目录", keys.Combo("o", keys.CmdOrCtrlKey, keys.ShiftKey), func(*menu.CallbackData) {
		app.openDataDirectory()
	})
	fileMenu.AddSeparator()
	fileMenu.AddText("导出诊断信息…", nil, func(*menu.CallbackData) {
		app.exportDiagnostics()
	})

	viewMenu := applicationMenu.AddSubmenu("显示")
	viewMenu.AddText("重新载入", keys.CmdOrCtrl("r"), func(*menu.CallbackData) {
		app.withWailsContext(runtime.WindowReload)
	})
	viewMenu.AddText("显示主窗口", nil, func(*menu.CallbackData) {
		app.withWailsContext(func(ctx context.Context) {
			runtime.WindowUnminimise(ctx)
			runtime.WindowShow(ctx)
		})
	})

	applicationMenu.Append(menu.EditMenu())
	applicationMenu.Append(menu.WindowMenu())

	helpMenu := applicationMenu.AddSubmenu("帮助")
	helpMenu.AddText("检查更新…", nil, func(*menu.CallbackData) {
		app.checkForUpdates()
	})
	helpMenu.AddText("Teamva 网站", nil, func(*menu.CallbackData) {
		app.withWailsContext(func(ctx context.Context) {
			runtime.BrowserOpenURL(ctx, "https://teamva.dev")
		})
	})
	helpMenu.AddText("版本信息", nil, func(*menu.CallbackData) {
		app.showVersion()
	})
	return applicationMenu
}

func (a *desktopApp) withWailsContext(callback func(context.Context)) {
	a.mu.RLock()
	ctx := a.wails
	a.mu.RUnlock()
	if ctx != nil {
		callback(ctx)
	}
}

func (a *desktopApp) openDataDirectory() {
	a.withWailsContext(func(ctx context.Context) {
		dataDir, err := a.localDataDirectory()
		if err != nil {
			runtime.LogErrorf(ctx, "无法定位数据目录: %v", err)
			_, _ = runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
				Type: runtime.ErrorDialog, Title: "无法打开数据目录", Message: err.Error(),
			})
			return
		}
		runtime.BrowserOpenURL(ctx, dataDirectoryURL(dataDir))
	})
}

func dataDirectoryURL(dataDir string) string {
	return (&url.URL{Scheme: "file", Path: dataDir}).String()
}

func (a *desktopApp) showVersion() {
	a.withWailsContext(func(ctx context.Context) {
		_, _ = runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
			Type: runtime.InfoDialog, Title: "Teamva",
			Message: "版本 " + a.version,
		})
	})
}
