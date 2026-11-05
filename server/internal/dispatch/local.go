package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/node/embedded"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/teamva-ai/teamva/server/internal/toolservice"
)

type LocalConfig struct {
	DataDir       string
	ProviderPaths map[string]string
	Logger        *log.Logger
	// Private configuration is resolved in memory immediately before a Run.
	MCPResolver func(context.Context, string) (map[string]json.RawMessage, error)
}

// LocalExecutionStore is owned by the in-process dispatcher. Besides the
// shared business operations it lists the SQLite-style durable outbox,
// recovery, lease and checkpoint operations required for local execution.
type LocalExecutionStore interface {
	toolservice.Repository
	store.LocalGoalRepository
	LocalIdentity() (domain.User, domain.Computer, error)
	Checkpoint(context.Context) error
	UpsertAgentService(context.Context, string, domain.AgentService) (domain.AgentService, error)
	GetRun(context.Context, string, string) (domain.Run, error)
	GetIssue(context.Context, string, string) (domain.Issue, error)
	UpdateIssue(context.Context, string, string, int64, func(*domain.Issue)) (domain.Issue, error)
	ListResources(context.Context, string, string) ([]domain.ProjectResource, error)
	ListIssueComments(context.Context, string, string) ([]domain.IssueComment, error)
	ListIssuePromptComments(context.Context, string, string) ([]domain.IssueComment, bool, error)
	RecoverLocalExecution(context.Context) error
	TriggerDueLocalSchedules(context.Context, time.Time) (int, error)
	ClaimNextIssueMention(context.Context, time.Time, time.Duration) (store.IssueMentionClaim, bool, error)
	RetryLocalIssueMention(context.Context, string, string, string, time.Time) error
	DeadLetterLocalIssueMention(context.Context, string, string, string) error
	CreateRunForClaimedMention(context.Context, store.IssueMentionClaim, []domain.ContentBlock, string) (domain.Run, error)
	ClaimLocalCommand(context.Context, time.Time, time.Duration, ...string) (store.LocalCommand, bool, error)
	RenewLocalCommandLease(context.Context, string, string, time.Time, time.Duration) error
	CompleteLocalCommand(context.Context, string, string, json.RawMessage) error
	RetryLocalCommand(context.Context, string, string, string, time.Time) error
	MarkLocalCommandUncertain(context.Context, string, string, string) error
	FailLocalCommand(context.Context, string, string, string, json.RawMessage) error
	ApplyLocalEvents(context.Context, []nodeprotocol.RunEvent) ([]nodeprotocol.RunEventCursor, error)
	LocalRun(context.Context, string) (domain.Run, error)
}

type runtimeController interface {
	Probe(context.Context) ([]nodeprotocol.AgentManifest, error)
	Handle(context.Context, nodeprotocol.CommandEnvelope) (nodeprotocol.CommandResult, error)
	UnackedEvents(context.Context, int) ([]nodeprotocol.RunEvent, error)
	AckEvents(context.Context, []nodeprotocol.RunEventCursor) error
	Close() error
}

// GitRead remains separate from runtimeController/Handle. In particular, none
// of these calls may be represented as a LocalCommand or persisted in its
// result column.
type GitRead interface {
	ListGitRepositories(context.Context, nodeprotocol.GitRepositoriesRequest) (nodeprotocol.GitRepositoryList, error)
	ListGitChanges(context.Context, nodeprotocol.GitChangesRequest) (nodeprotocol.GitChangesPage, error)
	ReadGitDiff(context.Context, nodeprotocol.GitDiffRequest) (nodeprotocol.GitDiffResult, error)
	RefreshGit(context.Context, nodeprotocol.GitRefreshRequest) (nodeprotocol.GitRefreshResult, error)
	SubscribeGitEvents(context.Context, nodeprotocol.GitRepositoriesRequest) (<-chan nodeprotocol.GitEvent, func(), error)
	GetRunGitSnapshots(context.Context, string) ([]nodeprotocol.GitRunWorkspaceSnapshot, error)
}

