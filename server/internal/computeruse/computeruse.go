// Package computeruse 管理本机 open-computer-use 官方 MCP server 的安装引导：
// 二进制状态检测与一键 npm 全局安装。这里是"安装指引"而不是配置注入；
// Computer Use 的 MCP 配置由每次 Run 的实例配置生成时按开关注入。
package computeruse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// ServerName 与上游 open-computer-use 一致的 npm 包名/命令名。
	ServerName = "open-computer-use"

	installTimeout  = 5 * time.Minute
	versionTimeout  = 5 * time.Second
	npmProbeTimeout = 5 * time.Second
	maxOutputBytes  = 8 << 10
)

// Binary 描述本机 open-computer-use 命令的检测结果。
type Binary struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Status 是 GET /computer-use 的状态投影。
type Status struct {
	Binary    Binary   `json:"binary"`
	NPM       string   `json:"npm_path,omitempty"`
	Platform  string   `json:"platform"`
	Supported bool     `json:"supported"`
	Errors    []string `json:"errors,omitempty"`
}

var (
	// CommonBinDirs 覆盖 macOS/Linux 常见 npm 全局 bin；GUI 应用进程的
	// PATH 通常不包含这些目录，因此状态检测必须额外扫一遍。
	CommonBinDirs = []string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"~/.npm-global/bin",
		"~/.local/bin",
		"~/.bin",
	}
	// CommonNpmBins 同上，用于找到 npm 本身。
	CommonNpmBins = []string{
		"/opt/homebrew/bin/npm",
		"/usr/local/bin/npm",
		"~/.npm-global/bin/npm",
	}
)

// Detect 检测 open-computer-use 与 npm 的本机状态。
func Detect() Status {
	status := Status{Platform: runtime.GOOS, Supported: currentPlatformSupported()}
	if !status.Supported {
		status.Errors = append(status.Errors, "Computer Use 官方 MCP 仅支持 macOS/Linux/Windows")
		status.Binary.Error = "当前平台不受支持"
		return status
	}
	status.NPM = findNPM()
	path, found := findBinary(ServerName)
	if !found {
		status.Binary.Error = "未找到 " + ServerName + "；点击安装即可通过 npm 全局安装（需要 Node.js）"
		return status
	}
	status.Binary.Path = path
	version, err := readVersion(path)
	if err != nil {
		status.Binary.Error = fmt.Sprintf("读取版本失败: %v", err)
		return status
	}
	status.Binary.Installed = true
	status.Binary.Version = version
	return status
}

// Install 通过 npm 全局安装 open-computer-use。返回安装后的状态与输出摘要。
func Install(ctx context.Context) (Status, string, error) {
	npm := findNPM()
	if npm == "" {
		return Status{}, "", errors.New("本机未找到 npm/node；请先安装 Node.js 后再安装 " + ServerName)
	}
	installCtx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	command := exec.CommandContext(installCtx, npm, "install", "-g", ServerName)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr := command.Run()
	summary := trimmedSummary(stdout.String(), stderr.String())
	detected := Detect()
	if runErr != nil {
		return detected, summary, fmt.Errorf("npm install 失败: %s", summary)
	}
	if !detected.Binary.Installed {
		return detected, summary, fmt.Errorf("npm install 完成但 %s 仍不可用；请检查 npm 全局 bin 是否在当前 PATH", ServerName)
	}
	return detected, summary, nil
}

func readVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, "--version")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		details := strings.TrimSpace(stderr.String())
		if details == "" {
			details = err.Error()
		}
		return "", errors.New(truncate(details, 200))
	}
	return strings.TrimSpace(truncate(stdout.String(), 200)), nil
}

func findNPM() string {
	if path, err := exec.LookPath("npm"); err == nil {
		return path
	}
	for _, candidate := range CommonNpmBins {
		if path := expandHome(candidate); executableFile(path) {
			return path
		}
	}
	// 常见 Node 版本管理器的最后兜底：~/.nvm/versions/node/*/bin/npm。
	matches, _ := filepath.Glob(expandHome("~/.nvm/versions/node/*/bin/npm"))
	for _, candidate := range matches {
		if executableFile(candidate) {
			return candidate
		}
	}
	return ""
}

func findBinary(name string) (string, bool) {
	if path, err := exec.LookPath(name); err == nil {
		return path, true
	}
	for _, dir := range CommonBinDirs {
		candidate := filepath.Join(expandHome(dir), name)
		if executableFile(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func currentPlatformSupported() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux"
}

func trimmedSummary(stdout, stderr string) string {
	combined := strings.TrimSpace(stdout)
	if details := strings.TrimSpace(stderr); details != "" {
		if combined == "" {
			combined = details
		} else {
			combined += "\n" + details
		}
	}
	return truncate(combined, maxOutputBytes)
}

func truncate(value string, limit int) string {
	if len(value) > limit {
		return value[:limit] + "…"
	}
	return value
}
