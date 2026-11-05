package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/process"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type Client struct {
	executable string
	// configDir 为该实例私有配置目录：codex 通过 CODEX_HOME 指向它，
	// 实现 skills / instructions / MCP 按实例隔离。
	configDir string
	// Frozen at factory creation; CWD is host metadata, never an implicit grant.
	writableRepositories []string
	protectedPaths       []string
	readOnly             bool
	disableTools         bool
	systemPrompt         string

	startMu      sync.Mutex
	closeMu      sync.Mutex
	runMu        sync.Mutex
	writeMu      sync.Mutex
	mu           sync.Mutex
	process      *providerprocess.Tree
	processEpoch uint64
	stdin        io.WriteCloser
	pending      map[int64]pendingRPC
	approvals    map[string]pendingApproval
	active       *runState
	nextID       atomic.Int64
	closed       bool
}

// WithConfigDir 设置独立配置目录（CODEX_HOME）。必须在 Start 前调用。
func (c *Client) WithConfigDir(dir string) *Client {
	c.configDir = dir
	return c
}

type runState struct {
	ctx          context.Context
	request      adapter.RunRequest
	threadID     string
	turnID       string
	sink         adapter.Sink
	done         chan error
	once         sync.Once
	processEpoch uint64
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcResult struct {
	message rpcMessage
	err     error
}

type pendingRPC struct {
	epoch    uint64
	response chan rpcResult
}

type pendingApproval struct {
	message rpcMessage
	runID   string
	epoch   uint64
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	payload := map[string]any{"message": e.Message}
	if len(e.Data) > 0 {
		payload["data"] = e.Data
	}
	clean := nodeprotocol.NormalizeDiagnosticPayload(payload)
	return fmt.Sprintf("codex rpc %d: %s", e.Code, clean["message"])
}

func New(executable string) *Client {
	if executable == "" {
		executable = "codex"
	}
	return &Client{
		executable: executable,
		pending:    make(map[int64]pendingRPC),
		approvals:  make(map[string]pendingApproval),
	}
}

func (c *Client) Name() string { return "codex" }

func (c *Client) Probe(ctx context.Context) (adapter.CapabilityManifest, error) {
	manifest, _, err := c.Diagnose(ctx)
	return manifest, err
}

func (c *Client) Diagnose(ctx context.Context) (adapter.CapabilityManifest, []nodeprotocol.DiagnosticStep, error) {
	manifest := adapter.CapabilityManifest{
		Provider: "codex", Streaming: true, Resume: true, Interrupt: true,
		Approval: true, MaxParallelRuns: 1, CapabilityVersion: 1,
	}
	steps := make([]nodeprotocol.DiagnosticStep, 0, 5)
	started := time.Now()
	resolved, err := exec.LookPath(c.executable)
	if err != nil {
		manifest.UnavailableReasonCode = "executable_not_found"
		steps = append(steps, diagnosticStep("executable", "查找 Codex 可执行文件", "failed", "没有在当前进程的 PATH 中找到 Codex", err.Error(), started))
		steps = append(steps, skippedDiagnosticSteps(
			[2]string{"version", "读取 Codex 版本"},
			[2]string{"server", "启动 Codex app-server"},
			[2]string{"authentication", "检查 Codex 登录状态"},
			[2]string{"models", "读取 Codex 模型能力"},
		)...)
		return manifest, steps, nil
	}
	steps = append(steps, diagnosticStep("executable", "查找 Codex 可执行文件", "success", "已找到 Codex", resolved, started))

	started = time.Now()
	output, err := providerprocess.CombinedOutput(ctx, exec.Command(resolved, "--version"), 1<<20)
	if err != nil {
		manifest.UnavailableReasonCode = "executable_not_found"
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		steps = append(steps, diagnosticStep("version", "读取 Codex 版本", "failed", "Codex 可执行文件存在，但无法运行", detail, started))
		steps = append(steps, skippedDiagnosticSteps(
			[2]string{"server", "启动 Codex app-server"},
			[2]string{"authentication", "检查 Codex 登录状态"},
			[2]string{"models", "读取 Codex 模型能力"},
		)...)
		return manifest, steps, nil
	}
	manifest.ProviderVersion = strings.TrimSpace(strings.TrimPrefix(string(output), "codex-cli "))
	steps = append(steps, diagnosticStep("version", "读取 Codex 版本", "success", "Codex 命令可以正常执行", manifest.ProviderVersion, started))

	started = time.Now()
	if err := c.ensureStarted(ctx); err != nil {
		manifest.UnavailableReasonCode = "app_server_unavailable"
		steps = append(steps, diagnosticStep("server", "启动 Codex app-server", "failed", "无法启动或连接 Codex app-server", err.Error(), started))
		steps = append(steps, skippedDiagnosticSteps(
			[2]string{"authentication", "检查 Codex 登录状态"},
			[2]string{"models", "读取 Codex 模型能力"},
		)...)
		return manifest, steps, nil
	}
	steps = append(steps, diagnosticStep("server", "启动 Codex app-server", "success", "Codex app-server 已响应", "stdio JSONL 连接正常", started))

	var account struct {
		Account            json.RawMessage `json:"account"`
		RequiresOpenAIAuth bool            `json:"requiresOpenaiAuth"`
	}
	started = time.Now()
	if err := c.call(ctx, "account/read", map[string]any{"refreshToken": false}, &account); err != nil {
		manifest.UnavailableReasonCode = "auth_probe_failed"
		steps = append(steps, diagnosticStep("authentication", "检查 Codex 登录状态", "failed", "app-server 已启动，但无法读取账号状态", err.Error(), started))
		steps = append(steps, skippedDiagnosticSteps([2]string{"models", "读取 Codex 模型能力"})...)
		return manifest, steps, nil
	}
	manifest.Available = true
	manifest.Authenticated = len(account.Account) > 0 && string(account.Account) != "null" ||
		!account.RequiresOpenAIAuth
	if manifest.Authenticated {
		steps = append(steps, diagnosticStep("authentication", "检查 Codex 登录状态", "success", "Codex 已连接账号", "account/read 成功", started))
	} else {
		steps = append(steps, diagnosticStep("authentication", "检查 Codex 登录状态", "warning", "Codex 已安装，但尚未登录", "请先在终端运行 codex login", started))
	}

	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	started = time.Now()
	if err := c.call(ctx, "model/list", map[string]any{"limit": 100, "includeHidden": false}, &models); err == nil {
		for _, model := range models.Data {
			manifest.Models = append(manifest.Models, model.ID)
		}
		steps = append(steps, diagnosticStep("models", "读取 Codex 模型能力", "success", fmt.Sprintf("读取到 %d 个可用模型", len(manifest.Models)), "model/list 成功", started))
	} else {
		steps = append(steps, diagnosticStep("models", "读取 Codex 模型能力", "warning", "Codex 可用，但暂时无法读取模型列表", err.Error(), started))
	}
	return manifest, steps, nil
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

func (c *Client) Start(ctx context.Context, request adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
	request.NativeSessionID = ""
	return c.run(ctx, request, sink)
}

func (c *Client) Resume(ctx context.Context, request adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
	if request.NativeSessionID == "" {
		return adapter.NativeSession{}, errors.New("codex resume requires native_session_id")
	}
	return c.run(ctx, request, sink)
}

func (c *Client) run(ctx context.Context, request adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if err := ctx.Err(); err != nil {
		return adapter.NativeSession{}, err
	}
	if c.disableTools || request.DisableTools {
		return adapter.NativeSession{}, errCodexNoToolsUnsupported
	}
	request.ReadOnly = request.ReadOnly || c.readOnly
	if request.NativeSessionID != "" {
		// A loaded native thread ignores thread/resume config overrides, and
		// turn/start has no shell_environment_policy field. Reopen its durable
		// session in a fresh process so TMPDIR/TMP/TEMP cannot retain another
		// Run's IPC path (or a prior bridge when this Run has no bridge).
		if err := c.stopProcess(); err != nil {
			return adapter.NativeSession{}, err
		}
	}
	carveouts, err := codexPrivateCarveouts(c.protectedPaths, codexRunWritableRoots(request, c.writableRepositories))
	if err != nil {
		return adapter.NativeSession{}, err
	}
	gitMetadata, err := codexLinkedMetadataWrites(request, c.writableRepositories, carveouts)
	if err != nil {
		return adapter.NativeSession{}, err
	}
	if request.ToolRequestDir != "" {
		ipc, err := filepath.EvalSymlinks(request.ToolRequestDir)
		if err != nil {
			return adapter.NativeSession{}, errors.New("resolve codex Run IPC failed")
		}
		for _, path := range carveouts {
			rel, err := filepath.Rel(path, filepath.Join(ipc, codexRunTempName))
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return adapter.NativeSession{}, errors.New("codex Run temp overlaps a private authentication path")
			}
		}
	}
	if err := prepareCodexRunTemp(request); err != nil {
		return adapter.NativeSession{}, err
	}
	if err := c.ensureStarted(ctx); err != nil {
		return adapter.NativeSession{}, err
	}
	var threadResponse struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		ActivePermissionProfile *struct {
			ID string `json:"id"`
		} `json:"activePermissionProfile"`
		RuntimeWorkspaceRoots *[]string `json:"runtimeWorkspaceRoots"`
	}
	method := "thread/start"
	params := threadStartParams(request, c.writableRepositories...)
	params["config"] = codexRunConfig(request, carveouts...)
	filesystem := params["config"].(map[string]any)["permissions"].(map[string]any)[codexPermissionProfile].(map[string]any)["filesystem"].(map[string]any)
	for _, path := range gitMetadata {
		filesystem[filepath.ToSlash(path)] = "write"
	}
	if c.systemPrompt != "" {
		params["baseInstructions"] = c.systemPrompt
	}
	if request.NativeSessionID != "" {
		method = "thread/resume"
		params["threadId"] = request.NativeSessionID
	}
	if err := c.call(ctx, method, params, &threadResponse); err != nil {
		return adapter.NativeSession{}, err
	}
	if threadResponse.ActivePermissionProfile == nil || threadResponse.ActivePermissionProfile.ID != codexPermissionProfile ||
		threadResponse.RuntimeWorkspaceRoots == nil || (method == "thread/start" && !slices.Equal(*threadResponse.RuntimeWorkspaceRoots, codexRunWritableRoots(request, c.writableRepositories))) {
		return adapter.NativeSession{}, errors.Join(errors.New("codex did not confirm the exact filesystem permission profile"), c.stopProcess())
	}
	state := &runState{
		ctx: ctx, request: request, threadID: threadResponse.Thread.ID,
		sink: sink, done: make(chan error, 1),
	}
	c.mu.Lock()
	state.processEpoch = c.processEpoch
	c.active = state
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.active == state {
			c.active = nil
		}
		for id, approval := range c.approvals {
			if approval.runID == state.request.RunID && approval.epoch == state.processEpoch {
				delete(c.approvals, id)
			}
		}
		c.mu.Unlock()
	}()
	var turnResponse struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	turnParams := turnStartParams(request, state.threadID, c.writableRepositories...)
	if err := c.call(ctx, "turn/start", turnParams, &turnResponse); err != nil {
		// The request may have reached Codex even when its response lost the
		// cancellation race. Reusing this process could let that turn publish into
		// a later active state.
		return adapter.NativeSession{}, errors.Join(err, c.stopProcess())
	}
	c.mu.Lock()
	state.turnID = turnResponse.Turn.ID
	c.mu.Unlock()
	session := adapter.NativeSession{ID: state.threadID, TurnID: state.turnID, Provider: c.Name()}
	select {
	case err := <-state.done:
		if err != nil {
			// A failed sink means the durable consumer did not accept an event.
			// Codex may continue the turn and emit more notifications, so the
			// complete Provider tree must be gone before this run releases runMu
			// and a later run can install a new active state. Conservatively use
			// the same boundary for every non-successful turn result.
			err = errors.Join(err, c.stopProcess())
		}
		return session, err
	case <-ctx.Done():
		interruptContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.Interrupt(interruptContext, request.RunID)
		cancel()
		return session, errors.Join(ctx.Err(), c.stopProcess())
	}
}