// Local owns durable dispatch, scheduling and recovery for the embedded Runtime.
type Local struct {
	repository LocalExecutionStore
	controller runtimeController
	user       domain.User
	computer   domain.Computer
	toolCaller *localToolCaller
	// commandLease is deliberately kept on the dispatcher rather than baked
	// into executeOutboxCommand. Tests can exercise renewal with a short lease,
	// while production uses a one-minute lease.
	commandLeaseNanos atomic.Int64

	ctx       context.Context
	cancel    context.CancelFunc
	wake      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
	// Discovery shares mutable Provider clients with manual diagnostics and
	// executable changes. Keep at most one operation in flight, independently of
	// the event/outbox loop, and fence new operations before Close waits.
	discoveryMu sync.Mutex
	discovery   chan struct{}
	closing     bool
	// Both the event loop and command-result path drain the durable outbox.
	// Serialize complete commit-before-ACK cycles, not only individual reads.
	eventFlushMu sync.Mutex

	mu           sync.RWMutex
	runWaiters   map[string]map[chan struct{}]struct{}
	issueWaiters map[string]map[chan struct{}]struct{}
	inFlight     map[string]struct{}
}

func NewLocal(ctx context.Context, repository LocalExecutionStore, config LocalConfig) (*Local, error) {
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		return nil, err
	}
	caller := &localToolCaller{tools: toolservice.New(repository), computer: computer}
	controller, err := embedded.New(ctx, embedded.Config{
		ComputerID: computer.ID, DataDir: config.DataDir,
		ProviderPaths: config.ProviderPaths,
		ToolCaller:    caller, Logger: config.Logger,
	})
	if err != nil {
		return nil, err
	}
	controller.SetMCPResolver(config.MCPResolver)
	return newLocalWithController(ctx, repository, controller, caller, user, computer)
}

func newLocalWithController(
	ctx context.Context, repository LocalExecutionStore, controller runtimeController,
	caller *localToolCaller, user domain.User, computer domain.Computer,
) (*Local, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	local := &Local{
		repository: repository, controller: controller, user: user, computer: computer,
		toolCaller: caller, wake: make(chan struct{}, 1), closeDone: make(chan struct{}),
		runWaiters:   make(map[string]map[chan struct{}]struct{}),
		issueWaiters: make(map[string]map[chan struct{}]struct{}),
		inFlight:     make(map[string]struct{}),
		discovery:    make(chan struct{}, 1),
	}
	local.commandLeaseNanos.Store(int64(time.Minute))
	caller.notify = local.NotifyIssueActivation
	// Runtime events are imported before recovery classifies surviving active
	// runs. This preserves a completion event committed by runtime just before
	// the previous process exited and still guarantees commit-before-ACK.
	if err := local.flushEvents(ctx); err != nil {
		_ = controller.Close()
		return nil, err
	}
	if err := repository.RecoverLocalExecution(ctx); err != nil {
		_ = controller.Close()
		return nil, err
	}
	runContext, cancel := context.WithCancel(ctx)
	local.ctx = runContext
	local.cancel = cancel
	local.wg.Add(2)
	go local.run(runContext)
	go local.runProbes(runContext)
	local.startAutoTitles(runContext)
	return local, nil
}

func (l *Local) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	l.closeOnce.Do(func() {
		l.discoveryMu.Lock()
		l.closing = true
		if l.cancel != nil {
			l.cancel()
		}
		l.discoveryMu.Unlock()
		go l.finishClose()
	})
	select {
	case <-l.closeDone:
		return l.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Local) finishClose() {
	controllerDone := make(chan error, 1)
	go func() {
		controllerDone <- l.controller.Close()
	}()
	// Closing the controller/providers is intentionally concurrent with this
	// wait: provider shutdown is what releases a Handle implementation that
	// does not return from context cancellation alone. The repository remains
	// available until every command handler has finished its durable cleanup.
	l.wg.Wait()
	controllerErr := <-controllerDone
	l.closeErr = errors.Join(controllerErr, l.repository.Checkpoint(context.Background()))
	close(l.closeDone)
}

