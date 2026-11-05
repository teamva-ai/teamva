package opencode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
	providerprocess "github.com/teamva-ai/teamva/node/provider/process"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type Client struct {
	executable string
	username   string
	password   string
	http       *http.Client
	// configDir 为该实例私有配置目录：OpenCode 通过 XDG_CONFIG_HOME 读取
	// opencode/opencode.json，skills / instructions / MCP 按实例隔离。
	configDir      string
	ownedConfigDir string
	// dataDir 是该实例私有的数据根（XDG_DATA_HOME）：会话与数据库互不争用。
	dataDir           string
	readOnlySkillDirs []string
	boundSkills       []skillBinding
	writableRoots     []string
	readOnly          bool
	disableTools      bool
	systemPrompt      string
	computerUse       bool
	probeOnly         bool
	authSourcePaths   []string
	writeSandbox      *writeSandbox
	sandboxReady      bool

	startMu sync.Mutex
	closeMu sync.Mutex
	runMu   sync.Mutex
	mu      sync.Mutex
	process *providerprocess.Tree
	baseURL string
	active  *runState
	closed  bool
}

type runState struct {
	request   provider.RunRequest
	session   string
	started   bool
	usage     usageTotals
	compacted map[string]string
}

type usageTotals struct {
	input, output, reasoning, cacheRead, cacheWrite int64
}

