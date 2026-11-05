package main

import (
	"embed"
	"io/fs"
	"log"
	"log/slog"

	serverapp "github.com/teamva-ai/teamva/server/app"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

// The production Web build is staged into frontend/dist before compiling.
// Wails keeps its own origin and reaches the embedded loopback service through
// desktopHTTPBridge, so production does not rely on cross-origin navigation.
//
//go:embed all:frontend/dist
var desktopAssets embed.FS

var version = "dev"

// Keep the production singleton identity stable. Acceptance builds may replace
// this value through -ldflags -X while using an isolated HOME, so they can test
// a newly built .app without stopping an already running installed copy.
var singleInstanceID = "77d24284-73ee-44c0-b24a-83f040dff5ad"

func main() {
	dataDir, dataDirErr := serverapp.DefaultLocalDataDir()
	var fileLogger *desktopLogger
	if dataDirErr == nil {
		fileLogger, dataDirErr = newDesktopLogger(dataDir)
	}
	if dataDirErr != nil {
		log.Printf("desktop file logging unavailable: %v", dataDirErr)
	} else {
		defer fileLogger.Close()
		log.SetOutput(fileLogger)
		log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC)
		slog.SetDefault(slog.New(slog.NewTextHandler(fileLogger, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}

	webAssets, err := fs.Sub(desktopAssets, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}
	app := newDesktopApp(webAssets, version)
	app.dataDir = dataDir
	if fileLogger != nil {
		app.logger = log.New(fileLogger, "embedded-runtime ", log.Ldate|log.Ltime|log.Lmicroseconds|log.LUTC)
	}
	err = wails.Run(&options.App{
		Title:     "Teamva",
		Width:     1440,
		Height:    900,
		MinWidth:  1040,
		MinHeight: 680,
		// Keep the outer window decorated and opaque. The OS window manager then
		// owns its corner geometry: rectangular on Windows 10, system-rounded on
		// Windows 11, and the matching native radius on each macOS release.
		Frameless:        false,
		BackgroundColour: &options.RGBA{R: 251, G: 251, B: 251, A: 255},
		AssetServer:      &assetserver.Options{Handler: newDesktopHTTPBridge(app)},
		Windows: &windows.Options{
			Theme:                windows.SystemDefault,
			WindowIsTranslucent:  false,
			WebviewIsTransparent: false,
		},
		Mac: &mac.Options{
			// HiddenInset retains a titled NSWindow (HideTitleBar=false); it only
			// extends content beneath the native title area. NSWindow still owns the
			// version-specific corners, shadow, resizing and traffic-light controls.
			TitleBar:             mac.TitleBarHiddenInset(),
			WindowIsTranslucent:  false,
			WebviewIsTransparent: false,
		},
		Menu:                     newApplicationMenu(app),
		Logger:                   fileLogger,
		OnStartup:                app.startup,
		OnShutdown:               app.shutdown,
		Bind:                     []interface{}{app},
		EnableDefaultContextMenu: false,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               singleInstanceID,
			OnSecondInstanceLaunch: app.secondInstance,
		},
	})
	if err != nil {
		log.Printf("Teamva exited: %v", err)
	}
}
