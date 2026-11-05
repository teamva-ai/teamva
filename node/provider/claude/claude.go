package claude

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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
	providerprocess "github.com/teamva-ai/teamva/node/provider/process"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type Client struct {
	provider.UnsupportedOperations
	executable, configDir, dataDir, ownedDirectory string
	mu                                             sync.Mutex
	probeMu                                        sync.Mutex
	probeCancel                                    context.CancelFunc
	probeDone                                      chan struct{}
	active                                         *activeRun
	closed                                         bool
}
type activeRun struct {
	id          string
	tree        *providerprocess.Tree
	stdin       *os.File
	writeMu     sync.Mutex
	interrupted bool
	cancelTools context.CancelFunc
}

func (c *Client) Name() string { return "claude" }
func (a *activeRun) send(value any) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return json.NewEncoder(a.stdin).Encode(value)
}
func controlRequest(id, subtype string) map[string]any {
	return map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": subtype}}
}

func (c *Client) Probe(ctx context.Context) (provider.CapabilityManifest, error) {
	m, _, err := c.Diagnose(ctx)
	return m, err
}
func (c *Client) Diagnose(ctx context.Context) (provider.CapabilityManifest, []nodeprotocol.DiagnosticStep, error) {
	c.probeMu.Lock()
	defer c.probeMu.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return provider.CapabilityManifest{Provider: "claude"}, nil, errors.New("claude instance is closed")
	}
	ctx, cancelProbe := context.WithCancel(ctx)
	done := make(chan struct{})
	c.probeCancel = cancelProbe
	c.probeDone = done
	c.mu.Unlock()
	defer func() {
		cancelProbe()
		c.mu.Lock()
		if c.probeDone == done {
			c.probeCancel = nil
			c.probeDone = nil
		}
		close(done)
		c.mu.Unlock()
	}()
	m := provider.CapabilityManifest{Provider: "claude", Streaming: true, Resume: true, Interrupt: true, MaxParallelRuns: 1, CapabilityVersion: 1}
	steps := []nodeprotocol.DiagnosticStep{}
	step := func(id, status, detail string) {
		labels := map[string]string{"executable": "Claude Code 可执行文件", "version": "CLI 隔离运行能力", "authentication": "隔离实例 API 认证"}
		steps = append(steps, nodeprotocol.DiagnosticStep{ID: id, Label: labels[id], Status: status, Message: detail})
	}
	if _, err := exec.LookPath(c.executable); err != nil {
		m.UnavailableReasonCode = "executable_not_found"
		step("executable", "failed", "Claude Code executable not found")
		return m, steps, nil
	}
	step("executable", "success", "Claude Code executable found")
	account, err := accountEnvironment()
	if err != nil {
		m.UnavailableReasonCode = "authentication_failed"
		step("authentication", "failed", err.Error())
		return m, steps, nil
	}
	r := newRedactor(account)
	command := func(args ...string) ([]byte, error) {
		cmd := exec.Command(c.executable, args...)
		cmd.Env = c.environment(account)
		cmd.Dir = c.dataDir
		return providerprocess.CombinedOutput(ctx, cmd, 1<<20)
	}
	version, err := command("--version")
	if err != nil {
		m.UnavailableReasonCode = "probe_failed"
		step("version", "failed", "Claude Code version probe failed")
		return m, steps, nil
	}
	m.ProviderVersion = r.diagnosticText(strings.TrimSpace(string(version)))
	help, err := command("--help")
	if err != nil {
		m.UnavailableReasonCode = "probe_failed"
		step("version", "failed", "Claude Code flags unavailable")
		return m, steps, nil
	}
	for _, flag := range []string{"--bare", "--strict-mcp-config", "--setting-sources", "--input-format", "--tools"} {
		if !strings.Contains(string(help), flag) {
			m.UnavailableReasonCode = "version_unsupported"
			step("version", "failed", "Claude Code lacks required isolation/SDK flags")
			return m, steps, nil
		}
	}
	m.Available = true
	step("version", "success", m.ProviderVersion)
	if account["ANTHROPIC_API_KEY"] == "" && account["ANTHROPIC_AUTH_TOKEN"] == "" {
		m.UnavailableReasonCode = "authentication_required"
		step("authentication", "failed", "API credential is not configured in Claude settings env; OAuth and helpers are not imported")
		return m, steps, nil
	}
	status, err := command("auth", "status", "--json")
	var auth struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if err != nil || json.Unmarshal(status, &auth) != nil || !auth.LoggedIn {
		m.UnavailableReasonCode = "authentication_required"
		step("authentication", "failed", "Isolated Claude Code auth status is not logged in")
		return m, steps, nil
	}
	m.Authenticated = true
	step("authentication", "success", "Claude Code recognizes the projected API credential (not a model/API validation)")
	seen := map[string]bool{}
	for _, name := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"} {
		if model := account[name]; model != "" && !seen[model] {
			m.Models = append(m.Models, model)
			seen[model] = true
		}
	}
	return m, steps, nil
}

