package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	serverapp "github.com/teamva-ai/teamva/server/app"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const diagnosticLogLimit = desktopLogMaxBytes

type diagnosticConfig struct {
	DataDir      string
	AppVersion   string
	ServiceReady bool
	GeneratedAt  time.Time
}

type diagnosticManifest struct {
	FormatVersion int              `json:"format_version"`
	GeneratedAt   time.Time        `json:"generated_at"`
	AppVersion    string           `json:"app_version"`
	GoVersion     string           `json:"go_version"`
	OS            string           `json:"os"`
	Architecture  string           `json:"architecture"`
	ServiceReady  bool             `json:"service_ready"`
	Files         []diagnosticFile `json:"files"`
	Notices       []string         `json:"notices,omitempty"`
}

type diagnosticFile struct {
	Kind          string    `json:"kind"`
	Path          string    `json:"path"`
	Size          int64     `json:"size"`
	ModifiedAt    time.Time `json:"modified_at"`
	ArchivedBytes int       `json:"archived_bytes,omitempty"`
}

func (a *desktopApp) exportDiagnostics() {
	a.withWailsContext(func(ctx context.Context) {
		destination, err := runtime.SaveFileDialog(ctx, runtime.SaveDialogOptions{
			Title:           "导出诊断信息",
			DefaultFilename: "Teamva-diagnostics-" + time.Now().UTC().Format("20060102-150405") + ".zip",
			Filters:         []runtime.FileFilter{{DisplayName: "ZIP 压缩包 (*.zip)", Pattern: "*.zip"}},
		})
		if err != nil {
			a.showDiagnosticError(ctx, err)
			return
		}
		if strings.TrimSpace(destination) == "" {
			return
		}
		dataDir, err := a.localDataDirectory()
		if err != nil {
			a.showDiagnosticError(ctx, err)
			return
		}
		archivePath, err := createDiagnosticsArchive(destination, diagnosticConfig{
			DataDir: dataDir, AppVersion: a.version,
			ServiceReady: a.serviceReady(), GeneratedAt: time.Now().UTC(),
		})
		if err != nil {
			a.showDiagnosticError(ctx, err)
			return
		}
		_, _ = runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
			Type: runtime.InfoDialog, Title: "诊断信息已导出", Message: archivePath,
		})
	})
}

func (a *desktopApp) showDiagnosticError(ctx context.Context, err error) {
	runtime.LogErrorf(ctx, "导出诊断信息失败: %v", err)
	_, _ = runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
		Type: runtime.ErrorDialog, Title: "无法导出诊断信息", Message: err.Error(),
	})
}

func (a *desktopApp) localDataDirectory() (string, error) {
	if strings.TrimSpace(a.dataDir) != "" {
		return a.dataDir, nil
	}
	return serverapp.DefaultLocalDataDir()
}