func (l *Local) Probe(ctx context.Context) error {
	ctx, release, err := l.beginDiscovery(ctx)
	if err != nil {
		return err
	}
	defer release()
	manifests, err := l.controller.Probe(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, manifest := range manifests {
		if _, err := l.upsertAgentManifest(ctx, manifest, now); err != nil {
			return err
		}
	}
	return l.repository.Checkpoint(ctx)
}

func (l *Local) DiagnoseProvider(ctx context.Context, provider string) (nodeprotocol.ProviderDiagnostic, error) {
	ctx, release, err := l.beginDiscovery(ctx)
	if err != nil {
		return nodeprotocol.ProviderDiagnostic{}, err
	}
	defer release()
	raw, err := l.ExecuteLocalCommand(ctx, "agent.diagnose", nodeprotocol.DiagnoseProviderPayload{Provider: provider})
	if err != nil {
		return nodeprotocol.ProviderDiagnostic{}, err
	}
	var diagnostic nodeprotocol.ProviderDiagnostic
	if err := json.Unmarshal(raw, &diagnostic); err != nil {
		return nodeprotocol.ProviderDiagnostic{}, fmt.Errorf("decode agent diagnostic: %w", err)
	}
	if diagnostic.Provider != provider {
		return nodeprotocol.ProviderDiagnostic{}, fmt.Errorf("diagnostic provider mismatch: got %q", diagnostic.Provider)
	}
	manifest := nodeprotocol.AgentManifest{
		Provider: diagnostic.Provider, ProviderVersion: diagnostic.ProviderVersion,
		Available: diagnostic.Available, Authenticated: diagnostic.Authenticated,
		Capabilities: diagnostic.Capabilities, CapabilityVersion: diagnostic.CapabilityVersion,
	}
	if _, err := l.upsertAgentManifest(ctx, manifest, diagnostic.FinishedAt); err != nil {
		return nodeprotocol.ProviderDiagnostic{}, err
	}
	if err := l.repository.Checkpoint(ctx); err != nil {
		return nodeprotocol.ProviderDiagnostic{}, err
	}
	return diagnostic, nil
}

// RecycleIdleProvider closes idle embedded Provider instances synchronously.
// It is intentionally separate from run.stop: terminal Runs have already lost
// their run mapping, while active Runs must remain untouched.
func (l *Local) RecycleIdleProvider(ctx context.Context, provider string) (nodeprotocol.ProviderRuntimeRecycleResult, error) {
	recycler, ok := l.controller.(interface {
		RecycleIdleProvider(context.Context, string) (nodeprotocol.ProviderRuntimeRecycleResult, error)
	})
	if !ok {
		return nodeprotocol.ProviderRuntimeRecycleResult{Provider: provider}, &domain.Error{
			Code: "capability_unsupported", Message: "本机 Runtime 不支持 Provider 回收",
			HTTPStatus: 501,
		}
	}
	return recycler.RecycleIdleProvider(ctx, provider)
}

// ProviderExecutables lists the effective CLI path for every Provider. The
// settings API stores the user override; this projection reports what the
// runtime would actually use.
func (l *Local) ProviderExecutables() ([]nodeprotocol.ProviderExecutable, error) {
	lister, ok := l.controller.(interface {
		ProviderExecutables() []nodeprotocol.ProviderExecutable
	})
	if !ok {
		return nil, &domain.Error{
			Code: "capability_unsupported", Message: "本机 Runtime 不支持可执行文件设置",
			HTTPStatus: 501,
		}
	}
	return lister.ProviderExecutables(), nil
}

// SetProviderExecutable applies one Provider's executable path immediately.
// The durable setting is owned by the control plane; this only swaps the
// in-process discovery client and idle instances.
func (l *Local) SetProviderExecutable(
	ctx context.Context, provider, executable string,
) (nodeprotocol.ProviderExecutable, error) {
	ctx, release, err := l.beginDiscovery(ctx)
	if err != nil {
		return nodeprotocol.ProviderExecutable{}, err
	}
	defer release()
	updater, ok := l.controller.(interface {
		SetProviderExecutable(context.Context, string, string) (nodeprotocol.ProviderExecutable, error)
	})
	if !ok {
		return nodeprotocol.ProviderExecutable{}, &domain.Error{
			Code: "capability_unsupported", Message: "本机 Runtime 不支持可执行文件设置",
			HTTPStatus: 501,
		}
	}
	return updater.SetProviderExecutable(ctx, provider, executable)
}

func (l *Local) upsertAgentManifest(
	ctx context.Context, manifest nodeprotocol.AgentManifest, probedAt time.Time,
) (domain.AgentService, error) {
	status := "unavailable"
	loginStatus := "unknown"
	if manifest.Available {
		loginStatus = "required"
	}
	if manifest.Available && manifest.Authenticated {
		status = "ready"
		loginStatus = "authenticated"
	}
	if probedAt.IsZero() {
		probedAt = time.Now().UTC()
	}
	return l.repository.UpsertAgentService(ctx, l.user.ID, domain.AgentService{
		ComputerID: l.computer.ID, Provider: manifest.Provider,
		ProviderVersion: manifest.ProviderVersion, LoginStatus: loginStatus,
		Status: status, Capabilities: manifest.Capabilities,
		CapabilityVersion: manifest.CapabilityVersion, LastProbedAt: &probedAt,
	})
}

func (l *Local) ExecuteLocalCommand(ctx context.Context, name string, input any) (json.RawMessage, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	command := nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: randomID("cmd"), Name: name,
		ComputerID: l.computer.ID, SentAt: now, ExpiresAt: now.Add(time.Minute),
		IdempotencyKey: name + ":" + randomID("idem"), TraceContext: map[string]any{}, Payload: payload,
	}
	result, err := l.controller.Handle(ctx, command)
	if err != nil {
		return nil, err
	}
	if result.Status != "completed" {
		return nil, fmt.Errorf("%s failed: %s", name, result.ErrorCode)
	}
	return result.Result, nil
}