var sessionIDPattern = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

func newSessionID() string {
	value := randomID()
	return value[:8] + "-" + value[8:12] + "-4" + value[13:16] + "-a" + value[17:20] + "-" + value[20:]
}
func (c *Client) Start(ctx context.Context, request provider.RunRequest, sink provider.Sink) (provider.NativeSession, error) {
	if request.NativeSessionID != "" {
		return provider.NativeSession{}, errors.New("claude Start must not resume a session")
	}
	return c.run(ctx, request, sink, false)
}
func (c *Client) Resume(ctx context.Context, request provider.RunRequest, sink provider.Sink) (provider.NativeSession, error) {
	if !sessionIDPattern.MatchString(request.NativeSessionID) {
		return provider.NativeSession{}, errors.New("invalid claude session id")
	}
	ok, err := c.VerifySession(ctx, request.NativeSessionID)
	if err != nil {
		return provider.NativeSession{}, err
	}
	if !ok {
		return provider.NativeSession{}, errors.New("claude session is not present in this instance")
	}
	var record struct {
		CWD string `json:"cwd"`
	}
	data, err := os.ReadFile(filepath.Join(c.dataDir, "sessions", request.NativeSessionID+".json"))
	if err != nil || json.Unmarshal(data, &record) != nil {
		return provider.NativeSession{}, errors.New("claude session scope unavailable")
	}
	cwd, err := filepath.EvalSymlinks(request.CWD)
	if err != nil || cwd != record.CWD {
		return provider.NativeSession{}, errors.New("claude session workspace scope mismatch")
	}
	return c.run(ctx, request, sink, true)
}