func (a *desktopApp) serviceReady() bool {
	a.mu.RLock()
	service, startErr := a.service, a.startErr
	a.mu.RUnlock()
	if service == nil || startErr != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, service.Endpoint().ReadyURL, nil)
	if err != nil {
		return false
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func createDiagnosticsArchive(destination string, config diagnosticConfig) (archivePath string, returnErr error) {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return "", errors.New("diagnostic archive destination is required")
	}
	if !strings.EqualFold(filepath.Ext(destination), ".zip") {
		destination += ".zip"
	}
	if strings.TrimSpace(config.DataDir) == "" {
		return "", errors.New("diagnostic data directory is required")
	}
	if config.GeneratedAt.IsZero() {
		config.GeneratedAt = time.Now().UTC()
	}
	parent := filepath.Dir(destination)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("destination parent is not a directory")
		}
		return "", fmt.Errorf("validate diagnostic destination: %w", err)
	}

	temporary, err := os.CreateTemp(parent, ".teamva-diagnostics-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create diagnostic archive: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if returnErr != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return "", fmt.Errorf("secure diagnostic archive: %w", err)
	}

	manifest := diagnosticManifest{
		FormatVersion: 1, GeneratedAt: config.GeneratedAt, AppVersion: config.AppVersion,
		GoVersion: goruntime.Version(), OS: goruntime.GOOS, Architecture: goruntime.GOARCH,
		ServiceReady: config.ServiceReady,
		Notices: []string{
			"Database contents and runtime bootstrap state are intentionally excluded.",
			"Log values matching common credential fields are redacted during export.",
		},
	}
	archive := zip.NewWriter(temporary)
	for _, relativePath := range []string{"app.db", filepath.Join("runtime", "runtime.db")} {
		metadata, metadataErr := inspectDiagnosticFile(config.DataDir, relativePath, "database")
		if metadataErr != nil {
			if !os.IsNotExist(metadataErr) {
				manifest.Notices = append(manifest.Notices, filepath.ToSlash(relativePath)+": source unavailable")
			}
			continue
		}
		manifest.Files = append(manifest.Files, metadata)
	}
	for index := 0; index <= desktopLogBackups; index++ {
		name := "app.log"
		if index > 0 {
			name = fmt.Sprintf("app.log.%d", index)
		}
		relativePath := filepath.Join("logs", name)
		metadata, metadataErr := inspectDiagnosticFile(config.DataDir, relativePath, "log")
		if metadataErr != nil {
			if !os.IsNotExist(metadataErr) {
				manifest.Notices = append(manifest.Notices, filepath.ToSlash(relativePath)+": source unavailable")
			}
			continue
		}
		archivedBytes, addErr := addDiagnosticLog(archive, config.DataDir, relativePath, metadata.Size)
		if addErr != nil {
			manifest.Notices = append(manifest.Notices, filepath.ToSlash(relativePath)+": could not be archived")
			continue
		}
		metadata.ArchivedBytes = archivedBytes
		manifest.Files = append(manifest.Files, metadata)
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode diagnostic manifest: %w", err)
	}
	manifestWriter, err := archive.CreateHeader(&zip.FileHeader{
		Name: "diagnostics.json", Method: zip.Deflate, Modified: config.GeneratedAt,
	})
	if err != nil {
		return "", fmt.Errorf("create diagnostic manifest: %w", err)
	}
	if _, err := manifestWriter.Write(manifestJSON); err != nil {
		return "", fmt.Errorf("write diagnostic manifest: %w", err)
	}
	if err := archive.Close(); err != nil {
		return "", fmt.Errorf("finish diagnostic archive: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return "", fmt.Errorf("sync diagnostic archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close diagnostic archive: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return "", fmt.Errorf("publish diagnostic archive: %w", err)
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return "", fmt.Errorf("secure published diagnostic archive: %w", err)
	}
	return destination, nil
}

func inspectDiagnosticFile(dataDir, relativePath, kind string) (diagnosticFile, error) {
	path := filepath.Join(dataDir, relativePath)
	info, err := os.Lstat(path)
	if err != nil {
		return diagnosticFile{}, err
	}
	if !info.Mode().IsRegular() {
		return diagnosticFile{}, errors.New("source is not a regular file")
	}
	return diagnosticFile{
		Kind: kind, Path: filepath.ToSlash(relativePath), Size: info.Size(), ModifiedAt: info.ModTime().UTC(),
	}, nil
}

func addDiagnosticLog(archive *zip.Writer, dataDir, relativePath string, size int64) (int, error) {
	path := filepath.Join(dataDir, relativePath)
	sourceInfo, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !sourceInfo.Mode().IsRegular() {
		return 0, errors.New("source is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, openedInfo) {
		return 0, errors.New("source changed while preparing diagnostics")
	}
	start := int64(0)
	if size > diagnosticLogLimit {
		start = size - diagnosticLogLimit
		if _, err := file.Seek(start, io.SeekStart); err != nil {
			return 0, err
		}
	}
	content, err := io.ReadAll(io.LimitReader(file, diagnosticLogLimit))
	if err != nil {
		return 0, err
	}
	redacted := []byte(redactLogText(string(content)))
	header := &zip.FileHeader{
		Name: filepath.ToSlash(relativePath), Method: zip.Deflate, Modified: time.Now().UTC(),
	}
	writer, err := archive.CreateHeader(header)
	if err != nil {
		return 0, err
	}
	if _, err := writer.Write(redacted); err != nil {
		return 0, err
	}
	return len(redacted), nil
}