func New(executable string) (*Client, error) {
	if executable == "" {
		executable = "opencode"
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Private loopback credentials must never reach a proxy.
	return &Client{
		executable: executable,
		// OpenCode pins the Basic auth username to "opencode"; only the
		// password is configurable through OPENCODE_SERVER_PASSWORD.
		username: "opencode",
		password: base64.RawURLEncoding.EncodeToString(raw),
		http: &http.Client{Timeout: 2 * time.Minute, Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (c *Client) Name() string { return "opencode" }

// WithConfigDir 设置实例私有配置目录（XDG_CONFIG_HOME）。必须在 Start 前调用。
func (c *Client) WithConfigDir(dir string) *Client {
	c.configDir = dir
	return c
}

// WithDataDir 设置实例私有的数据根（XDG_DATA_HOME）。必须在 Start 前调用。
func (c *Client) WithDataDir(dir string) *Client {
	c.dataDir = dir
	return c
}

// WithReadOnlySkillDirs records the instance-private Skill directories that an
// unattended read-only run may inspect outside its project working tree.
func (c *Client) WithReadOnlySkillDirs(dirs ...string) *Client {
	c.readOnlySkillDirs = append([]string(nil), dirs...)
	return c
}

// managedEnv returns the process environment without ambient OpenCode or XDG
// overrides, so managed instances only see the app-controlled configuration.
func managedEnv() []string {
	environ := os.Environ()
	filtered := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "OPENCODE") || name == "XDG_CONFIG_HOME" || name == "XDG_DATA_HOME" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func (c *Client) Probe(ctx context.Context) (provider.CapabilityManifest, error) {
	manifest, _, err := c.Diagnose(ctx)
	return manifest, err
}

func (c *Client) Diagnose(ctx context.Context) (provider.CapabilityManifest, []nodeprotocol.DiagnosticStep, error) {
	manifest := provider.CapabilityManifest{
		Provider: "opencode", Streaming: true, Resume: true, Interrupt: true,
		Approval: true, MaxParallelRuns: 1, CapabilityVersion: 1,
	}
	steps := make([]nodeprotocol.DiagnosticStep, 0, 6)
	started := time.Now()
	resolved, err := exec.LookPath(c.executable)
	if err != nil {
		manifest.UnavailableReasonCode = "executable_not_found"
		steps = append(steps, diagnosticStep("executable", "查找 OpenCode 可执行文件", "failed", "没有找到 OpenCode（应用内置、设置路径或 PATH）", err.Error(), started))
		steps = append(steps, skippedDiagnosticSteps(
			[2]string{"version", "读取 OpenCode 版本"},
			[2]string{"server", "启动 OpenCode 服务"},
			[2]string{"health", "检查 OpenCode 服务健康状态"},
			[2]string{"authentication", "检查 Provider 连接"},
			[2]string{"models", "读取可用模型"},
		)...)
		return manifest, steps, nil
	}
	steps = append(steps, diagnosticStep("executable", "查找 OpenCode 可执行文件", "success", "已找到 OpenCode", resolved, started))

	started = time.Now()
	output, err := providerprocess.CombinedOutput(ctx, exec.Command(resolved, "--version"), 1<<20)
	if err != nil {
		manifest.UnavailableReasonCode = "executable_not_found"
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		steps = append(steps, diagnosticStep("version", "读取 OpenCode 版本", "failed", "OpenCode 可执行文件存在，但无法运行", detail, started))
		steps = append(steps, skippedDiagnosticSteps(
			[2]string{"server", "启动 OpenCode 服务"},
			[2]string{"health", "检查 OpenCode 服务健康状态"},
			[2]string{"authentication", "检查 Provider 连接"},
			[2]string{"models", "读取可用模型"},
		)...)
		return manifest, steps, nil
	}
	manifest.ProviderVersion = strings.TrimSpace(string(output))
	steps = append(steps, diagnosticStep("version", "读取 OpenCode 版本", "success", "OpenCode 命令可以正常执行", manifest.ProviderVersion, started))

	started = time.Now()
	if err := c.ensureStarted(ctx); err != nil {
		manifest.UnavailableReasonCode = "server_unavailable"
		steps = append(steps, diagnosticStep("server", "启动 OpenCode 服务", "failed", "无法启动或连接 OpenCode 服务", err.Error(), started))
		steps = append(steps, skippedDiagnosticSteps(
			[2]string{"health", "检查 OpenCode 服务健康状态"},
			[2]string{"authentication", "检查 Provider 连接"},
			[2]string{"models", "读取可用模型"},
		)...)
		return manifest, steps, nil
	}
	steps = append(steps, diagnosticStep("server", "启动 OpenCode 服务", "success", "OpenCode 服务已启动", "本机 loopback 连接已建立", started))

	started = time.Now()
	var info struct {
		Version string `json:"version"`
	}
	if err := c.getJSON(ctx, "/api/info", &info); err != nil {
		manifest.UnavailableReasonCode = "health_probe_failed"
		steps = append(steps, diagnosticStep("health", "检查 OpenCode 服务健康状态", "failed", "OpenCode 服务没有响应", err.Error(), started))
		steps = append(steps, skippedDiagnosticSteps(
			[2]string{"authentication", "检查 Provider 连接"},
			[2]string{"models", "读取可用模型"},
		)...)
		return manifest, steps, nil
	}
	if info.Version != "" {
		manifest.ProviderVersion = info.Version
	}
	steps = append(steps, diagnosticStep("health", "检查 OpenCode 服务健康状态", "success", "OpenCode 服务健康", "/api/info 响应正常", started))
	manifest.Available = true

	started = time.Now()
	models, providerErr := c.listModels(ctx)
	if providerErr != nil {
		steps = append(steps, diagnosticStep("authentication", "检查 Provider 连接", "warning", "服务可用，但无法读取 Provider 状态", providerErr.Error(), started))
		steps = append(steps, skippedDiagnosticSteps([2]string{"models", "读取可用模型"})...)
		return manifest, steps, nil
	}
	if len(models) == 0 {
		steps = append(steps, diagnosticStep("authentication", "检查 Provider 连接", "warning", "OpenCode 已就绪，但还没有可用的 Provider", "请先在 OpenCode 中登录或配置 Provider", started))
		steps = append(steps, diagnosticStep("models", "读取可用模型", "warning", "没有可用模型", "", started))
		return manifest, steps, nil
	}
	manifest.Models = models
	manifest.Authenticated = true
	steps = append(steps, diagnosticStep("authentication", "检查 Provider 连接", "success", fmt.Sprintf("已连接 Provider，共 %d 个模型", len(models)), "", started))
	steps = append(steps, diagnosticStep("models", "读取可用模型", "success", fmt.Sprintf("读取到 %d 个可用模型", len(models)), "", started))
	return manifest, steps, nil
}

func (c *Client) listModels(ctx context.Context) ([]string, error) {
	return c.listModelsAt(ctx, "")
}

func (c *Client) listModelsAt(ctx context.Context, directory string) ([]string, error) {
	// The v2 model catalog loads asynchronously after server start. An immediate
	// first query can legitimately return zero models even though providers are
	// configured, so retry briefly before reporting "no models".
	deadline := time.Now().Add(modelListWarmup)
	for {
		models, err := c.listModelsOnceAt(ctx, directory)
		if err != nil {
			return nil, err
		}
		if len(models) > 0 || time.Now().After(deadline) {
			return models, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// modelListWarmup bounds how long diagnosis/default selection waits for the local model
// catalog; providers without any model still fail fast after this window.
const modelListWarmup = 8 * time.Second

func (c *Client) listModelsOnce(ctx context.Context) ([]string, error) {
	return c.listModelsOnceAt(ctx, "")
}

func (c *Client) listModelsOnceAt(ctx context.Context, directory string) ([]string, error) {
	var response struct {
		Data []struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerID"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, modelLocationPath("/api/model", directory), &response); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(response.Data))
	seen := make(map[string]bool)
	for _, item := range response.Data {
		// Model.Ref selects the catalog ID, not an aliased upstream modelID.
		id := item.ID
		if item.ProviderID == "" || id == "" {
			continue
		}
		full := item.ProviderID + "/" + id
		if seen[full] {
			continue
		}
		seen[full] = true
		models = append(models, full)
	}
	return models, nil
}

func diagnosticStep(id, label, status, message, detail string, started time.Time) nodeprotocol.DiagnosticStep {
	return nodeprotocol.DiagnosticStep{
		ID: id, Label: label, Status: status, Message: message, Detail: detail,
		DurationMS: time.Since(started).Milliseconds(),
	}
}

func skippedDiagnosticSteps(items ...[2]string) []nodeprotocol.DiagnosticStep {
	steps := make([]nodeprotocol.DiagnosticStep, 0, len(items))
	for _, item := range items {
		steps = append(steps, nodeprotocol.DiagnosticStep{
			ID: item[0], Label: item[1], Status: "skipped", Message: "因前一步失败而跳过",
		})
	}
	return steps
}

func (c *Client) Start(ctx context.Context, request provider.RunRequest, sink provider.Sink) (provider.NativeSession, error) {
	request.NativeSessionID = ""
	return c.run(ctx, request, sink)
}

func (c *Client) Resume(ctx context.Context, request provider.RunRequest, sink provider.Sink) (provider.NativeSession, error) {
	if request.NativeSessionID == "" {
		return provider.NativeSession{}, errors.New("opencode resume requires native_session_id")
	}
	return c.run(ctx, request, sink)
}

func (c *Client) run(ctx context.Context, request provider.RunRequest, sink provider.Sink) (provider.NativeSession, error) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if request.DisableTools && !c.disableTools {
		// A tool-enabled instance may have startup MCP capabilities already
		// configured. Do not claim that a per-turn catalog change un-injects them.
		// Runtime creates/restarts the immutable tool-free config before calling us.
		return provider.NativeSession{}, errors.New("opencode tool-free run requires a tool-free instance configuration")
	}
	// A Run cannot relax the immutable instance authorization snapshot.
	request.ReadOnly = request.ReadOnly || c.readOnly
	request.DisableTools = request.DisableTools || c.disableTools
	if request.DisableTools {
		request.ToolRequestDir = ""
	}
	var err error
	originalCWD := request.CWD
	request.CWD, err = permissionDirectory(request.CWD)
	if err != nil {
		return provider.NativeSession{}, errors.New("opencode working directory is not a safe real directory")
	}
	if request.ToolRequestDir != "" {
		// Preserve canonical platform aliases above CWD (macOS /var), but never
		// resolve an aliased transport/Run directory into a new grant. The host
		// supplied IPC itself must still be the same real path below that CWD.
		ipc := filepath.Clean(request.ToolRequestDir)
		if rel, relErr := filepath.Rel(originalCWD, ipc); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			ipc = filepath.Join(request.CWD, rel)
		}
		request.ToolRequestDir, err = permissionDirectory(ipc)
		if err != nil || request.ToolRequestDir != ipc {
			return provider.NativeSession{}, errors.New("opencode IPC directory is not a safe real directory")
		}
	}
	if err := ctx.Err(); err != nil {
		return provider.NativeSession{}, err
	}
	if err := c.ensureStartedScoped(ctx, &request); err != nil {
		return provider.NativeSession{}, err
	}
	if err := c.verifyPermissionGuard(ctx, request.CWD); err != nil {
		return provider.NativeSession{}, err
	}
	if err := c.verifySkillBindings(ctx, request.CWD); err != nil {
		return provider.NativeSession{}, err
	}
	if c.systemPrompt != "" {
		var agent struct {
			Data struct {
				System string `json:"system"`
			} `json:"data"`
		}
		if err := c.getJSON(ctx, "/api/agent/build?location[directory]="+url.QueryEscape(request.CWD), &agent); err != nil || agent.Data.System != c.systemPrompt {
			return provider.NativeSession{}, errors.New("opencode native system prompt verification failed")
		}
	}
	c.mu.Lock()
	process := c.process
	c.mu.Unlock()
	if process == nil {
		return provider.NativeSession{}, errors.New("opencode server is not running")
	}
	sessionID, usageBaseline, err := c.prepareSession(ctx, request)
	if err != nil {
		return provider.NativeSession{}, err
	}
	state := &runState{request: request, session: sessionID, usage: usageBaseline, compacted: map[string]string{}}
	c.mu.Lock()
	c.active = state
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.active == state {
			c.active = nil
		}
		c.mu.Unlock()
	}()

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan error, 1)
	done := make(chan error, 1)
	go c.streamEvents(streamCtx, state, sink, ready, done)
	select {
	case err := <-ready:
		if err != nil {
			return provider.NativeSession{}, err
		}
	case <-process.Done():
		if err := process.Err(); err != nil {
			return provider.NativeSession{}, fmt.Errorf("opencode server exited before event stream readiness: %w", err)
		}
		return provider.NativeSession{}, errors.New("opencode server exited before event stream readiness")
	case <-ctx.Done():
		return provider.NativeSession{}, ctx.Err()
	}
	if err := c.requestJSON(ctx, http.MethodPost,
		"/api/session/"+url.PathEscape(sessionID)+"/prompt", promptBody(request),
		nil,
	); err != nil {
		return provider.NativeSession{}, errors.Join(err, c.stopProcess())
	}
	session := provider.NativeSession{ID: sessionID, Provider: c.Name()}
	select {
	case err := <-done:
		if err != nil {
			err = errors.Join(err, c.stopProcess())
		}
		return session, err
	case <-process.Done():
		if err := process.Err(); err != nil {
			return session, fmt.Errorf("opencode server exited: %w", err)
		}
		return session, errors.New("opencode server exited unexpectedly")
	case <-ctx.Done():
		interruptContext, cancelInterrupt := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.Interrupt(interruptContext, request.RunID)
		cancelInterrupt()
		return session, errors.Join(ctx.Err(), c.stopProcess())
	}
}

func (c *Client) sessionCreateBody(request provider.RunRequest) map[string]any {
	body := map[string]any{
		"title":    "Teamva " + request.RunID,
		"location": map[string]any{"directory": request.CWD},
		"agent":    "build",
	}
	if providerID, model, ok := strings.Cut(request.Model, "/"); ok && providerID != "" && model != "" {
		body["model"] = map[string]any{"providerID": providerID, "id": model}
	}
	if rules := c.permissionRules(request); len(rules) > 0 {
		body["permissions"] = rules
	}
	return body
}

func promptBody(request provider.RunRequest) map[string]any {
	body := map[string]any{"text": request.Prompt}
	files := make([]map[string]any, 0, len(request.Attachments))
	for _, attachment := range request.Attachments {
		entry := map[string]any{"uri": attachment.DataURL}
		if attachment.Filename != "" {
			entry["name"] = attachment.Filename
		}
		files = append(files, entry)
	}
	if len(files) > 0 {
		body["files"] = files
	}
	return body
}

func (c *Client) streamEvents(
	ctx context.Context,
	state *runState,
	sink provider.Sink,
	ready chan<- error,
	done chan<- error,
) {
	baseURL := c.currentBaseURL()
	if baseURL == "" {
		ready <- errors.New("opencode server is not running")
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/event", nil)
	if err != nil {
		ready <- err
		return
	}
	request.Header.Set("Accept", "text/event-stream")
	request.SetBasicAuth(c.username, c.password)
	streamClient := *c.http
	streamClient.Timeout = 0
	response, err := streamClient.Do(request)
	if err != nil {
		ready <- err
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		ready <- fmt.Errorf("opencode event stream returned %d", response.StatusCode)
		return
	}
	ready <- nil
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	wireType := ""
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			wireType = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			wireType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := bytes.TrimSpace([]byte(strings.TrimPrefix(line, "data:")))
		if wireType == "effect/http-api/stream/failure" {
			var cause any
			message := "native event stream failure"
			if json.Unmarshal(raw, &cause) == nil {
				payload := nodeprotocol.NormalizeDiagnosticPayload(map[string]any{"error": cause})
				if detail, _ := payload["message"].(string); detail != "" {
					message = detail
				}
			}
			done <- errors.New("opencode event stream failed: " + message)
			return
		}
		var event v2Event
		if json.Unmarshal(raw, &event) != nil {
			continue
		}
		sessionID, _ := event.Data["sessionID"].(string)
		if sessionID != state.session {
			continue
		}
		if err := c.handleEvent(ctx, state, sink, event); err != nil {
			done <- err
			return
		}
		if isTerminalEvent(event.Type) {
			done <- nil
			return
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		done <- err
		return
	}
	if ctx.Err() == nil {
		done <- errors.New("opencode event stream reached unexpected EOF")
	}
}

type v2Event struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Created    int64          `json:"created"`
	Location   map[string]any `json:"location"`
	Data       map[string]any `json:"data"`
	Metadata   map[string]any `json:"metadata"`
	ProviderID string         `json:"provider_id"`
}

func isTerminalEvent(eventType string) bool {
	switch eventType {
	case "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
		return true
	}
	return false
}

func (c *Client) handleEvent(ctx context.Context, state *runState, sink provider.Sink, event v2Event) error {
	now := time.Now().UTC()
	emit := func(eventType string, payload map[string]any) error {
		return sink(ctx, provider.Event{
			ID: event.ID, RunID: state.request.RunID, Type: eventType, Payload: payload,
			ProviderEventType: event.Type, ProviderRaw: nil, OccurredAt: now,
		})
	}
	emitProvider := func(payload map[string]any) error {
		return emit("provider.event", payload)
	}
	assistantMessageID, _ := event.Data["assistantMessageID"].(string)
	ordinal := intValue(event.Data["ordinal"])

	switch event.Type {
	case "session.execution.started":
		if !state.started {
			state.started = true
			return emit("run.started", map[string]any{"status": "running", "native_session_id": state.session})
		}
		return nil
	case "session.text.delta":
		delta, _ := event.Data["delta"].(string)
		if delta == "" {
			return nil
		}
		return emit("output.delta", map[string]any{
			"text":       delta,
			"message_id": assistantMessageID,
			"part_id":    textPartID(assistantMessageID, ordinal),
		})
	case "session.text.ended":
		text, _ := event.Data["text"].(string)
		return emitProvider(map[string]any{
			"kind": "text", "part_id": textPartID(assistantMessageID, ordinal),
			"message_id": assistantMessageID, "text": text,
		})
	case "session.reasoning.delta":
		delta, _ := event.Data["delta"].(string)
		if delta == "" {
			return nil
		}
		return emitProvider(map[string]any{
			"kind": "reasoning", "part_id": reasoningPartID(assistantMessageID, ordinal),
			"message_id": assistantMessageID, "delta": delta,
		})
	case "session.reasoning.ended":
		text, _ := event.Data["text"].(string)
		return emitProvider(map[string]any{
			"kind": "reasoning", "part_id": reasoningPartID(assistantMessageID, ordinal),
			"message_id": assistantMessageID, "text": text,
		})
	case "session.tool.input.started", "session.tool.input.delta", "session.tool.input.ended",
		"session.tool.called", "session.tool.progress", "session.tool.success", "session.tool.failed":
		if event.Type == "session.tool.success" && (state.request.DisableTools || c.disableTools) {
			// Never project a broken/unsupported native capability boundary as a
			// successful audit. The stream caller stops the native process on error.
			return errors.New("opencode tool-free contract violated: native tool executed")
		}
		return emitProvider(toolPayload(event))
	case "session.usage.updated":
		return c.emitUsage(state, event, emit)
	case "session.retry.scheduled":
		payload := nodeprotocol.NormalizeDiagnosticPayload(event.Data)
		payload["kind"], payload["willRetry"] = "error", true
		return emitProvider(payload)
	case "session.compaction.started", "session.compaction.delta", "session.compaction.ended", "session.compaction.failed":
		status := strings.TrimPrefix(event.Type, "session.compaction.")
		if status == "delta" {
			return nil
		}
		if status == "ended" {
			status = "completed"
		}
		if status == "started" {
			status = "running"
		}
		// V2 compaction events have no assistantMessageID. Correlate the
		// serialized session lifecycle using its start event, not each event ID.
		if state.compacted == nil {
			state.compacted = map[string]string{}
		}
		id := state.compacted["active"]
		if status == "running" || id == "" {
			id = event.ID
		}
		if id == "" {
			id = "compaction:" + state.request.RunID
		}
		if status == "running" {
			state.compacted["active"] = id
		} else {
			delete(state.compacted, "active")
		}
		payload := map[string]any{"id": id, "provider": "opencode", "status": status}
		if nativeError, ok := event.Data["error"]; ok {
			payload["error"] = nativeError
		}
		// Never persist the compaction summary/native context as UI diagnostics.
		payload = nodeprotocol.NormalizeDiagnosticPayload(payload)
		return emit("context.compaction", payload)
	case "permission.asked":
		requestID, _ := event.Data["id"].(string)
		if requestID == "" {
			return nil
		}
		// Teamva runs unattended and never shows an approval card. A normal Run
		// answers the native ask in the affirmative; the host permission rules
		// still deny anything outside the Run's granted roots. Read-only and
		// tool-disabled Runs may not escalate, so they reject the ask.
		decision := "always"
		if state.request.ReadOnly || state.request.DisableTools || c.disableTools {
			decision = "reject"
		}
		go c.replyPermission(context.Background(), state.session, requestID, decision)
		return nil
	case "session.execution.succeeded":
		return emit("run.completed", map[string]any{"status": "completed"})
	case "session.execution.failed":
		return emit("run.error", nodeprotocol.NormalizeDiagnosticPayload(map[string]any{"status": "failed", "error": event.Data}))
	case "session.execution.interrupted":
		return emit("run.completed", map[string]any{"status": "interrupted"})
	}
	return nil
}

func toolPayload(event v2Event) map[string]any {
	payload := map[string]any{
		"kind":    "tool",
		"phase":   strings.TrimPrefix(event.Type, "session.tool."),
		"call_id": stringValue(event.Data["id"]),
	}
	for _, key := range []string{"assistantMessageID", "name", "text", "input", "content", "metadata", "error", "executed"} {
		if value, ok := event.Data[key]; ok {
			payload[key] = value
		}
	}
	return payload
}

func (c *Client) emitUsage(state *runState, event v2Event, emit func(string, map[string]any) error) error {
	raw, err := json.Marshal(event.Data["tokens"])
	if err != nil {
		return errors.New("opencode native session usage update invalid")
	}
	next, err := decodeUsageTotals(raw)
	if err != nil {
		return errors.New("opencode native session usage update invalid")
	}
	if next.input < state.usage.input || next.output < state.usage.output || next.reasoning < state.usage.reasoning ||
		next.cacheRead < state.usage.cacheRead || next.cacheWrite < state.usage.cacheWrite {
		// A reset/out-of-order cumulative snapshot has no reliable billing delta.
		// Fail visibly instead of clamping and later counting old usage again.
		return errors.New("opencode native session usage counter regressed")
	}
	delta := usageTotals{
		input:      next.input - state.usage.input,
		output:     next.output - state.usage.output,
		reasoning:  next.reasoning - state.usage.reasoning,
		cacheRead:  next.cacheRead - state.usage.cacheRead,
		cacheWrite: next.cacheWrite - state.usage.cacheWrite,
	}
	state.usage = next
	total := delta.input + delta.output + delta.reasoning + delta.cacheRead + delta.cacheWrite
	if total <= 0 {
		return nil
	}
	messageID, _ := event.Data["assistantMessageID"].(string)
	return emit("usage.updated", map[string]any{
		"accounting_mode":    "delta",
		"message_id":         messageID,
		"input_tokens":       delta.input,
		"output_tokens":      delta.output,
		"reasoning_tokens":   delta.reasoning,
		"cache_read_tokens":  delta.cacheRead,
		"cache_write_tokens": delta.cacheWrite,
		"total_tokens":       total,
	})
}

func textPartID(messageID string, ordinal int) string {
	return "text:" + messageID + ":" + strconv.Itoa(ordinal)
}

func reasoningPartID(messageID string, ordinal int) string {
	return "reasoning:" + messageID + ":" + strconv.Itoa(ordinal)
}

// Every component is required by the pinned native TokenUsage.Info schema.
// Pointers distinguish a real zero from absent/null counters. Validate the
// sum too, so malformed huge counts cannot wrap a budget to zero/negative.
func decodeUsageTotals(raw json.RawMessage) (usageTotals, error) {
	var tokens struct {
		Input     *int64 `json:"input"`
		Output    *int64 `json:"output"`
		Reasoning *int64 `json:"reasoning"`
		Cache     *struct {
			Read  *int64 `json:"read"`
			Write *int64 `json:"write"`
		} `json:"cache"`
	}
	invalid := errors.New("opencode native session usage counters invalid")
	if json.Unmarshal(raw, &tokens) != nil || tokens.Input == nil || tokens.Output == nil || tokens.Reasoning == nil ||
		tokens.Cache == nil || tokens.Cache.Read == nil || tokens.Cache.Write == nil {
		return usageTotals{}, invalid
	}
	var total int64
	for _, value := range []*int64{tokens.Input, tokens.Output, tokens.Reasoning, tokens.Cache.Read, tokens.Cache.Write} {
		if *value < 0 || *value > math.MaxInt64-total {
			return usageTotals{}, invalid
		}
		total += *value
	}
	return usageTotals{input: *tokens.Input, output: *tokens.Output, reasoning: *tokens.Reasoning,
		cacheRead: *tokens.Cache.Read, cacheWrite: *tokens.Cache.Write}, nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func intValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	}
	return 0
}

func (c *Client) replyPermission(ctx context.Context, sessionID, requestID, decision string) {
	rejectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = c.requestJSON(rejectCtx, http.MethodPost,
		"/api/session/"+url.PathEscape(sessionID)+"/permission/"+url.PathEscape(requestID)+"/reply",
		map[string]any{"decision": decision, "message": "Decision from Teamva control plane"}, nil,
	)
}

func (c *Client) DecideApproval(context.Context, provider.ApprovalDecision) error {
	// Native asks are answered inline; OpenCode never leaves one pending.
	return errors.New("opencode approvals are disabled")
}

func (c *Client) Interrupt(ctx context.Context, runID string) error {
	c.startMu.Lock()
	c.mu.Lock()
	state := c.active
	process := c.process
	c.mu.Unlock()
	c.startMu.Unlock()
	if state == nil || state.request.RunID != runID || process == nil {
		return &runNotActiveError{provider: c.Name(), runID: runID}
	}
	err := c.interruptSession(ctx, state.session)
	if err != nil {
		c.mu.Lock()
		noLongerActive := c.active != state || c.process != process
		c.mu.Unlock()
		if noLongerActive {
			return &runNotActiveError{provider: c.Name(), runID: runID}
		}
	}
	return err
}

func (c *Client) interruptSession(ctx context.Context, sessionID string) error {
	return c.requestJSON(ctx, http.MethodPost,
		"/api/session/"+url.PathEscape(sessionID)+"/interrupt", nil, nil,
	)
}

func (c *Client) Steer(ctx context.Context, runID, prompt string, attachments []nodeprotocol.Attachment) error {
	c.mu.Lock()
	state := c.active
	c.mu.Unlock()
	if state == nil || state.request.RunID != runID {
		return fmt.Errorf("opencode run %s is not active", runID)
	}
	body := promptBody(provider.RunRequest{Prompt: prompt, Attachments: attachments})
	body["delivery"] = "steer"
	return c.steerSession(ctx, state.session, body)
}

func (c *Client) steerSession(ctx context.Context, sessionID string, body map[string]any) error {
	return c.requestJSON(ctx, http.MethodPost,
		"/api/session/"+url.PathEscape(sessionID)+"/prompt", body, nil,
	)
}

func (c *Client) VerifySession(ctx context.Context, sessionID string) (bool, error) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if c.probeOnly || c.configDir == "" {
		return false, errors.New("opencode session verification requires a managed instance")
	}
	cwd, err := permissionDirectory(c.configDir)
	if err != nil {
		return false, errors.New("opencode verification requires a private real configuration directory")
	}
	// Verification has no Run authorization context, even on a warm idle
	// instance. Revoke the previous business/IPC scope rather than carrying it
	// into a metadata-only startup; execution will restore its exact new scope.
	if err := c.ensureStartedScoped(ctx, &provider.RunRequest{CWD: cwd, ReadOnly: true}); err != nil {
		return false, err
	}
	return c.verifySession(ctx, sessionID)
}

func (c *Client) verifySession(ctx context.Context, sessionID string) (bool, error) {
	var session struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	err := c.getJSON(ctx, "/api/session/"+url.PathEscape(sessionID), &session)
	if err != nil {
		var httpError *HTTPError
		if errors.As(err, &httpError) && httpError.Status == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	return session.Data.ID == sessionID, nil
}

func (c *Client) ensureStarted(ctx context.Context) error {
	return c.ensureStartedScoped(ctx, nil)
}

// Every managed cold start, including VerifySession/diagnostics before a Run,
// gets an OS boundary BEFORE the first plugin/MCP process can execute. Discovery
// probes remain separate; no business roots or Run IPC are invented for them.
func (c *Client) ensureStartedScoped(ctx context.Context, scope *provider.RunRequest) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return errors.New("opencode adapter is closed")
	}
	if scope != nil {
		if c.probeOnly || c.configDir == "" {
			return errors.New("opencode execution requires a managed instance")
		}
		if err := c.prepareSandboxLocked(ctx, *scope); err != nil {
			return err
		}
	} else if c.configDir != "" && !c.probeOnly {
		c.mu.Lock()
		bounded := c.process != nil && c.sandboxReady && c.writeSandbox != nil
		if bounded {
			select {
			case <-c.process.Done():
				bounded = false
			default:
			}
		}
		c.mu.Unlock()
		if !bounded {
			cwd, err := permissionDirectory(c.configDir)
			if err != nil {
				return errors.New("opencode verification requires a private real configuration directory")
			}
			// Verification only reads native session metadata. It has no business
			// write roots and no Run IPC; later execution replaces this profile.
			if err := c.prepareSandboxLocked(ctx, provider.RunRequest{CWD: cwd, ReadOnly: true}); err != nil {
				return err
			}
		}
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("opencode adapter is closed")
	}
	if c.process != nil {
		process := c.process
		select {
		case <-process.Done():
			c.process = nil
			c.baseURL = ""
		default:
			c.mu.Unlock()
			return nil
		}
	}
	c.mu.Unlock()
	if c.configDir == "" {
		// V2 itself may reject a malformed global document with only a log
		// warning. Surface a sanitized failure in the adapter's diagnostics.
		if _, err := userConfig(); err != nil {
			return err
		}
	}
	startupCtx, cancel := context.WithTimeout(ctx, serverStartTimeout)
	defer cancel()
	// V2 binds port zero atomically and reports the actual bound address on
	// stdout after boot. Never reserve/close a port or probe a guessed address:
	// that could send Basic auth to an unrelated process that won the bind.
	cmd := exec.Command(c.executable, "serve", "--hostname", "127.0.0.1", "--port", "0")
	c.mu.Lock()
	sandbox := c.writeSandbox
	c.mu.Unlock()
	if sandbox != nil {
		var err error
		if sandbox.runTemp != nil {
			if err := sandbox.runTemp.validate(); err != nil {
				return err
			}
		}
		cmd, err = sandbox.command(c.executable, "serve", "--hostname", "127.0.0.1", "--port", "0")
		if err != nil {
			return err
		}
		cmd.Dir = sandbox.cwd
	}
	// OpenCode 2.0.x reads OPENCODE_PASSWORD first and falls back to
	// OPENCODE_SERVER_PASSWORD; newer 2.x builds only honor the latter. Set both
	// so bundled and externally supplied v2 binaries share one auth path.
	baseEnv := []string{
		"OPENCODE_PASSWORD=" + c.password,
		"OPENCODE_SERVER_PASSWORD=" + c.password,
	}
	if c.configDir == "" {
		cmd.Env = append(cmd.Environ(), baseEnv...)
	} else {
		// 托管实例使用受控环境：剥离会话进程带入的 OPENCODE_*/XDG_* 变量，
		// 避免用户 shell 的插件或配置混进 Agent 实例。
		cmd.Env = append(managedEnv(), baseEnv...)
		cmd.Env = append(cmd.Env,
			"XDG_CONFIG_HOME="+c.configDir,
			// The host already resolves project/global MCP configuration. Do not
			// rediscover arbitrary opencode.json(c) in the workspace's ancestors.
			// V2 recognizes the canonical flag and its compatibility alias.
			"OPENCODE_CONFIG_PROJECT_DISABLE=1",
			"OPENCODE_DISABLE_PROJECT_CONFIG=1",
			"OPENCODE_DISABLE_EXTERNAL_SKILLS=1",
			"OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1",
		)
		if c.dataDir != "" {
			cmd.Env = append(cmd.Env, "XDG_DATA_HOME="+c.dataDir)
		}
		// Reuse only the user's public models.dev cache, not their native DB.
		// The path is app-controlled metadata; credentials stay in private config.
		catalogPath := filepath.Join(c.configDir, "opencode", nativeCatalogFilename)
		if info, err := os.Lstat(catalogPath); err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("opencode private model metadata is not a regular file")
			}
			cmd.Env = append(cmd.Env, "OPENCODE_MODELS_PATH="+catalogPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("opencode private model metadata unavailable")
		}
	}
	if sandbox != nil {
		cmd.Env = sandbox.environment(cmd.Env)
	}
	discard, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	stdout, writer, err := os.Pipe()
	if err != nil {
		_ = discard.Close()
		return err
	}
	cmd.Stdout = writer
	cmd.Stderr = discard
	if err := startupCtx.Err(); err != nil {
		_ = stdout.Close()
		_ = writer.Close()
		_ = discard.Close()
		return err
	}
	process, err := providerprocess.Start(cmd)
	_ = writer.Close()
	_ = discard.Close()
	if err != nil {
		_ = stdout.Close()
		return fmt.Errorf("start opencode server: %w", err)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = stdout.Close()
		return errors.Join(errors.New("opencode adapter is closed"), process.Close())
	}
	c.process = process
	c.sandboxReady = sandbox != nil
	c.mu.Unlock()
	go c.monitorProcess(process)
	address := make(chan serverAddressResult, 1)
	go func() {
		defer stdout.Close()
		baseURL, err := readServerAddress(stdout)
		address <- serverAddressResult{baseURL: baseURL, err: err}
		// Do not retain or log arbitrary process output (it can contain keys).
		// Keep draining after readiness so later writes cannot block the server.
		_, _ = io.Copy(io.Discard, stdout)
	}()
	startupFailed := func(err error) error {
		_ = stdout.Close()
		return errors.Join(err, c.stopProcess())
	}
	select {
	case result := <-address:
		if result.err != nil {
			return startupFailed(result.err)
		}
		c.mu.Lock()
		if c.closed || c.process != process {
			c.mu.Unlock()
			return startupFailed(errors.New("opencode server stopped before readiness"))
		}
		c.baseURL = result.baseURL
		c.mu.Unlock()
	case <-process.Done():
		return startupFailed(errors.New("opencode server exited before reporting its listening address"))
	case <-startupCtx.Done():
		return startupFailed(fmt.Errorf("wait for opencode server listening address: %w", startupCtx.Err()))
	}
	deadline := time.NewTicker(100 * time.Millisecond)
	defer deadline.Stop()
	// A wrong password or a binary that cannot serve /api/info would otherwise
	// retry until the caller's context expires, hanging diagnostics for as long
	// as the HTTP request allows. Fail with a bounded, actionable error.
	for {
		select {
		case <-process.Done():
			return startupFailed(errors.New("opencode server exited during startup"))
		default:
		}
		var info struct {
			Version string `json:"version"`
		}
		if err := c.getJSON(startupCtx, "/api/info", &info); err == nil && info.Version != "" {
			select {
			case <-process.Done():
				return startupFailed(errors.New("opencode server exited during startup"))
			default:
				return nil
			}
		}
		select {
		case <-startupCtx.Done():
			return startupFailed(fmt.Errorf("wait for opencode server readiness: %w", startupCtx.Err()))
		case <-process.Done():
			return startupFailed(errors.New("opencode server exited during startup"))
		case <-deadline.C:
		}
	}
}

