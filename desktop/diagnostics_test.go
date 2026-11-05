package main

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateDiagnosticsArchiveExportsMetadataAndRedactedLogsOnly(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "private-user", "Teamva")
	for _, directory := range []string{dataDir, filepath.Join(dataDir, "runtime"), filepath.Join(dataDir, "logs")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dataDir, "app.db"), []byte("private application database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "runtime", "runtime.db"), []byte("private runtime database"), 0o600); err != nil {
		t.Fatal(err)
	}
	logContent := "ready nonce=private-nonce token=private-token\n" +
		`{"password":"test-only-prefix\"test-only-escaped-tail"}` + "\nAuthorization: Basic test-only-basic-credential\n"
	if err := os.WriteFile(filepath.Join(dataDir, "logs", "app.log"), []byte(logContent), 0o600); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "diagnostics")
	archivePath, err := createDiagnosticsArchive(destination, diagnosticConfig{
		DataDir: dataDir, AppVersion: "1.2.3", ServiceReady: true,
		GeneratedAt: time.Date(2026, time.August, 8, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if archivePath != destination+".zip" {
		t.Fatalf("archive path=%q", archivePath)
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("archive permissions=%o", info.Mode().Perm())
	}

	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	entries := make(map[string]string, len(archive.File))
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			_ = reader.Close()
			t.Fatal(err)
		}
		_ = reader.Close()
		entries[file.Name] = string(content)
	}
	if len(entries) != 2 || entries["diagnostics.json"] == "" || entries["logs/app.log"] == "" {
		t.Fatalf("archive entries=%v", entries)
	}
	allContent := entries["diagnostics.json"] + entries["logs/app.log"]
	for _, excluded := range []string{
		"private application database", "private runtime database", "private-nonce", "private-token", dataDir,
		"test-only-prefix", "test-only-escaped-tail", "test-only-basic-credential",
	} {
		if strings.Contains(allContent, excluded) {
			t.Fatalf("archive exposed %q: %s", excluded, allContent)
		}
	}
	if strings.Count(entries["logs/app.log"], "[REDACTED]") != 4 {
		t.Fatalf("redacted log=%q", entries["logs/app.log"])
	}
	var manifest diagnosticManifest
	if err := json.Unmarshal([]byte(entries["diagnostics.json"]), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.AppVersion != "1.2.3" || !manifest.ServiceReady || len(manifest.Files) != 3 {
		t.Fatalf("manifest=%+v", manifest)
	}
	for _, file := range manifest.Files {
		if strings.HasPrefix(file.Path, "/") || strings.Contains(file.Path, "private-user") {
			t.Fatalf("manifest exposed local path: %+v", file)
		}
	}
}

func TestCreateDiagnosticsArchiveSkipsSymlinkedLogs(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(root, "outside-secret.txt")
	if err := os.WriteFile(secretPath, []byte("outside secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretPath, filepath.Join(dataDir, "logs", "app.log")); err != nil {
		t.Fatal(err)
	}
	archivePath, err := createDiagnosticsArchive(filepath.Join(root, "diagnostics.zip"), diagnosticConfig{
		DataDir: dataDir, AppVersion: "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name == "logs/app.log" {
			t.Fatal("diagnostic archive followed a log symlink")
		}
	}
}