func threadStartParams(request adapter.RunRequest, writableRepositories ...string) map[string]any {
	params := map[string]any{
		"cwd": request.CWD, "serviceName": "teamva",
		"permissions":           codexPermissionProfile,
		"runtimeWorkspaceRoots": codexRunWritableRoots(request, writableRepositories),
		"approvalPolicy":        codexRunApprovalPolicy(),
		// Keep standalone New clients safe too. Write grants still come only
		// from replaced runtimeWorkspaceRoots. Run config carries the confined
		// shell temp environment; run() adds private-path read carveouts.
		"config": codexRunConfig(request),
	}
	if request.Model != "" {
		params["model"] = request.Model
	}
	return params
}

func turnStartParams(request adapter.RunRequest, threadID string, writableRepositories ...string) map[string]any {
	params := map[string]any{
		"threadId":              threadID,
		"input":                 codexUserInput(request.Prompt, request.Attachments),
		"cwd":                   request.CWD,
		"permissions":           codexPermissionProfile,
		"runtimeWorkspaceRoots": codexRunWritableRoots(request, writableRepositories),
		"approvalPolicy":        codexRunApprovalPolicy(),
	}
	if request.Model != "" {
		params["model"] = request.Model
	}
	return params
}

func codexUserInput(prompt string, attachments []nodeprotocol.Attachment) []map[string]any {
	input := make([]map[string]any, 0, 1+len(attachments))
	if strings.TrimSpace(prompt) != "" {
		input = append(input, map[string]any{"type": "text", "text": prompt})
	}
	for _, attachment := range attachments {
		input = append(input, map[string]any{"type": "image", "url": attachment.DataURL})
	}
	return input
}