func (l *Local) ListGitRepositories(ctx context.Context, request nodeprotocol.GitRepositoriesRequest) (nodeprotocol.GitRepositoryList, error) {
	reader, ok := l.controller.(embedded.GitRead)
	if !ok {
		return nodeprotocol.GitRepositoryList{}, unsupportedGitRead()
	}
	return reader.ListGitRepositories(ctx, request)
}

func (l *Local) ListGitChanges(ctx context.Context, request nodeprotocol.GitChangesRequest) (nodeprotocol.GitChangesPage, error) {
	reader, ok := l.controller.(embedded.GitRead)
	if !ok {
		return nodeprotocol.GitChangesPage{}, unsupportedGitRead()
	}
	return reader.ListGitChanges(ctx, request)
}

func (l *Local) ReadGitDiff(ctx context.Context, request nodeprotocol.GitDiffRequest) (nodeprotocol.GitDiffResult, error) {
	reader, ok := l.controller.(embedded.GitRead)
	if !ok {
		return nodeprotocol.GitDiffResult{}, unsupportedGitRead()
	}
	return reader.ReadGitDiff(ctx, request)
}

func (l *Local) RefreshGit(ctx context.Context, request nodeprotocol.GitRefreshRequest) (nodeprotocol.GitRefreshResult, error) {
	reader, ok := l.controller.(embedded.GitRead)
	if !ok {
		return nodeprotocol.GitRefreshResult{}, unsupportedGitRead()
	}
	return reader.RefreshGit(ctx, request)
}

func (l *Local) SubscribeGitEvents(ctx context.Context, request nodeprotocol.GitRepositoriesRequest) (<-chan nodeprotocol.GitEvent, func(), error) {
	reader, ok := l.controller.(embedded.GitRead)
	if !ok {
		return nil, nil, unsupportedGitRead()
	}
	return reader.SubscribeGitEvents(ctx, request)
}

func (l *Local) GetRunGitSnapshots(ctx context.Context, runID string) ([]nodeprotocol.GitRunWorkspaceSnapshot, error) {
	reader, ok := l.controller.(interface {
		GetRunGitSnapshots(context.Context, string) ([]nodeprotocol.GitRunWorkspaceSnapshot, error)
	})
	if !ok {
		return nil, unsupportedGitRead()
	}
	return reader.GetRunGitSnapshots(ctx, runID)
}

func unsupportedGitRead() error {
	return &nodeprotocol.GitReadError{
		Code: "capability_unsupported", Message: "当前运行模式不支持项目 Git 只读能力",
	}
}

// StopLocalRun is retained for desktop application callers that want to wait
// for the durable stop request. It never invokes runtime directly: StopRun has
// already inserted run.stop into the outbox and the dispatcher performs the
// delivery with the stable outbox ID.
func (l *Local) StopLocalRun(ctx context.Context, runID string) error {
	l.NotifyIssueActivation("")
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		run, err := l.repository.GetRun(ctx, l.user.ID, runID)
		if err != nil {
			return err
		}
		if domain.IsRunTerminal(run.Status) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l *Local) run(ctx context.Context) {
	defer l.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.wake:
		case <-ticker.C:
		}
		l.process(ctx)
	}
}

