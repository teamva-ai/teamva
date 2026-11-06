package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/mod/semver"
)

const desktopUpdateBaseURL = "https://teamva.dev/downloads/desktop"

func desktopUpdateAssetForArch(arch string) string {
	if arch == "arm64" {
		return "teamva-desktop-darwin-arm64.zip"
	}
	return "teamva-desktop-darwin-x64.zip"
}

type desktopRelease struct {
	CurrentVersion string
	LatestVersion  string
	Available      bool
	AssetName      string
	Checksum       string
	BaseURL        string
}

func (a *desktopApp) checkForUpdates() {
	a.withWailsContext(func(ctx context.Context) {
		go a.runUpdateCheck(ctx)
	})
}

func (a *desktopApp) runUpdateCheck(wailsContext context.Context) {
	if !a.updateMu.TryLock() {
		return
	}
	defer a.updateMu.Unlock()
	ctx, cancel := context.WithTimeout(wailsContext, 30*time.Second)
	release, err := checkDesktopUpdate(ctx, a.version, desktopUpdateBaseURL, nil)
	cancel()
	if err != nil {
		a.showUpdateError(wailsContext, err)
		return
	}
	if !release.Available {
		_, _ = runtime.MessageDialog(wailsContext, runtime.MessageDialogOptions{
			Type: runtime.InfoDialog, Title: "已是最新版本",
			Message: "当前版本 " + a.version,
		})
		return
	}
	choice, err := runtime.MessageDialog(wailsContext, runtime.MessageDialogOptions{
		Type: runtime.QuestionDialog, Title: "发现新版本 " + release.LatestVersion,
		Message: "将下载经过 SHA-256 校验、使用 ad-hoc 签名的 macOS 应用压缩包（未公证）。下载后请退出当前应用，解压并替换旧版本。",
		Buttons: []string{"下载更新", "取消"}, DefaultButton: "下载更新", CancelButton: "取消",
	})
	if err != nil || choice != "下载更新" {
		if err != nil {
			a.showUpdateError(wailsContext, err)
		}
		return
	}
	destination, err := runtime.SaveFileDialog(wailsContext, runtime.SaveDialogOptions{
		Title: "保存 Teamva 更新", DefaultFilename: release.AssetName,
		Filters: []runtime.FileFilter{{DisplayName: "ZIP 压缩包 (*.zip)", Pattern: "*.zip"}},
	})
	if err != nil {
		a.showUpdateError(wailsContext, err)
		return
	}
	if strings.TrimSpace(destination) == "" {
		return
	}
	downloadContext, downloadCancel := context.WithTimeout(wailsContext, 5*time.Minute)
	path, err := downloadDesktopUpdate(downloadContext, release, destination, nil)
	downloadCancel()
	if err != nil {
		a.showUpdateError(wailsContext, err)
		return
	}
	_, _ = runtime.MessageDialog(wailsContext, runtime.MessageDialogOptions{
		Type: runtime.InfoDialog, Title: "更新已下载",
		Message: path + "\n\nSHA-256: " + release.Checksum + "\n\n请退出当前应用后解压并替换旧版本。",
	})
}

func (a *desktopApp) showUpdateError(ctx context.Context, err error) {
	runtime.LogErrorf(ctx, "桌面更新失败: %v", err)
	_, _ = runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
		Type: runtime.ErrorDialog, Title: "无法检查或下载更新", Message: err.Error(),
	})
}

func checkDesktopUpdate(
	ctx context.Context, currentVersion, rawBaseURL string, client *http.Client,
) (desktopRelease, error) {
	baseURL, err := normalizeDesktopUpdateURL(rawBaseURL)
	if err != nil {
		return desktopRelease{}, err
	}
	client = desktopUpdateClient(client)
	versionPayload, err := downloadDesktopAsset(ctx, client, baseURL+"/VERSION", 64<<10)
	if err != nil {
		return desktopRelease{}, fmt.Errorf("检查桌面版本失败: %w", err)
	}
	latestVersion := strings.TrimSpace(string(versionPayload))
	latestSemantic := canonicalSemanticVersion(latestVersion)
	if !semver.IsValid(latestSemantic) {
		return desktopRelease{}, fmt.Errorf("更新服务返回了无效版本号 %q", latestVersion)
	}
	release := desktopRelease{
		CurrentVersion: currentVersion, LatestVersion: latestVersion,
		Available: isNewerDesktopVersion(latestVersion, currentVersion),
		AssetName: desktopUpdateAssetForArch(goruntime.GOARCH), BaseURL: baseURL,
	}
	if !release.Available {
		return release, nil
	}
	manifest, err := downloadDesktopAsset(ctx, client, baseURL+"/SHA256SUMS", 1<<20)
	if err != nil {
		return desktopRelease{}, fmt.Errorf("下载桌面更新校验清单失败: %w", err)
	}
	release.Checksum, err = desktopChecksum(string(manifest), release.AssetName)
	if err != nil {
		return desktopRelease{}, err
	}
	return release, nil
}