func (c *Client) Interrupt(ctx context.Context, runID string) error {
	// Synchronize with ensureStarted's process-launch gate. If cancellation wins
	// first, a queued Start observes ctx before spawning; if launch wins first,
	// run.stop cannot return until that launch phase has completed.
	c.startMu.Lock()
	c.mu.Lock()
	state := c.active
	turnID := ""
	if state != nil {
		turnID = state.turnID
	}
	process := c.process
	epoch := c.processEpoch
	c.mu.Unlock()
	c.startMu.Unlock()
	if state == nil || state.request.RunID != runID || turnID == "" || process == nil {
		return &runNotActiveError{provider: c.Name(), runID: runID}
	}
	var ignored map[string]any
	err := c.call(ctx, "turn/interrupt", map[string]any{
		"threadId": state.threadID, "turnId": turnID,
	}, &ignored)
	if err != nil {
		c.mu.Lock()
		noLongerActive := c.active != state || c.process != process || c.processEpoch != epoch
		c.mu.Unlock()
		if noLongerActive {
			return &runNotActiveError{provider: c.Name(), runID: runID}
		}
	}
	return err
}

func (c *Client) Steer(ctx context.Context, runID, prompt string, attachments []nodeprotocol.Attachment) error {
	c.mu.Lock()
	state := c.active
	turnID := ""
	if state != nil {
		turnID = state.turnID
	}
	c.mu.Unlock()
	if state == nil || state.request.RunID != runID || turnID == "" {
		return fmt.Errorf("codex run %s is not active", runID)
	}
	var ignored map[string]any
	return c.call(ctx, "turn/steer", map[string]any{
		"threadId":       state.threadID,
		"expectedTurnId": turnID,
		"input":          codexUserInput(prompt, attachments),
	}, &ignored)
}