func (l *Local) runProbes(ctx context.Context) {
	defer l.wg.Done()
	// The initial probe is also asynchronous: a hung CLI must not delay startup
	// recovery, events, scheduling or stop commands.
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		probeContext, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := l.Probe(probeContext)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("local adapter probe failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (l *Local) beginDiscovery(ctx context.Context) (context.Context, func(), error) {
	l.discoveryMu.Lock()
	if l.closing {
		l.discoveryMu.Unlock()
		return nil, nil, context.Canceled
	}
	l.wg.Add(1)
	l.discoveryMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	cleanup := func() { stop(); cancel(); l.wg.Done() }
	select {
	case <-ctx.Done():
		cleanup()
		return nil, nil, ctx.Err()
	case l.discovery <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-l.discovery
			cleanup()
			return nil, nil, err
		}
		return ctx, func() { <-l.discovery; cleanup() }, nil
	}
}

func (l *Local) process(ctx context.Context) {
	if err := l.flushEvents(ctx); err != nil {
		slog.Error("flush local runtime events", "error", err)
	}
	if _, err := l.repository.AdvanceLocalGoals(ctx, time.Now().UTC()); err != nil {
		slog.Error("advance local goals", "error", err)
	}
	if _, err := l.repository.AdvanceConversationGoals(ctx, time.Now().UTC()); err != nil {
		slog.Error("advance conversation goals", "error", err)
	}
	if _, err := l.repository.TriggerDueLocalSchedules(ctx, time.Now().UTC()); err != nil {
		slog.Error("trigger local schedules", "error", err)
	}
	if work, ok := l.repository.(interface {
		AdvanceIssueWorkItems(context.Context, time.Time) (int, error)
	}); ok {
		if _, err := work.AdvanceIssueWorkItems(ctx, time.Now().UTC()); err != nil {
			slog.Error("advance issue work items", "error", err)
		}
	}
	for i := 0; i < 16; i++ {
		worked, err := l.processMention(ctx)
		if err != nil {
			slog.Error("process local mention", "error", err)
			break
		}
		if !worked {
			break
		}
	}
	for i := 0; i < 16; i++ {
		lease := time.Duration(l.commandLeaseNanos.Load())
		if lease <= 0 {
			lease = time.Minute
		}
		command, ok, err := l.repository.ClaimLocalCommand(
			ctx, time.Now().UTC(), lease, l.inFlightCommandIDs()...,
		)
		if err != nil {
			slog.Error("claim local command", "error", err)
			break
		}
		if !ok {
			break
		}
		l.mu.Lock()
		l.inFlight[command.ID] = struct{}{}
		l.mu.Unlock()
		l.wg.Add(1)
		go l.executeOutboxCommand(ctx, command)
		// Let run.start enter runtime and register its run before a subsequently
		// queued run.stop/approval.decide for the same run is claimed. Long-running
		// execution still happens concurrently with later dispatcher ticks.
		if command.Name == "run.start" {
			break
		}
	}
	if err := l.flushEvents(ctx); err != nil {
		slog.Error("flush local runtime events", "error", err)
	}
}

func (l *Local) processMention(ctx context.Context) (bool, error) {
	claim, ok, err := l.repository.ClaimNextIssueMention(ctx, time.Now().UTC(), 30*time.Second)
	if err != nil || !ok {
		return ok, err
	}
	retry := func(cause error) {
		delay := 200 * time.Millisecond
		for attempt := 1; attempt < claim.Attempts && delay < 30*time.Second; attempt++ {
			delay *= 2
		}
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		_ = l.repository.RetryLocalIssueMention(context.Background(), claim.MentionID,
			claim.ClaimToken, cause.Error(), time.Now().UTC().Add(delay))
	}
	deadLetter := func(cause error) {
		_ = l.repository.DeadLetterLocalIssueMention(context.Background(), claim.MentionID,
			claim.ClaimToken, cause.Error())
	}
	issue, err := l.repository.GetIssue(ctx, claim.UserID, claim.IssueID)
	if err != nil {
		deadLetter(err)
		return true, nil
	}
	comments, truncated, err := l.repository.ListIssuePromptComments(ctx, claim.UserID, claim.IssueID)
	if err != nil {
		retry(err)
		return true, nil
	}
	prompt := buildMentionPrompt(issue, comments)
	// An older queued mention may fall outside the recent history window. Keep
	// the actual activation source independently, rather than silently turning
	// another recent comment into the current instruction.
	trigger, err := l.repository.GetIssueComment(ctx, claim.UserID, claim.CommentID)
	if err != nil || trigger.IssueID != issue.ID {
		if err == nil {
			err = domain.NotFound("评论", claim.CommentID)
		}
		deadLetter(err)
		return true, nil
	}
	prompt += "\n\n## 触发本次委派的评论\ncomment_id: " + trigger.ID + "\n" + clipMentionText(trigger.Body, 64<<10)
	if truncated || len(trigger.Body) > 64<<10 {
		prompt += "\n（上下文已裁剪；需要评论全文时使用 issue_get 的 comment_id 参数精确读取。）"
	}
	_, err = l.repository.CreateRunForClaimedMention(ctx, claim,
		[]domain.ContentBlock{{Type: "text", Text: prompt}}, "mention:"+claim.MentionID)
	if err == nil {
		return true, nil
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		switch domainErr.Code {
		case "target_busy", "agent_instance_paused", "runtime_unavailable", "issue_not_open", "work_item_blocked":
			retry(err)
		default:
			deadLetter(err)
		}
	} else {
		retry(err)
	}
	slog.Warn("deferred local mention", "mention_id", claim.MentionID, "error", err)
	return true, nil
}