func (c *Client) run(ctx context.Context, request provider.RunRequest, sink provider.Sink, resume bool) (session provider.NativeSession, returnErr error) {
	if err := ctx.Err(); err != nil {
		return session, err
	}
	if len(request.Attachments) != 0 {
		return session, provider.ErrUnsupported
	}
	if request.RunID == "" || sink == nil {
		return session, errors.New("claude run requires an id and sink")
	}
	config, err := c.config()
	if err != nil {
		return session, err
	}
	workspaceInstructions, err := readWorkspaceInstructions(ctx, config.WorkspaceInstructions)
	if err != nil {
		return session, err
	}
	if len(config.Skills) != 0 || config.ComputerUse || len(config.MCPServers) != 0 {
		return session, provider.ErrUnsupported
	}
	scope, err := newToolScope(c, config, request)
	if err != nil {
		return session, err
	}
	defer scope.close()
	account, err := accountEnvironment()
	if err != nil {
		return session, err
	}
	if account["ANTHROPIC_API_KEY"] == "" && account["ANTHROPIC_AUTH_TOKEN"] == "" {
		return session, errors.New("claude API authentication is required")
	}
	redact := newRedactor(account)
	c.mu.Lock()
	if c.closed || c.active != nil {
		c.mu.Unlock()
		return session, errors.New("claude instance is closed or busy")
	}
	a := &activeRun{id: request.RunID}
	c.active = a
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.active == a {
			c.active = nil
		}
		c.mu.Unlock()
	}()
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return session, errors.New("prepare claude input failed")
	}
	defer stdinRead.Close()
	defer stdinWrite.Close()
	c.mu.Lock()
	a.stdin = stdinWrite
	c.mu.Unlock()
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		return session, errors.New("prepare claude output failed")
	}
	defer stdoutRead.Close()
	defer stdoutWrite.Close()
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		return session, errors.New("prepare claude diagnostics failed")
	}
	defer stderrRead.Close()
	defer stderrWrite.Close()
	// stderr is never forwarded raw. Keep a bounded, redacted tail only for
	// failure reporting; unexpected CLI diagnostics may contain API credentials.
	stderrResult := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(stderrRead, 64<<10))
		_, _ = io.Copy(io.Discard, stderrRead)
		stderrResult <- redact.diagnosticText(strings.TrimSpace(string(data)))
	}()
	prompt := config.SystemPrompt + "\n" + workspaceInstructions + "\nYou run inside Teamva. Native tools, shell, hooks, skills, plugins and external MCP are disabled. Use only the teamva MCP tools. Paths are scoped by the host. For the file-based host protocol documented in the user prompt, call the corresponding named MCP tool with the same JSON arguments instead of writing request/reading response files. The tool returns the real receipt. An Issue public summary must use issue_comment_create; ordinary assistant text is not a public comment. Do not claim shell execution, git, tests, network access, unsupported tools or approvals happened."
	promptPath := filepath.Join(c.configDir, "system-prompt.txt")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return session, errors.New("write claude system prompt failed")
	}
	args := []string{"--print", "--bare", "--disable-slash-commands", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--setting-sources=", "--strict-mcp-config", "--tools", "", "--permission-mode", "default", "--permission-prompt-tool", "stdio", "--system-prompt-file", promptPath}
	mcp := `{"mcpServers":{}}`
	if !scope.disabled {
		mcp = `{"mcpServers":{"teamva":{"type":"sdk","name":"teamva"}}}`
	}
	args = append(args, "--mcp-config", mcp)
	model := request.Model
	if model == "" {
		model = config.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	id := request.NativeSessionID
	if !resume {
		id = newSessionID()
		args = append(args, "--session-id="+id)
	} else {
		args = append(args, "--resume="+id)
	}
	session = provider.NativeSession{ID: id, Provider: "claude"}
	command := exec.Command(c.executable, args...)
	command.Dir = request.CWD
	command.Env = c.environment(account)
	command.Stdin = stdinRead
	command.Stdout = stdoutWrite
	command.Stderr = stderrWrite
	tree, err := providerprocess.Start(command)
	_ = stdinRead.Close()
	_ = stdoutWrite.Close()
	_ = stderrWrite.Close()
	if err != nil {
		return session, errors.New("start Claude Code failed")
	}
	c.mu.Lock()
	a.tree = tree
	closed := c.closed
	interrupted := a.interrupted
	c.mu.Unlock()
	if closed || interrupted {
		_ = tree.Terminate()
	}
	defer func() { _ = tree.Close() }()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	toolCtx, cancelTools := context.WithCancel(runCtx)
	defer cancelTools()
	c.mu.Lock()
	a.cancelTools = cancelTools
	alreadyInterrupted := a.interrupted
	c.mu.Unlock()
	if alreadyInterrupted {
		cancelTools()
	}
	// Do not allow an unresponsive CLI or blocked tool to delay context cancel.
	go func() {
		select {
		case <-runCtx.Done():
			_ = tree.Terminate()
		case <-tree.Done():
		}
	}()
	messages := make(chan map[string]any, 16)
	scanErrors := make(chan error, 1)
	go func() {
		defer close(messages)
		scanner := bufio.NewScanner(stdoutRead)
		scanner.Buffer(make([]byte, 64<<10), 8<<20)
		for scanner.Scan() {
			var message map[string]any
			if json.Unmarshal(scanner.Bytes(), &message) != nil {
				scanErrors <- errors.New("invalid claude stream-json frame")
				return
			}
			select {
			case messages <- message:
			case <-runCtx.Done():
				return
			}
		}
		scanErrors <- scanner.Err()
	}()
	boundary := newEventBoundary(redact, func(kind string, payload map[string]any) error {
		// Runtime and FileBridge/ToolBroker share this Run's durable cursor.
		// Native frames have no host sequence: leave Sequence=0 so Runtime can
		// atomically assign it alongside independently inserted host events.
		return sink(ctx, provider.Event{RunID: request.RunID, Type: kind, Payload: payload, OccurredAt: time.Now().UTC()})
	})
	emit := boundary.emit
	projection := newStreamProjection(emit)
	terminal := false
	defer func() {
		if projection.compactionID != "" {
			status := "failed"
			if errors.Is(returnErr, context.Canceled) || errors.Is(returnErr, context.DeadlineExceeded) || a.isInterrupted(c) {
				status = "cancelled"
			}
			_ = emit("context.compaction", map[string]any{"id": projection.compactionID, "status": status, "reason": "Run ended before Claude reported compaction completion"})
		}
		if !terminal && returnErr != nil {
			status := "failed"
			if errors.Is(returnErr, context.Canceled) || errors.Is(returnErr, context.DeadlineExceeded) || a.isInterrupted(c) {
				status = "cancelled"
			}
			_ = emit("run.finished", map[string]any{"status": status, "reason": returnErr.Error()})
		}
	}()
	initID := "initialize-" + randomID()
	init := controlRequest(initID, "initialize")
	init["request"].(map[string]any)["hooks"] = nil
	if err = a.send(init); err != nil {
		return session, errors.New("initialize claude protocol failed")
	}
	initialized := false
	started := false
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return session, ctx.Err()
		case <-timer.C:
			if !initialized {
				return session, errors.New("claude SDK initialization timed out")
			}
		case message, ok := <-messages:
			if !ok {
				if ctx.Err() != nil {
					return session, ctx.Err()
				}
				if a.isInterrupted(c) {
					return session, context.Canceled
				}
				scanErr := <-scanErrors
				if scanErr != nil {
					return session, scanErr
				}
				_ = tree.Wait(ctx)
				diagnostics := <-stderrResult
				if diagnostics != "" {
					return session, fmt.Errorf("Claude Code exited before result: %s", diagnostics)
				}
				return session, errors.New("Claude Code exited before result")
			}
			typeName, _ := message["type"].(string)
			if typeName == "control_response" {
				response, _ := message["response"].(map[string]any)
				if response["request_id"] == initID {
					if response["subtype"] != "success" {
						return session, fmt.Errorf("Claude Code initialize failed: %s", redact.diagnosticText(stringValue(response["error"])))
					}
					initialized = true
					timer.Stop()
					if err = a.send(map[string]any{"type": "user", "session_id": id, "message": map[string]any{"role": "user", "content": request.Prompt}, "parent_tool_use_id": nil}); err != nil {
						return session, errors.New("send Claude Code prompt failed")
					}
				}
				continue
			}
			if typeName == "control_request" {
				if err = c.handleControl(toolCtx, a, scope, message, emit, redact); err != nil {
					return session, err
				}
				continue
			}
			if typeName == "control_cancel_request" {
				continue
			}
			if native := stringValue(message["session_id"]); native != "" && native != id {
				return session, errors.New("Claude Code returned a different native session id")
			}
			if typeName == "system" && message["subtype"] == "init" {
				if stringValue(message["session_id"]) != id {
					return session, errors.New("Claude Code init did not confirm the native session id")
				}
				if !started {
					if err = c.recordSession(id, request.CWD); err != nil {
						return session, err
					}
					if err = emit("run.started", map[string]any{"status": "running", "native_session_id": id}); err != nil {
						return session, err
					}
					started = true
				}
				// Never persist the complete init: it can contain API key source,
				// private paths, plugins or future secret-bearing metadata.
				continue
			}
			if typeName == "result" {
				terminal = true
				status := resultStatus(message)
				if a.isInterrupted(c) {
					status = "cancelled"
				}
				if err = projection.result(message, status); err != nil {
					return session, err
				}
				if projection.compactionID != "" {
					compactionStatus := "failed"
					if status == "cancelled" {
						compactionStatus = "cancelled"
					}
					if err = emit("context.compaction", map[string]any{"id": projection.compactionID, "status": compactionStatus, "reason": "Claude result arrived without a successful compaction boundary"}); err != nil {
						return session, err
					}
					projection.compactionID = ""
				}
				payload := map[string]any{"status": status}
				if status == "failed" {
					payload["reason"] = errorMessage(message)
					code := stringValue(message["subtype"])
					if code == "success" {
						code = stringValue(message["terminal_reason"])
						if code == "" {
							code = "api_error"
						}
					}
					payload["code"] = code
					if value, ok := message["api_error_status"]; ok {
						payload["status_code"] = value
					}
				}
				if err = emit("run.finished", payload); err != nil {
					return session, err
				}
				_ = a.stdin.Close()
				if status == "cancelled" {
					return session, context.Canceled
				}
				if status == "failed" {
					return session, fmt.Errorf("Claude Code: %s", redact.diagnosticText(errorMessage(message)))
				}
				return session, nil
			}
			if !terminal {
				if err = projection.message(message); err != nil {
					return session, err
				}
			}
		}
	}
}