func (c *Client) VerifySession(ctx context.Context, sessionID string) (bool, error) {
	if err := c.ensureStarted(ctx); err != nil {
		return false, err
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	err := c.call(ctx, "thread/read", map[string]any{"threadId": sessionID, "includeTurns": false}, &response)
	if err != nil {
		var rpcErr *rpcError
		if errors.As(err, &rpcErr) {
			return false, nil
		}
		return false, err
	}
	return response.Thread.ID == sessionID, nil
}

func (c *Client) ensureStarted(ctx context.Context) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("codex adapter is closed")
	}
	if c.process != nil {
		process := c.process
		select {
		case <-process.Done():
			if c.stdin != nil {
				_ = c.stdin.Close()
			}
			c.process = nil
			c.stdin = nil
		default:
			c.mu.Unlock()
			return nil
		}
	}
	c.mu.Unlock()

	cmd := exec.Command(c.executable, "app-server", "--listen", "stdio://")
	cmd.Env = append(cmd.Environ(), "CODEX_HOME="+c.configDir)
	stdinReader, stdin, err := os.Pipe()
	if err != nil {
		return err
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdinReader.Close()
		_ = stdin.Close()
		return err
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdinReader.Close()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		return err
	}
	cmd.Stdin = stdinReader
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	if err := ctx.Err(); err != nil {
		_ = stdinReader.Close()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		return err
	}
	process, err := providerprocess.Start(cmd)
	_ = stdinReader.Close()
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return fmt.Errorf("start codex app-server: %w", err)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return errors.Join(errors.New("codex adapter is closed"), process.Close())
	}
	c.processEpoch++
	epoch := c.processEpoch
	c.process = process
	c.stdin = stdin
	c.mu.Unlock()
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
		_ = stderr.Close()
	}()
	go c.readLoop(stdout, epoch)
	go c.monitorProcess(process, epoch)

	var initialized struct {
		UserAgent string `json:"userAgent"`
	}
	if err := c.call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name": "teamva", "title": "Teamva", "version": "0.1.0",
		},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &initialized); err != nil {
		return errors.Join(err, c.stopProcess())
	}
	if !codexSupportsExactPermissions(initialized.UserAgent) {
		return errors.Join(errors.New("Codex 0.163.0 or later is required for exact filesystem isolation"), c.stopProcess())
	}
	return c.notify(ctx, "initialized", map[string]any{})
}

