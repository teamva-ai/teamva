package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	serverapp "github.com/teamva-ai/teamva/server/app"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type desktopApp struct {
	webAssets fs.FS
	version   string
	logger    *log.Logger
	dataDir   string

	mu          sync.RWMutex
	wails       context.Context
	service     *serverapp.Local
	cancel      context.CancelFunc
	startErr    error
	closeOnce   sync.Once
	updateMu    sync.Mutex
	startupDone chan struct{}
}

func newDesktopApp(webAssets fs.FS, appVersion string) *desktopApp {
	return &desktopApp{
		webAssets: webAssets, version: appVersion,
		startupDone: make(chan struct{}),
	}
}

// startup runs after Wails has acquired the single-instance lock and before
// the bootstrap page is loaded. Starting the complete service here prevents a
// second app process from opening the shared SQLite databases.
func (a *desktopApp) startup(ctx context.Context) {
	serviceContext, cancel := context.WithCancel(context.Background())
	service, err := serverapp.StartLocal(serviceContext, serverapp.LocalConfig{
		Version:    a.version,
		FrontendFS: a.webAssets,
		Logger:     a.logger,
		DataDir:    a.dataDir,
	})
	if err == nil {
		err = waitForLocalReady(serviceContext, service.Endpoint().ReadyURL)
	}
	if err != nil {
		cancel()
		if service != nil {
			shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = service.Close(shutdownContext)
			shutdownCancel()
		}
	}

	a.mu.Lock()
	a.wails = ctx
	a.service = service
	a.cancel = cancel
	a.startErr = err
	a.mu.Unlock()
	close(a.startupDone)

	if service != nil && err == nil {
		go a.monitorService(ctx, service)
	}
}

// Wails may report DOM readiness while OnStartup is still bringing up the
// embedded HTTP service. The AssetServer bridge waits on this explicit barrier
// before it forwards the first page request.
func (a *desktopApp) waitForStartup(ctx context.Context) bool {
	select {
	case <-a.startupDone:
		return true
	case <-ctx.Done():
		return false
	}
}

func (a *desktopApp) shutdown(context.Context) {
	a.close()
}

// ChooseDirectory opens the native directory picker for the embedded desktop
// client. The web build detects the binding at runtime and keeps its in-app
// explorer as the cross-platform fallback.
func (a *desktopApp) ChooseDirectory() (string, error) {
	a.mu.RLock()
	ctx := a.wails
	a.mu.RUnlock()
	if ctx == nil {
		return "", errors.New("desktop window is not ready")
	}
	return runtime.OpenDirectoryDialog(ctx, runtime.OpenDialogOptions{
		Title:                "选择项目目录",
		CanCreateDirectories: true,
	})
}

func (a *desktopApp) secondInstance(options.SecondInstanceData) {
	a.mu.RLock()
	ctx := a.wails
	a.mu.RUnlock()
	if ctx == nil {
		return
	}
	runtime.WindowUnminimise(ctx)
	runtime.WindowShow(ctx)
}

func (a *desktopApp) monitorService(ctx context.Context, service *serverapp.Local) {
	select {
	case err := <-service.Errors():
		if err != nil {
			runtime.LogErrorf(ctx, "本地 HTTP 服务异常退出: %v", err)
			runtime.Quit(ctx)
		}
	case <-ctx.Done():
	}
}

func (a *desktopApp) close() {
	a.closeOnce.Do(func() {
		a.mu.RLock()
		service, cancel := a.service, a.cancel
		a.mu.RUnlock()
		if cancel != nil {
			cancel()
		}
		if service != nil {
			ctx, done := context.WithTimeout(context.Background(), 15*time.Second)
			defer done()
			if err := service.Close(ctx); err != nil {
				log.Printf("关闭本地服务失败: %v", err)
			}
		}
	})
}

func waitForLocalReady(ctx context.Context, readyURL string) error {
	client := &http.Client{Timeout: time.Second}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("本地服务未在 5 秒内就绪")
		case <-ticker.C:
		}
	}
}