// serverStartTimeout bounds one opencode serve cold start. Server startup is
// normally sub-second; the allowance covers slow plugin/model initialization.
const serverStartTimeout = 60 * time.Second

type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("opencode returned HTTP %d: %s", e.Status, e.Body)
}

func (c *Client) getJSON(ctx context.Context, path string, output any) error {
	return c.requestJSON(ctx, http.MethodGet, path, nil, output)
}

func (c *Client) requestJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	baseURL := c.currentBaseURL()
	if baseURL == "" {
		return errors.New("opencode server is not running")
	}
	request, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
	if err != nil {
		return err
	}
	request.SetBasicAuth(c.username, c.password)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPError{Status: response.StatusCode, Body: string(payload)}
	}
	if output != nil && len(payload) > 0 {
		return json.Unmarshal(payload, output)
	}
	return nil
}

func (c *Client) currentBaseURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.process != nil {
		select {
		case <-c.process.Done():
			return ""
		default:
		}
	}
	return c.baseURL
}

func (c *Client) stopProcess() error {
	c.mu.Lock()
	process := c.process
	c.process = nil
	c.baseURL = ""
	c.sandboxReady = false
	c.mu.Unlock()
	if process != nil {
		return process.Close()
	}
	return nil
}

func (c *Client) monitorProcess(process *providerprocess.Tree) {
	<-process.Done()
	c.mu.Lock()
	if c.process == process {
		c.process = nil
		c.baseURL = ""
		c.sandboxReady = false
	}
	c.mu.Unlock()
}

type runNotActiveError struct {
	provider string
	runID    string
}

func (e *runNotActiveError) Error() string {
	return fmt.Sprintf("%s run %s is not active", e.provider, e.runID)
}

func (e *runNotActiveError) RunNotActive() bool { return true }

func (c *Client) Close() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	firstErr := c.stopProcess()
	c.startMu.Lock()
	secondErr := c.stopProcess()
	c.startMu.Unlock()
	result := errors.Join(firstErr, secondErr)
	if result == nil && c.ownedConfigDir != "" {
		result = os.RemoveAll(c.ownedConfigDir)
	}
	return result
}

var _ provider.Adapter = (*Client)(nil)