const codexRPCTimeout = 30 * time.Second

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	callContext, cancel := context.WithTimeout(ctx, codexRPCTimeout)
	defer cancel()
	id := c.nextID.Add(1)
	response := make(chan rpcResult, 1)
	c.mu.Lock()
	epoch := c.processEpoch
	if c.process == nil {
		c.mu.Unlock()
		return errors.New("codex app-server is not running")
	}
	c.pending[id] = pendingRPC{epoch: epoch, response: response}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.writeContextEpoch(callContext, epoch, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case responseResult := <-response:
		if responseResult.err != nil {
			return responseResult.err
		}
		message := responseResult.message
		if message.Error != nil {
			return message.Error
		}
		if result != nil && len(message.Result) > 0 {
			return json.Unmarshal(message.Result, result)
		}
		return nil
	case <-callContext.Done():
		return callContext.Err()
	}
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.writeContext(ctx, map[string]any{"method": method, "params": params})
}

func (c *Client) write(message any) error {
	return c.writeContext(context.Background(), message)
}

func (c *Client) writeContext(ctx context.Context, message any) error {
	return c.writeContextEpoch(ctx, 0, message)
}

func (c *Client) writeContextEpoch(ctx context.Context, expectedEpoch uint64, message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	stdin := c.stdin
	epoch := c.processEpoch
	process := c.process
	c.mu.Unlock()
	if stdin == nil || process == nil || (expectedEpoch != 0 && epoch != expectedEpoch) {
		return errors.New("codex app-server is not running")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stopClose := context.AfterFunc(ctx, func() { _ = stdin.Close() })
	defer stopClose()
	if deadline, ok := ctx.Deadline(); ok {
		if writer, ok := stdin.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = writer.SetWriteDeadline(deadline)
			defer writer.SetWriteDeadline(time.Time{})
		}
	}
	payload = append(payload, '\n')
	_, err = stdin.Write(payload)
	if err == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (c *Client) readLoop(stdout io.Reader, epoch uint64) {
	if closer, ok := stdout.(io.Closer); ok {
		defer closer.Close()
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		c.mu.Lock()
		current := c.process != nil && c.processEpoch == epoch
		c.mu.Unlock()
		if !current {
			continue
		}
		raw := append([]byte(nil), scanner.Bytes()...)
		var message rpcMessage
		if json.Unmarshal(raw, &message) != nil {
			continue
		}
		if len(message.ID) > 0 && message.Method == "" {
			var id int64
			if json.Unmarshal(message.ID, &id) == nil {
				c.mu.Lock()
				pending := c.pending[id]
				c.mu.Unlock()
				if pending.response != nil && pending.epoch == epoch {
					select {
					case pending.response <- rpcResult{message: message}:
					default:
					}
				}
			}
			continue
		}
		if len(message.ID) > 0 && message.Method != "" {
			c.handleServerRequest(message, raw, epoch)
			continue
		}
		c.handleNotification(message, raw)
	}
	exitErr := scanner.Err()
	if exitErr == nil {
		exitErr = io.EOF
	}
	c.failTransport(epoch, fmt.Errorf("codex app-server stream closed: %w", exitErr))
}

func (c *Client) monitorProcess(process *providerprocess.Tree, epoch uint64) {
	<-process.Done()
	err := process.Err()
	if err == nil {
		err = errors.New("codex app-server exited unexpectedly")
	} else {
		err = fmt.Errorf("codex app-server exited: %w", err)
	}
	c.mu.Lock()
	if c.process == process && c.processEpoch == epoch {
		c.process = nil
		if c.stdin != nil {
			_ = c.stdin.Close()
			c.stdin = nil
		}
	}
	c.mu.Unlock()
	c.failTransport(epoch, err)
}

func (c *Client) failTransport(epoch uint64, err error) {
	c.mu.Lock()
	pending := make([]chan rpcResult, 0, len(c.pending))
	for _, call := range c.pending {
		if call.epoch == epoch {
			pending = append(pending, call.response)
		}
	}
	state := c.active
	c.mu.Unlock()
	for _, response := range pending {
		select {
		case response <- rpcResult{err: err}:
		default:
		}
	}
	if state != nil && state.processEpoch == epoch {
		state.once.Do(func() { state.done <- err })
	}
}

func (c *Client) handleServerRequest(message rpcMessage, raw []byte, sourceEpoch ...uint64) {
	c.mu.Lock()
	epoch := c.processEpoch
	if len(sourceEpoch) > 0 {
		epoch = sourceEpoch[0]
	}
	current := epoch == c.processEpoch && (len(sourceEpoch) == 0 || c.process != nil)
	c.mu.Unlock()
	if !current {
		return
	}
	mcpRequest, isMCPApproval := nativeMCPToolApproval(message)
	if !isApprovalRequest(message.Method) && !isMCPApproval {
		var id any
		_ = json.Unmarshal(message.ID, &id)
		_ = c.writeContextEpoch(context.Background(), epoch, map[string]any{
			"id": id,
			"error": map[string]any{
				"code": -32601, "message": "unsupported Codex server request: " + message.Method,
			},
		})
		return
	}
	requestID := string(message.ID)
	c.mu.Lock()
	state := c.active
	owned := state != nil && !state.request.ReadOnly && state.processEpoch == epoch && c.processEpoch == epoch && (len(sourceEpoch) == 0 || c.process != nil)
	if isMCPApproval && owned {
		owned = mcpRequest.ThreadID == state.threadID && (mcpRequest.TurnID == "" || state.turnID == "" || mcpRequest.TurnID == state.turnID)
	}
	if owned {
		c.approvals[requestID] = pendingApproval{message: message, runID: state.request.RunID, epoch: epoch}
	}
	c.mu.Unlock()
	if owned {
		c.emitState(state, "approval.requested", message.Method, map[string]any{
			"request_id":        requestID,
			"action_kind":       message.Method,
			"normalized_action": json.RawMessage(message.Params),
			"risk":              "high",
		}, raw)
		return
	}
	result := approvalResult(message, adapter.ApprovalDecision{Decision: "denied"})
	if isMCPApproval {
		result["action"] = "cancel"
	}
	var id any
	_ = json.Unmarshal(message.ID, &id)
	_ = c.writeContextEpoch(context.Background(), epoch, map[string]any{"id": id, "result": result})
}

func isApprovalRequest(method string) bool {
	switch method {
	case "item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/permissions/requestApproval",
		"execCommandApproval",
		"applyPatchApproval":
		return true
	default:
		return false
	}
}

func approvalResult(message rpcMessage, decision adapter.ApprovalDecision) map[string]any {
	if request, ok := nativeMCPToolApproval(message); ok {
		return mcpToolApprovalResult(request, decision)
	}
	if message.Method == "item/permissions/requestApproval" {
		if decision.Decision == "approved" {
			var params map[string]any
			_ = json.Unmarshal(message.Params, &params)
			return map[string]any{"permissions": params["permissions"], "scope": "turn"}
		}
		return map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	}
	value := "decline"
	if decision.Decision == "approved" {
		value = "accept"
		if decision.Scope == "session" || decision.Scope == "always" {
			value = "acceptForSession"
		}
	}
	return map[string]any{"decision": value}
}

func (c *Client) DecideApproval(ctx context.Context, decision adapter.ApprovalDecision) error {
	c.mu.Lock()
	state := c.active
	pending, ok := c.approvals[decision.RequestID]
	owned := ok && state != nil && state.request.RunID == decision.RunID && pending.runID == decision.RunID && pending.epoch == state.processEpoch && pending.epoch == c.processEpoch
	if owned {
		delete(c.approvals, decision.RequestID)
	}
	c.mu.Unlock()
	if state == nil || state.request.RunID != decision.RunID {
		return fmt.Errorf("codex run %s is not active", decision.RunID)
	}
	if !owned {
		return fmt.Errorf("codex approval %s is not pending", decision.RequestID)
	}
	message := pending.message
	var id any
	if err := json.Unmarshal(message.ID, &id); err != nil {
		return err
	}
	return c.writeContextEpoch(ctx, pending.epoch, map[string]any{
		"id": id, "result": approvalResult(message, decision),
	})
}

func (c *Client) handleNotification(message rpcMessage, raw []byte) {
	var params map[string]any
	_ = json.Unmarshal(message.Params, &params)
	switch message.Method {
	case "turn/started":
		c.emit("run.started", message.Method, params, raw)
	case "thread/tokenUsage/updated":
		if usage, ok := codexTurnUsage(params); ok {
			c.emit("usage.updated", message.Method, usage, raw)
		} else {
			// Compaction may recompute the context-window estimate with zero
			// input/output counters. Keep it observable, but not as billable delta.
			params["method"] = message.Method
			c.emit("provider.event", message.Method, params, raw)
		}
	case "item/agentMessage/delta":
		c.emit("output.delta", message.Method, map[string]any{
			"text": params["delta"], "item_id": params["itemId"],
		}, raw)
	case "item/started", "item/completed":
		item, _ := params["item"].(map[string]any)
		if item["type"] == "contextCompaction" {
			payload := map[string]any{
				"id": item["id"], "provider": "codex", "status": "running",
			}
			if message.Method == "item/completed" {
				payload["status"] = "completed"
			}
			c.emit("context.compaction", message.Method, payload, raw)
		} else {
			// Item lifecycle is authoritative even when native status/timestamps
			// are absent. Preserve it in the durable payload without changing raw.
			params["method"] = message.Method
			c.emit("provider.item", message.Method, params, raw)
		}
	case "turn/diff/updated":
		c.emit("workspace.diff", message.Method, params, raw)
	case "error":
		params = nodeprotocol.NormalizeDiagnosticPayload(params)
		params["kind"], params["method"] = "error", message.Method
		c.emit(codexErrorEventKind(params), message.Method, params, raw)
	case "turn/completed":
		c.handleTurnCompleted(message.Method, params, raw)
	default:
		if strings.HasPrefix(message.Method, "item/") || strings.HasPrefix(message.Method, "turn/") {
			// Keep the native method when persisting diagnostics/deltas. Without
			// it a reasoning delta cannot be distinguished from tool output.
			params["method"] = message.Method
			c.emit("provider.event", message.Method, params, raw)
		}
	}
}

func (c *Client) handleTurnCompleted(providerType string, params map[string]any, raw []byte) {
	status := strings.ToLower(strings.TrimSpace(nestedString(params, "turn", "status")))
	switch status {
	case "completed", "succeeded":
		c.emit("run.completed", providerType, params, raw)
		c.finishActive(nil)
	case "interrupted", "cancelled":
		c.emit("run.finished", providerType, codexTerminalPayload(params, "cancelled", "interrupted"), raw)
		c.finishActive(nil)
	case "failed":
		payload := nodeprotocol.NormalizeDiagnosticPayload(codexTerminalPayload(params, "failed", "provider_failed"))
		c.emit("run.finished", providerType, payload, raw)
		message, _ := payload["message"].(string)
		if message == "" {
			message = "codex turn failed"
		}
		c.failActive(errors.New(message))
	default:
		c.emit("run.finished", providerType, codexTerminalPayload(params, "failed", "provider_failed"), raw)
		if status == "" {
			c.failActive(errors.New("codex turn completed without a status"))
			return
		}
		c.failActive(fmt.Errorf("codex turn completed with unknown status %q", status))
	}
}

// codexTerminalPayload adds the normalized terminal projection without
// discarding Codex's original params. ProviderRaw remains attached by emit.
func codexTerminalPayload(params map[string]any, status, reason string) map[string]any {
	payload := make(map[string]any, len(params)+2)
	for key, value := range params {
		payload[key] = value
	}
	payload["status"] = status
	payload["reason"] = reason
	return payload
}

// codexTurnUsage normalizes Codex app-server's per-turn `last` usage. The
// `total` object is cumulative for the native thread and therefore must not be
// summed across Teamva runs. `last.totalTokens` is the provider's own
// accounting value (cached input is already included in input tokens).
func codexTurnUsage(params map[string]any) (map[string]any, bool) {
	tokenUsage, _ := params["tokenUsage"].(map[string]any)
	last, _ := tokenUsage["last"].(map[string]any)
	total := nonNegativeInteger(last["totalTokens"])
	if total <= 0 {
		return nil, false
	}
	_, hasInput := last["inputTokens"]
	_, hasOutput := last["outputTokens"]
	if hasInput && hasOutput && nonNegativeInteger(last["inputTokens"]) == 0 && nonNegativeInteger(last["outputTokens"]) == 0 &&
		nonNegativeInteger(last["cachedInputTokens"]) == 0 && nonNegativeInteger(last["reasoningOutputTokens"]) == 0 {
		return nil, false
	}
	return map[string]any{
		"accounting_mode":         "delta",
		"input_tokens":            nonNegativeInteger(last["inputTokens"]),
		"cached_input_tokens":     nonNegativeInteger(last["cachedInputTokens"]),
		"output_tokens":           nonNegativeInteger(last["outputTokens"]),
		"reasoning_output_tokens": nonNegativeInteger(last["reasoningOutputTokens"]),
		"total_tokens":            total,
	}, true
}

func nonNegativeInteger(value any) int64 {
	switch typed := value.(type) {
	case float64:
		if typed > 0 {
			return int64(typed)
		}
	case int:
		if typed > 0 {
			return int64(typed)
		}
	case int64:
		if typed > 0 {
			return typed
		}
	case json.Number:
		if parsed, err := typed.Int64(); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

func codexErrorEventKind(params map[string]any) string {
	if willRetry, _ := params["willRetry"].(bool); willRetry {
		// Codex emits `error` notifications for transient stream disconnects
		// while it is actively reconnecting. They are diagnostic provider
		// events, not terminal run failures.
		return "provider.event"
	}
	return "run.error"
}

func nestedString(input map[string]any, keys ...string) string {
	var current any = input
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[key]
	}
	value, _ := current.(string)
	return value
}

func (c *Client) emit(kind, providerType string, payload map[string]any, raw []byte) {
	c.mu.Lock()
	state := c.active
	c.mu.Unlock()
	c.emitState(state, kind, providerType, payload, raw)
}

func (c *Client) emitState(state *runState, kind, providerType string, payload map[string]any, raw []byte) {
	if state == nil || state.sink == nil {
		return
	}
	err := state.sink(state.ctx, adapter.Event{
		RunID: state.request.RunID, Type: kind, Payload: payload,
		ProviderEventType: providerType, ProviderRaw: append([]byte(nil), raw...),
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		state.once.Do(func() { state.done <- err })
	}
}

func (c *Client) finishActive(err error) {
	c.mu.Lock()
	state := c.active
	c.mu.Unlock()
	if state != nil {
		state.once.Do(func() { state.done <- err })
	}
}

func (c *Client) failActive(err error) {
	c.finishActive(err)
}

type runNotActiveError struct {
	provider string
	runID    string
}

func (e *runNotActiveError) Error() string {
	return fmt.Sprintf("%s run %s is not active", e.provider, e.runID)
}

func (e *runNotActiveError) RunNotActive() bool { return true }

func (c *Client) stopProcess() error {
	c.mu.Lock()
	process := c.process
	epoch := c.processEpoch
	stdin := c.stdin
	c.process = nil
	c.stdin = nil
	clear(c.approvals)
	c.mu.Unlock()
	var closeErr error
	if process != nil {
		// Keep the leader alive while Tree identifies and terminates its job.
		// Closing stdin first can make app-server exit and orphan freshly forked
		// descendants before the process-group ownership check runs.
		closeErr = process.Close()
	}
	if stdin != nil {
		_ = stdin.Close()
	}
	c.failTransport(epoch, errors.New("codex app-server stopped"))
	return closeErr
}

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
	// First stop catches an already-installed process without waiting for an
	// in-flight health/RPC handshake. The start gate plus the closed recheck in
	// ensureStarted then proves no process can be installed after Close returns.
	firstErr := c.stopProcess()
	c.startMu.Lock()
	secondErr := c.stopProcess()
	c.startMu.Unlock()
	return errors.Join(firstErr, secondErr)
}

var _ adapter.Adapter = (*Client)(nil)