func (a *activeRun) isInterrupted(c *Client) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return a.interrupted
}
func (c *Client) Interrupt(ctx context.Context, runID string) error {
	c.mu.Lock()
	a := c.active
	if a == nil || a.id != runID {
		c.mu.Unlock()
		return errors.New("claude run is not active")
	}
	a.interrupted = true
	ready := a.stdin != nil
	tree := a.tree
	cancelTools := a.cancelTools
	c.mu.Unlock()
	if cancelTools != nil {
		cancelTools()
	}
	if !ready {
		if tree != nil {
			return tree.Terminate()
		}
		return nil
	}
	if err := a.send(controlRequest("interrupt-"+randomID(), "interrupt")); err != nil {
		if tree != nil {
			_ = tree.Terminate()
		}
		return errors.New("send Claude Code interrupt failed")
	}
	// Native interrupt is attempted first; process ownership is the fail-safe
	// when an old/broken CLI doesn't acknowledge or finish cancellation.
	if tree != nil {
		go func() {
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			select {
			case <-tree.Done():
			case <-timer.C:
				_ = tree.Terminate()
			}
		}()
	}
	return nil
}
func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	a := c.active
	cancelProbe, probeDone := c.probeCancel, c.probeDone
	var tree *providerprocess.Tree
	if a != nil {
		tree = a.tree
	}
	owned := c.ownedDirectory
	c.ownedDirectory = ""
	c.mu.Unlock()
	if cancelProbe != nil {
		cancelProbe()
	}
	if tree != nil {
		_ = tree.Close()
	}
	if probeDone != nil {
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-probeDone:
		case <-timer.C:
		}
		timer.Stop()
	}
	if owned != "" {
		_ = os.RemoveAll(owned)
	}
	return nil
}