func buildMentionPrompt(issue domain.Issue, comments []domain.IssueComment) string {
	var value strings.Builder
	value.WriteString("# 任务（Issue）上下文\n\n## 标题\n")
	value.WriteString(clipMentionText(issue.Title, 4<<10))
	value.WriteString("\n\n## 描述\n")
	if strings.TrimSpace(issue.Description) == "" {
		value.WriteString("（无）")
	} else {
		value.WriteString(clipMentionText(issue.Description, 16<<10))
	}
	value.WriteString("\n\n## 状态\n")
	value.WriteString(issue.Status)
	if issue.WorktreeHint {
		value.WriteString("\n\n如需修改 Git 仓库，请先创建独立 worktree，并将名称与路径写入评论。")
	}
	value.WriteString("\n\n## 评论历史\n")
	start, remaining := len(comments), 32<<10
	for start > 0 {
		size := min(len(comments[start-1].Body), 16<<10) + 256
		if size > remaining {
			break
		}
		remaining -= size
		start--
	}
	for _, comment := range comments[start:] {
		if comment.Status == domain.IssueCommentWorking {
			continue
		}
		value.WriteString("- ")
		if comment.AuthorKind == domain.IssueCommentAuthorAgent {
			value.WriteString("Agent(" + comment.AuthorAgentID + ")")
		} else {
			value.WriteString("用户")
		}
		value.WriteString(" [comment_id=" + comment.ID + "]: " + clipMentionText(comment.Body, 16<<10) + "\n")
	}
	value.WriteString("\n（历史采用有界最近窗口；需要单条评论全文时使用 issue_get 的 comment_id 参数。）\n")
	value.WriteString("\n本轮结束前必须使用 issue_comment_create 工具向本任务发送本轮结果；向其他 Agent 委派时，委派说明即为本轮结果。")
	value.WriteString("\n每个 Agent 会话只维护一条总结评论：系统会自动复用你最近发布的评论并原地更新，不要重复发布；")
	value.WriteString("只有用户明确要求“再发一条”时才使用 append_new=true。")
	value.WriteString(store.IssueAsyncDelegationPrompt)
	return value.String()
}

func clipMentionText(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes-len("…")]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "…"
}

