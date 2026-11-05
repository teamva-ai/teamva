package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	serverapp "github.com/teamva-ai/teamva/server/app"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	webDir := strings.TrimSpace(os.Getenv("TEAMVA_LOCAL_WEB_DIR"))
	config := serverapp.LocalConfig{
		Address:        os.Getenv("TEAMVA_LOCAL_HTTP_ADDR"),
		DataDir:        os.Getenv("TEAMVA_LOCAL_DATA_DIR"),
		BootstrapNonce: os.Getenv("TEAMVA_LOCAL_BOOTSTRAP_NONCE"),
		DisableRuntime: strings.EqualFold(os.Getenv("TEAMVA_LOCAL_DISABLE_RUNTIME"), "true"),
		Version:        version,
		// Optional first-run automation seed. The Settings-managed password hash
		// and enabled state are authoritative on subsequent launches.
		AllowExternalAccess: strings.EqualFold(os.Getenv("TEAMVA_LOCAL_ALLOW_EXTERNAL"), "true"),
		AccessToken:         os.Getenv("TEAMVA_LOCAL_ACCESS_TOKEN"),
	}
	if webDir != "" {
		config.FrontendFS = os.DirFS(webDir)
	}
	// The token lives only in this process's configuration; drop it from the
	// environment so child Provider processes can never inherit the secret.
	_ = os.Unsetenv("TEAMVA_LOCAL_ACCESS_TOKEN")
	local, err := serverapp.StartLocal(ctx, config)
	if err != nil {
		slog.Error("start local Teamva", "error", err)
		os.Exit(1)
	}
	attributes := []any{
		"origin", local.Endpoint().Origin,
		"runtime_state", local.Endpoint().StateFile,
	}
	if external := local.Endpoint().ExternalURLs; len(external) > 0 {
		attributes = append(attributes,
			"external", strings.Join(external, ","),
			"external_auth", "password-only workbench login page; password managed in local Settings",
		)
	}
	slog.Info("local Teamva ready", attributes...)
	select {
	case <-ctx.Done():
	case err := <-local.Errors():
		slog.Error("local Teamva stopped unexpectedly", "error", err)
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := local.Close(shutdownContext); err != nil {
		slog.Error("close local Teamva", "error", err)
		os.Exit(1)
	}
}