func downloadDesktopUpdate(
	ctx context.Context, release desktopRelease, destination string, client *http.Client,
) (archivePath string, returnErr error) {
	if !release.Available || release.AssetName == "" || release.Checksum == "" {
		return "", errors.New("桌面更新信息不完整")
	}
	baseURL, err := normalizeDesktopUpdateURL(release.BaseURL)
	if err != nil {
		return "", err
	}
	if filepath.Base(release.AssetName) != release.AssetName {
		return "", errors.New("桌面更新文件名无效")
	}
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return "", errors.New("桌面更新保存路径不能为空")
	}
	if !strings.EqualFold(filepath.Ext(destination), ".zip") {
		destination += ".zip"
	}
	parent := filepath.Dir(destination)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("保存目录无效")
		}
		return "", err
	}
	temporary, err := os.CreateTemp(parent, ".teamva-desktop-update-*.tmp")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer func() {
		if returnErr != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return "", err
	}
	response, err := requestDesktopAsset(ctx, desktopUpdateClient(client), baseURL+"/"+release.AssetName)
	if err != nil {
		return "", fmt.Errorf("下载桌面更新失败: %w", err)
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(response.Body, (500<<20)+1))
	closeErr := response.Body.Close()
	if copyErr != nil {
		return "", fmt.Errorf("下载桌面更新失败: %w", copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("关闭桌面更新响应失败: %w", closeErr)
	}
	if written > 500<<20 {
		return "", errors.New("桌面更新文件超过大小限制")
	}
	if !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), release.Checksum) {
		return "", errors.New("桌面更新文件 SHA-256 校验失败")
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return "", err
	}
	return destination, nil
}

func desktopUpdateClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	clone := *client
	previousRedirect := client.CheckRedirect
	clone.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if _, err := normalizeDesktopUpdateURL(request.URL.String()); err != nil {
			return err
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return &clone
}

func normalizeDesktopUpdateURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("桌面更新地址无效")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("桌面更新地址不能包含查询参数或片段")
	}
	host := parsed.Hostname()
	if parsed.Scheme == "http" && host != "localhost" {
		address := net.ParseIP(host)
		if address == nil || !address.IsLoopback() {
			return "", errors.New("桌面更新地址必须使用 HTTPS")
		}
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func downloadDesktopAsset(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	response, err := requestDesktopAsset(ctx, client, address)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, errors.New("桌面更新文件超过大小限制")
	}
	return payload, nil
}

func requestDesktopAsset(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/octet-stream, text/plain")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])); mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		_ = response.Body.Close()
		return nil, errors.New("桌面更新制品尚未发布（更新地址返回了网页）")
	}
	return response, nil
}

func desktopChecksum(manifest, asset string) (string, error) {
	for _, line := range strings.Split(manifest, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		if len(fields[0]) == sha256.Size*2 {
			if _, err := hex.DecodeString(fields[0]); err == nil {
				return strings.ToLower(fields[0]), nil
			}
		}
		break
	}
	return "", fmt.Errorf("桌面更新校验清单中没有 %s", asset)
}

func isNewerDesktopVersion(candidate, current string) bool {
	candidate = canonicalSemanticVersion(candidate)
	current = canonicalSemanticVersion(current)
	if !semver.IsValid(candidate) {
		return false
	}
	if !semver.IsValid(current) {
		return candidate != current
	}
	return semver.Compare(candidate, current) > 0
}

func canonicalSemanticVersion(value string) string {
	value = strings.TrimSpace(value)
	if value != "" && !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	return value
}