func (l *Local) executeOutboxCommand(ctx context.Context, item store.LocalCommand) {
	defer func() {
		l.mu.Lock()
		delete(l.inFlight, item.ID)
		l.mu.Unlock()
		l.wg.Done()
	}()
	now := time.Now().UTC()
	command := nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: item.ID, Name: item.Name,
		ComputerID: item.ComputerID,
		SentAt:     now, ExpiresAt: item.ExpiresAt, IdempotencyKey: item.IdempotencyKey,
		TraceContext: map[string]any{}, Payload: item.Payload,
	}
	leaseDuration := time.Minute
	if item.ClaimedAt != nil && item.LeaseUntil != nil {
		if claimedLease := item.LeaseUntil.Sub(*item.ClaimedAt); claimedLease > 0 {
			leaseDuration = claimedLease
		}
	}
	handleContext, cancelHandle := context.WithCancel(ctx)
	stopRenewal := make(chan struct{})
	renewalStopped := make(chan struct{})
	renewalError := make(chan error, 1)
	go func() {
		defer close(renewalStopped)
		interval := leaseDuration / 3
		if interval < time.Millisecond {
			interval = time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		if err := l.renewOutboxCommandLease(handleContext, item, leaseDuration, ticker.C, stopRenewal); err != nil {
			renewalError <- err
			cancelHandle()
		}
	}()
	result, err := l.controller.Handle(handleContext, command)
	close(stopRenewal)
	cancelHandle()
	<-renewalStopped
	var leaseErr error
	select {
	case leaseErr = <-renewalError:
	default:
	}
	background, cancelWrite := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWrite()
	if leaseErr != nil {
		_ = l.repository.MarkLocalCommandUncertain(background, item.ID, item.ClaimToken, "lease_renewal_failed")
		return
	}
	if err != nil && ctx.Err() != nil {
		_ = l.repository.MarkLocalCommandUncertain(background, item.ID, item.ClaimToken, "dispatcher_stopped")
		return
	}
	if (result.Status == "failed" || result.ErrorCode != "") && len(result.Result) == 0 && err != nil {
		// Preparation/envelope failures may precede the Runtime command inbox or
		// event sink. Keep their actionable safe error in the fallback as well.
		result.Result, _ = json.Marshal(map[string]any{"error": nodeprotocol.SanitizeDiagnosticText(err.Error())})
	}
	if item.Name == "run.start" || item.Name == "run.stop" || item.Name == "approval.decide" {
		// Handle's durable execution events are authoritative. Import them before
		// a command result can synthesize a generic failed/cancelled terminal;
		// otherwise the real diagnostic arrives only after SSE already closed.
		if flushErr := l.flushEvents(background); flushErr != nil {
			// Do not ACK-before-commit, hide an event gap, or seal a fallback while
			// durable native events are unreadable/unacknowledged. Re-delivery uses
			// the same Runtime MessageID and recovers its result without re-execution.
			_ = l.repository.MarkLocalCommandUncertain(background, item.ID, item.ClaimToken, "event_flush_failed")
			return
		}
	}
	if result.Status == "failed" || (err != nil && result.ErrorCode != "") {
		reason := result.ErrorCode
		if reason == "" {
			reason = "runtime_failed"
		}
		_ = l.repository.FailLocalCommand(background, item.ID, item.ClaimToken, reason, result.Result)
	} else if err != nil || result.Status == "processing" {
		reason := "runtime_retry"
		if err != nil {
			reason = err.Error()
		}
		if item.Attempts >= 10 {
			_ = l.repository.FailLocalCommand(background, item.ID, item.ClaimToken, "runtime_retry_exhausted", result.Result)
		} else {
			_ = l.repository.RetryLocalCommand(background, item.ID, item.ClaimToken, reason, time.Now().UTC().Add(200*time.Millisecond))
		}
	} else if result.Status != "completed" {
		_ = l.repository.FailLocalCommand(background, item.ID, item.ClaimToken, "invalid_command_status", nil)
	} else if completeErr := l.repository.CompleteLocalCommand(background, item.ID, item.ClaimToken, result.Result); completeErr != nil {
		var invalid *domain.Error
		if errors.As(completeErr, &invalid) && invalid.Code == "invalid_argument" {
			_ = l.repository.FailLocalCommand(background, item.ID, item.ClaimToken, "invalid_command_result", nil)
		} else {
			// Re-delivery keeps the same Runtime MessageID and recovers the already
			// persisted result; a control-store write error must not redo the work.
			_ = l.repository.MarkLocalCommandUncertain(background, item.ID, item.ClaimToken, "result_persist_failed")
		}
		slog.Warn("local command result was not committed", "command_id", item.ID, "name", item.Name)
	}
	var payload struct {
		RunID   string `json:"run_id"`
		IssueID string `json:"issue_id"`
	}
	_ = json.Unmarshal(item.Payload, &payload)
	if payload.RunID != "" {
		run, _ := l.repository.LocalRun(background, payload.RunID)
		if payload.IssueID == "" {
			payload.IssueID = run.IssueID
		}
		l.notifyRun(payload.RunID, payload.IssueID)
	}
	l.NotifyIssueActivation(payload.IssueID)
}