func (c *Client) recordSession(id, cwd string) error {
	cwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return errors.New("resolve Claude session workspace failed")
	}
	data, _ := json.Marshal(map[string]any{"cwd": cwd})
	root, err := os.OpenRoot(filepath.Join(c.dataDir, "sessions"))
	if err != nil {
		return errors.New("open Claude session registry failed")
	}
	defer root.Close()
	f, err := root.OpenFile(id+".json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return errors.New("record Claude session scope failed")
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return errors.New("record Claude session scope failed")
	}
	return nil
}
func (c *Client) VerifySession(ctx context.Context, id string) (bool, error) {
	if !sessionIDPattern.MatchString(id) {
		return false, nil
	}
	root, err := os.OpenRoot(filepath.Join(c.dataDir, "sessions"))
	if err != nil {
		return false, nil
	}
	defer root.Close()
	info, err := root.Lstat(id + ".json")
	if err != nil || !info.Mode().IsRegular() {
		return false, nil
	}
	// Verify an actual native transcript, not a host marker alone. The entire
	// native home belongs to this exact instance/project/workspace generation.
	projects := filepath.Join(c.dataDir, "claude", "projects")
	found := false
	count := 0
	err = filepath.WalkDir(projects, func(path string, entry os.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		count++
		if count > 10000 {
			return errors.New("claude session scan limit exceeded")
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == id+".jsonl" && !entry.IsDir() {
			information, e := entry.Info()
			if e == nil && information.Mode().IsRegular() && information.Size() > 0 {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("verify Claude native session failed")
	}
	return found, nil
}

func (c *Client) handleControl(ctx context.Context, a *activeRun, scope *toolScope, message map[string]any, emit func(string, map[string]any) error, redact redactor) error {
	id, _ := message["request_id"].(string)
	request, _ := message["request"].(map[string]any)
	response := map[string]any{}
	responseType := "success"
	responseError := ""
	switch request["subtype"] {
	case "can_use_tool":
		name, _ := request["tool_name"].(string)
		name = strings.TrimPrefix(name, "mcp__"+mcpServerName+"__")
		if scope.permits(name) && strings.HasPrefix(stringValue(request["tool_name"]), "mcp__"+mcpServerName+"__") {
			response = map[string]any{"behavior": "allow", "updatedInput": request["input"]}
		} else {
			response = map[string]any{"behavior": "deny", "message": "Tool is outside the host-authorized catalog; permission escalation is not available"}
		}
	case "mcp_message":
		if request["server_name"] != mcpServerName {
			responseType = "error"
			responseError = "Unknown SDK MCP server"
			break
		}
		mcpMessage, _ := request["message"].(map[string]any)
		name := ""
		callID := randomID()
		var mcpResponse map[string]any
		if mcpMessage["method"] == "tools/call" {
			params, _ := mcpMessage["params"].(map[string]any)
			name, _ = params["name"].(string)
			// Native transports can call MCP directly: reject known credentials
			// before any file/IPC write, rather than rewriting tool arguments.
			if redact.containsCredential(params["arguments"]) {
				mcpResponse = map[string]any{"jsonrpc": "2.0", "id": mcpMessage["id"], "result": map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "Tool arguments contain a protected provider credential; request denied."}}}}
			}
			if err := emit("provider.tool", map[string]any{"id": callID, "tool": name, "status": "running"}); err != nil {
				return err
			}
		}
		if mcpResponse == nil {
			mcpResponse = scope.mcp(ctx, mcpMessage)
		}
		if name != "" {
			status := "completed"
			result, _ := mcpResponse["result"].(map[string]any)
			if result["isError"] == true {
				status = "failed"
			}
			if err := emit("provider.tool", map[string]any{"id": callID, "tool": name, "status": status}); err != nil {
				return err
			}
			if name == "write_file" && status == "completed" {
				params, _ := mcpMessage["params"].(map[string]any)
				args, _ := params["arguments"].(map[string]any)
				if path := stringValue(args["path"]); path != "" {
					if err := emit("files.changed", map[string]any{"id": callID, "paths": []any{path}}); err != nil {
						return err
					}
				}
			}
		}
		response = map[string]any{"mcp_response": redact.value(mcpResponse)}
	default:
		responseType = "error"
		responseError = "Unsupported control request; hooks and permission escalation are disabled"
	}
	result := map[string]any{"subtype": responseType, "request_id": id, "response": response}
	if responseError != "" {
		result["error"] = responseError
	}
	if err := a.send(map[string]any{"type": "control_response", "response": result}); err != nil {
		return errors.New("send Claude Code control response failed")
	}
	return nil
}
