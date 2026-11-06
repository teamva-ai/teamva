package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

func TestDesktopUpdateCheckAndVerifiedDownload(t *testing.T) {
	assetName := desktopUpdateAssetForArch(goruntime.GOARCH)
	payload := []byte("desktop-update-zip")
	digest := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/VERSION":
			_, _ = writer.Write([]byte("1.2.0\n"))
		case "/SHA256SUMS":
			_, _ = fmt.Fprintf(writer, "%s  %s\n", hex.EncodeToString(digest[:]), assetName)
		case "/" + assetName:
			_, _ = writer.Write(payload)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	release, err := checkDesktopUpdate(t.Context(), "1.1.0", server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !release.Available || release.LatestVersion != "1.2.0" || release.Checksum != hex.EncodeToString(digest[:]) {
		t.Fatalf("release=%+v", release)
	}
	destination := filepath.Join(t.TempDir(), "Teamva update")
	archivePath, err := downloadDesktopUpdate(t.Context(), release, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if archivePath != destination+".zip" {
		t.Fatalf("archive path=%q", archivePath)
	}
	actual, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, payload) {
		t.Fatalf("downloaded payload=%q", actual)
	}
}

func TestDesktopUpdateDoesNotDowngradeOrFetchManifest(t *testing.T) {
	manifestRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/VERSION":
			_, _ = writer.Write([]byte("1.1.0"))
		case "/SHA256SUMS":
			manifestRequests++
			http.Error(writer, "unexpected", http.StatusInternalServerError)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	release, err := checkDesktopUpdate(t.Context(), "1.2.0", server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if release.Available || manifestRequests != 0 {
		t.Fatalf("release=%+v manifest requests=%d", release, manifestRequests)
	}
}

func TestDesktopUpdateRejectsChecksumMismatch(t *testing.T) {
	assetName := desktopUpdateAssetForArch(goruntime.GOARCH)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/"+assetName {
			_, _ = writer.Write([]byte("tampered"))
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "update.zip")
	_, err := downloadDesktopUpdate(t.Context(), desktopRelease{
		Available: true, AssetName: assetName, Checksum: fmt.Sprintf("%064d", 0), BaseURL: server.URL,
	}, destination, nil)
	if err == nil {
		t.Fatal("checksum mismatch was accepted")
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("failed update was published: %v", statErr)
	}
}

func TestDesktopUpdateFailurePreservesExistingArchive(t *testing.T) {
	assetName := desktopUpdateAssetForArch(goruntime.GOARCH)
	for _, failure := range []string{"checksum", "truncated response", "HTTP error"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch failure {
				case "truncated response":
					writer.Header().Set("Content-Length", "100")
				case "HTTP error":
					writer.WriteHeader(http.StatusServiceUnavailable)
				}
				_, _ = writer.Write([]byte("test-only-incomplete-update"))
			}))
			defer server.Close()
			directory := t.TempDir()
			destination := filepath.Join(directory, "update.zip")
			original := []byte("existing archive must survive a failed download")
			if err := os.WriteFile(destination, original, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := downloadDesktopUpdate(t.Context(), desktopRelease{
				Available: true, AssetName: assetName, Checksum: strings.Repeat("0", 64), BaseURL: server.URL,
			}, destination, nil)
			if err == nil {
				t.Fatal("failed download was accepted")
			}
			actual, err := os.ReadFile(destination)
			if err != nil || !bytes.Equal(actual, original) {
				t.Fatal("failed download replaced the existing archive")
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 1 || entries[0].Name() != "update.zip" {
				t.Fatal("failed download left a temporary archive")
			}
		})
	}
}

func TestDesktopUpdateURLRequiresHTTPSOutsideLoopback(t *testing.T) {
	if _, err := normalizeDesktopUpdateURL("http://updates.example.com/desktop"); err == nil {
		t.Fatal("public plaintext update URL was accepted")
	}
	if _, err := normalizeDesktopUpdateURL("https://user:password@updates.example.com/desktop"); err == nil {
		t.Fatal("credential-bearing update URL was accepted")
	}
}

func TestDesktopUpdateRejectsUnsafeRedirect(t *testing.T) {
	for _, target := range []string{
		"http://updates.example.test/desktop/VERSION",
		"https://user:password@updates.example.test/desktop/VERSION",
		"https://updates.example.test/desktop/VERSION?token=test-only-value",
	} {
		t.Run(target, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				http.Redirect(writer, request, target, http.StatusFound)
			}))
			defer server.Close()
			if _, err := checkDesktopUpdate(t.Context(), "1.0.0", server.URL, nil); err == nil || !strings.Contains(err.Error(), "桌面更新地址") {
				t.Fatal("unsafe redirected update URL was accepted")
			}
		})
	}
}

func TestDesktopUpdateRejectsWebsiteFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte("<!doctype html><title>Teamva</title>"))
	}))
	defer server.Close()

	_, err := checkDesktopUpdate(t.Context(), "dev", server.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "桌面更新制品尚未发布") {
		t.Fatalf("website fallback error=%v", err)
	}
}

func TestDesktopVersionComparisonUsesSemanticPrereleaseOrder(t *testing.T) {
	tests := []struct {
		candidate string
		current   string
		newer     bool
	}{
		{candidate: "1.2.0", current: "1.1.9", newer: true},
		{candidate: "1.2.0", current: "1.2.0", newer: false},
		{candidate: "1.2.0-rc.10", current: "1.2.0-rc.2", newer: true},
		{candidate: "1.2.0-rc.2", current: "1.2.0", newer: false},
		{candidate: "1.2.0", current: "dev", newer: true},
	}
	for _, test := range tests {
		if actual := isNewerDesktopVersion(test.candidate, test.current); actual != test.newer {
			t.Fatalf("isNewerDesktopVersion(%q, %q)=%t want %t", test.candidate, test.current, actual, test.newer)
		}
	}
}

func TestDesktopUpdateAssetMatchesArchitecture(t *testing.T) {
	if got := desktopUpdateAssetForArch("arm64"); got != "teamva-desktop-darwin-arm64.zip" {
		t.Fatalf("arm64 asset = %q", got)
	}
	if got := desktopUpdateAssetForArch("amd64"); got != "teamva-desktop-darwin-x64.zip" {
		t.Fatalf("amd64 asset = %q", got)
	}
}