func (l *Local) renewOutboxCommandLease(ctx context.Context, item store.LocalCommand, lease time.Duration, ticks <-chan time.Time, stop <-chan struct{}) error {
	for {
		select {
		case <-stop:
			return nil
		case <-ctx.Done():
			return nil
		case <-ticks:
			// A ticker carries its scheduled time, not the time we receive it.
			// After scheduler/SQLite pressure that timestamp can predate expiry;
			// using it would revive a claim we no longer own. Tick is only a
			// wakeup hint: the repository must check ownership at current time.
			if err := l.repository.RenewLocalCommandLease(ctx, item.ID, item.ClaimToken, time.Now().UTC(), lease); err != nil {
				return err
			}
		}
	}
}

func (l *Local) inFlightCommandIDs() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	ids := make([]string, 0, len(l.inFlight))
	for id := range l.inFlight {
		ids = append(ids, id)
	}
	return ids
}

func (l *Local) flushEvents(ctx context.Context) error {
	l.eventFlushMu.Lock()
	defer l.eventFlushMu.Unlock()
	for {
		events, err := l.controller.UnackedEvents(ctx, nodeprotocol.MaxRunEventsPerBatch)
		if err != nil || len(events) == 0 {
			return err
		}
		cursors, err := l.repository.ApplyLocalEvents(ctx, events)
		if err != nil {
			return err
		}
		if len(cursors) == 0 {
			return errors.New("runtime event sequence gap")
		}
		// ApplyLocalEvents has committed its SQLite transaction before returning.
		// A crash here only causes an idempotent re-import; ACK-before-commit is
		// structurally impossible.
		if err := l.controller.AckEvents(ctx, cursors); err != nil {
			return err
		}
		notified := make(map[string]struct{}, len(cursors))
		for _, event := range events {
			if _, exists := notified[event.RunID]; exists {
				continue
			}
			notified[event.RunID] = struct{}{}
			run, err := l.repository.LocalRun(ctx, event.RunID)
			if err != nil {
				slog.Warn("resolve issue for imported run events", "run_id", event.RunID, "error", err)
			}
			l.notifyRun(event.RunID, run.IssueID)
		}
	}
}

func (l *Local) NotifyIssueActivation(issueID string) {
	select {
	case l.wake <- struct{}{}:
	default:
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	for waiter := range l.issueWaiters[issueID] {
		select {
		case waiter <- struct{}{}:
		default:
		}
	}
}

// ActivateIssueMentions materializes queued mention conversations before the
// desktop HTTP response returns. Provider execution still runs asynchronously.
func (l *Local) ActivateIssueMentions(ctx context.Context, _ string) error {
	for index := 0; index < 16; index++ {
		worked, err := l.processMention(ctx)
		if err != nil {
			return err
		}
		if !worked {
			return nil
		}
	}
	return nil
}

func (l *Local) SubscribeRunEvents(runID string) (<-chan struct{}, func()) {
	return l.subscribe(l.runWaiters, runID)
}

func (l *Local) SubscribeIssue(issueID string) (<-chan struct{}, func()) {
	return l.subscribe(l.issueWaiters, issueID)
}

func (l *Local) subscribe(registry map[string]map[chan struct{}]struct{}, id string) (<-chan struct{}, func()) {
	wake := make(chan struct{}, 1)
	l.mu.Lock()
	if registry[id] == nil {
		registry[id] = make(map[chan struct{}]struct{})
	}
	registry[id][wake] = struct{}{}
	l.mu.Unlock()
	return wake, func() {
		l.mu.Lock()
		delete(registry[id], wake)
		if len(registry[id]) == 0 {
			delete(registry, id)
		}
		l.mu.Unlock()
	}
}

func (l *Local) notifyRun(runID, issueID string) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for waiter := range l.runWaiters[runID] {
		select {
		case waiter <- struct{}{}:
		default:
		}
	}
	for waiter := range l.issueWaiters[issueID] {
		select {
		case waiter <- struct{}{}:
		default:
		}
	}
}

func randomID(prefix string) string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	return prefix + "_" + hex.EncodeToString(raw)
}

type localToolCaller struct {
	tools    *toolservice.Service
	computer domain.Computer
	notify   func(string)
}

func (c *localToolCaller) SendToolRequest(ctx context.Context, request nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
	execution := c.tools.Execute(ctx, c.computer.ID, request)
	if execution.ActivationIssueID != "" && c.notify != nil {
		c.notify(execution.ActivationIssueID)
	}
	return execution.Result, nil
}

var _ embedded.ToolCaller = (*localToolCaller)(nil)
var _ GitRead = (*Local)(nil)
