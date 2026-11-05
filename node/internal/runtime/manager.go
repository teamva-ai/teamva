package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/catalog"
)

// InstanceKey 标识一个独立运行实例：Agent 在某 Issue 中的实例 + 配置世代 +
// 项目 + 规范化的项目工作区授权范围。任一边界变化都不能复用
// Provider/native session 或配置目录。
// 不同 key 之间完全并行（独立 provider 进程）；同一 key 内串行执行。
type InstanceKey struct {
	InstanceID   string
	Generation   string
	ProjectID    string
	GitScopeHash string
}

// Manager 按 (issue_agent_instance, generation, project, workspace scope) 懒启动/回收独立的 provider
// runtime。每个实例拥有自己的配置目录（skills + system prompt + MCP tools），
// 与任务目录解耦。
// ToolBrokerProvider 为运行中的实例提供工具通道。
type ToolBrokerProvider func(runID, issueID, instanceID, agentID string) *ToolBroker

type Manager struct {
	provider   string
	executable string

	rootDir string // ~/.teamva/agents/<hashed instance/generation/project/repository/scope>
	store   localStore

	mu          sync.Mutex
	retentionMu sync.Mutex
	instances   map[InstanceKey]*managedInstance
	runs        map[string]InstanceKey
	closingKeys map[InstanceKey]chan struct{}
	idleAfter   time.Duration
	logger      *log.Logger
	brokers     ToolBrokerProvider
	closed      bool
	closing     sync.WaitGroup
	closingNow  int
	shutdownErr error
	closeDone   chan struct{}
	closeErr    error
	// testAdapter 用于单元测试注入 fake adapter（跳过真实 provider 启动）。
	testAdapter adapter.Adapter
	retention   configRetentionPolicy
}

const (
	maxManagedInstances = 64
	maxManagedRuns      = 256
)

// RegisterTestAdapter 注入测试用 adapter（仅测试）。
func (m *Manager) RegisterTestAdapter(item adapter.Adapter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.testAdapter = item
}

// SetExecutable replaces the CLI path used by instances created after this
// call. Existing instances keep their process; the Runtime recycles idle
// instances so a later Run starts from the new binary.
func (m *Manager) SetExecutable(executable string) {
	m.mu.Lock()
	m.executable = executable
	m.mu.Unlock()
}

// SetToolBrokerProvider 设置工具代理工厂（run.start 时由 runtime 注入）。
func (m *Manager) SetToolBrokerProvider(provider ToolBrokerProvider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.brokers = provider
}

// localStore 抽象 node 本地持久化（native session 保存/恢复）。
type localStore interface {
	SaveNativeSession(ctx context.Context, runID, provider, sessionID string) error
	GetConversationSession(ctx context.Context, conversationID, provider string) (string, bool, error)
	SaveConversationSession(ctx context.Context, conversationID, provider, sessionID string) error
}

type managedInstance struct {
	key        InstanceKey
	configDir  string
	configOnly bool
	session    string
	lastUsed   time.Time
	adapter    adapter.Adapter
	activeRuns int
	idleTimer  *time.Timer
	// Private in-memory fingerprint only. MCP values are deliberately excluded
	// from InstanceConfig JSON and must never reach the retention manifest.
	configDigest [sha256.Size]byte
	needsRestart bool
}

func (m *Manager) registerRunLocked(runID string, key InstanceKey, instance *managedInstance) {
	if previous, exists := m.runs[runID]; exists {
		if previous == key {
			return
		}
		if old := m.instances[previous]; old != nil && old.activeRuns > 0 {
			old.activeRuns--
		}
	}
	if instance.idleTimer != nil {
		instance.idleTimer.Stop()
		instance.idleTimer = nil
	}
	m.runs[runID] = key
	instance.activeRuns++
	instance.lastUsed = time.Now()
	m.logger.Printf("manager register run=%s instance=%s generation=%s", runID, key.InstanceID, key.Generation)
}

// ReleaseRun 在 run.start 返回时解除 run → 实例映射。最后一个活动 Run
// 结束后启动一次延迟回收；新的 Run 会停止该定时器。
func (m *Manager) ReleaseRun(runID string) {
	m.mu.Lock()
	key, ok := m.runs[runID]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.runs, runID)
	instance := m.instances[key]
	if instance == nil {
		m.mu.Unlock()
		return
	}
	if instance.activeRuns > 0 {
		instance.activeRuns--
	}
	instance.lastUsed = time.Now()
	if instance.activeRuns == 0 {
		if instance.idleTimer != nil {
			instance.idleTimer.Stop()
		}
		instance.idleTimer = time.AfterFunc(m.idleAfter, func() {
			m.RecycleIdle(context.Background())
		})
	}
	m.mu.Unlock()
}

// RecycleIdleNow synchronously closes every currently idle Provider instance,
// regardless of the normal reuse grace period. It is used by explicit local
// diagnostics and acceptance cleanup; active Runs are never interrupted.
func (m *Manager) RecycleIdleNow(ctx context.Context) (recycled, active, remaining int, result error) {
	recycled, active, remaining, result = m.recycle(ctx, true)
	return
}

// AdapterForRun 返回承载该 run 的实例 adapter（不存在时返回 nil）。
func (m *Manager) AdapterForRun(runID string) adapter.Adapter {
	m.mu.Lock()
	defer m.mu.Unlock()
	key, ok := m.runs[runID]
	if !ok {
		return nil
	}
	if instance := m.instances[key]; instance != nil {
		return instance.adapter
	}
	return nil
}

// InstanceCount 返回当前活跃实例数（调试用）。
func (m *Manager) InstanceCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.instances)
}

// NewManager 创建按实例管理的 runtime manager。
// provider is a discovered ID; executable is its configured CLI path.
func NewManager(provider, executable, rootDir string, store localStore, logger *log.Logger) *Manager {
	return &Manager{
		provider: provider, executable: executable, rootDir: rootDir,
		store: store, instances: make(map[InstanceKey]*managedInstance),
		runs: make(map[string]InstanceKey), closingKeys: make(map[InstanceKey]chan struct{}),
		idleAfter: 10 * time.Minute, logger: logger,
		retention: defaultConfigRetentionPolicy(), closeDone: make(chan struct{}),
	}
}

func (m *Manager) instanceDir(key InstanceKey) string {
	// One length-delimited key digest avoids both sanitize collisions and deeply
	// nested Windows paths while still binding every project/repository/scope
	// component into the private provider configuration directory.
	return filepath.Join(m.rootDir, "instances", keyDirectorySegment(instanceDirectoryKey(m.provider, generationSessionKey(key))))
}

func (m *Manager) instanceDataDir(key InstanceKey) string {
	return filepath.Join(m.rootDir, "provider-data", keyDirectorySegment(instanceDirectoryKey(m.provider, generationSessionKey(key))))
}

func (m *Manager) prepareInstanceDataDir(key InstanceKey) (string, error) {
	if err := os.MkdirAll(m.rootDir, 0o700); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(m.rootDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	// Only the hashed host scope chooses the native data home. Do not follow
	// pre-planted data-root/scope symlinks or inspect a user's default home.
	name := keyDirectorySegment(instanceDirectoryKey(m.provider, generationSessionKey(key)))
	for _, path := range []string{"provider-data", filepath.Join("provider-data", name)} {
		if err := root.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		info, err := root.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("unsafe provider data directory")
		}
	}
	return m.instanceDataDir(key), nil
}

func instanceDirectoryKey(provider, sessionKey string) string {
	return fmt.Sprintf("provider:%d:%s:session:%d:%s", len(provider), provider, len(sessionKey), sessionKey)
}

func keyDirectorySegment(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest[:])
}

func sanitize(value string) string {
	return adapter.SafePathSegment(value)
}

// Get 返回（或懒启动）指定实例的 adapter，并在返回前原子建立 run → 实例
// 映射。每次 Run 执行前调用。
func (m *Manager) Get(ctx context.Context, runID string, key InstanceKey, config InstanceConfig) (adapter.Adapter, string, error) {
	if config.ReadOnly || config.DisableTools {
		config.MCPServers = nil
	}
	if config.DisableTools {
		config.ComputerUse = false
	}
	digest, err := instanceConfigDigest(config)
	if err != nil {
		return nil, "", err
	}
	m.retentionMu.Lock()
	defer m.retentionMu.Unlock()
	for {
		m.mu.Lock()
		closing := m.closingKeys[key]
		if closing == nil {
			break
		}
		m.mu.Unlock()
		select {
		case <-closing:
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	if m.closed {
		m.mu.Unlock()
		return nil, "", errors.New("runtime manager is closed")
	}
	if _, exists := m.runs[runID]; !exists && len(m.runs) >= maxManagedRuns {
		m.mu.Unlock()
		return nil, "", errors.New("runtime manager active run limit reached")
	}
	existing, ok := m.instances[key]
	if ok {
		if existing.activeRuns > 0 {
			if existing.configDigest != digest {
				m.mu.Unlock()
				return nil, "", errors.New("runtime instance is busy; configuration applies to the next run")
			}
			// Do not rewrite private config files underneath an active Run.
			m.registerRunLocked(runID, key, existing)
			item, session := existing.adapter, existing.session
			m.mu.Unlock()
			return item, session, nil
		}
		// The workspace authorization set is part of key.GitScopeHash. Reuse is
		// therefore safe, but Provider config is still regenerated so mutable
		// instructions and per-Agent settings never remain stale.
		if err := m.writeProviderConfig(existing.configDir, config); err != nil {
			m.mu.Unlock()
			return nil, "", fmt.Errorf("refresh provider config: %w", err)
		}
		if err := refreshAdapterConfig(existing.adapter, existing.configDir, config); err != nil {
			m.mu.Unlock()
			return nil, "", fmt.Errorf("refresh adapter config: %w", err)
		}
		if existing.configDigest != digest || existing.needsRestart {
			// Native servers cache config/tool catalogs. Rewriting their files is
			// insufficient: replace only an idle process, retaining the same data
			// home and native session. This is provider-independent factory logic.
			existing.needsRestart = true
			if err := existing.adapter.Close(); err != nil {
				m.mu.Unlock()
				return nil, "", errors.New("close idle provider for configuration refresh failed")
			}
			item, err := m.newAdapter(key, existing.configDir, config)
			if err != nil {
				m.mu.Unlock()
				return nil, "", err
			}
			existing.adapter = item
			existing.configDigest = digest
			existing.needsRestart = false
		}
		m.registerRunLocked(runID, key, existing)
		session := existing.session
		instance := existing
		m.mu.Unlock()
		return instance.adapter, session, nil
	}
	if len(m.instances)+m.closingNow >= maxManagedInstances {
		m.mu.Unlock()
		return nil, "", errors.New("runtime manager instance limit reached")
	}

	// Keep creation under the manager lock. Besides bounding the instance map,
	// this prevents concurrent Get calls for the same key from racing while
	// clearing and rewriting the shared private config directory.
	configDir := m.instanceDir(key)
	_, inspectErr := os.Lstat(configDir)
	createdDirectory := errors.Is(inspectErr, os.ErrNotExist)
	if inspectErr != nil && !createdDirectory {
		m.mu.Unlock()
		return nil, "", fmt.Errorf("inspect instance config dir: %w", inspectErr)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		m.mu.Unlock()
		return nil, "", fmt.Errorf("create instance config dir: %w", err)
	}
	// A directory lacking the separated-data marker may already contain native
	// files from an earlier/direct factory. Never reclassify or clean that data.
	configOnly := createdDirectory
	if !createdDirectory {
		if manifest, err := readInstanceManifest(configDir); err == nil &&
			manifest.Provider == m.provider && manifest.SessionKey == generationSessionKey(key) {
			configOnly = manifest.ConfigOnly
		}
	}
	// 写入该实例的 provider 配置（system prompt / skills / MCP tools）。
	if err := m.writeProviderConfig(configDir, config); err != nil {
		if createdDirectory {
			_ = os.RemoveAll(configDir)
		}
		m.mu.Unlock()
		return nil, "", fmt.Errorf("write provider config: %w", err)
	}
	item, err := m.newAdapter(key, configDir, config)
	if err != nil {
		if createdDirectory {
			_ = os.RemoveAll(configDir)
		}
		m.mu.Unlock()
		return nil, "", err
	}
	if err := writeInstanceManifest(configDir, instanceManifest{
		Version: 1, Provider: m.provider, SessionKey: generationSessionKey(key),
		UpdatedAt: time.Now().UTC(), SessionPersisted: false, ConfigOnly: configOnly,
	}); err != nil {
		_ = item.Close()
		if createdDirectory {
			_ = os.RemoveAll(configDir)
		}
		m.mu.Unlock()
		return nil, "", fmt.Errorf("write instance retention manifest: %w", err)
	}
	instance := &managedInstance{
		key: key, configDir: configDir, configOnly: configOnly, adapter: item, lastUsed: time.Now(),
		configDigest: digest,
	}
	m.instances[key] = instance
	m.registerRunLocked(runID, key, instance)
	m.mu.Unlock()
	m.logger.Printf("runtime manager started provider=%s instance=%s generation=%s", m.provider, key.InstanceID, key.Generation)
	return instance.adapter, "", nil
}

func instanceConfigDigest(config InstanceConfig) ([sha256.Size]byte, error) {
	// An explicit private envelope is needed because InstanceConfig.MCPServers
	// is json:"-". The payload is hashed in memory and never persisted/logged.
	payload, err := json.Marshal(struct {
		Config InstanceConfig
		MCP    map[string]json.RawMessage
	}{config, config.MCPServers})
	if err != nil {
		return [sha256.Size]byte{}, errors.New("invalid private instance configuration")
	}
	return sha256.Sum256(payload), nil
}

func refreshAdapterConfig(item adapter.Adapter, configDir string, config InstanceConfig) error {
	if definition, ok := catalog.Lookup(item.Name()); ok {
		return definition.Configure(configDir, config)
	}
	return nil // Test-injected adapters do not own a source configuration factory.
}

// MarkSessionPersisted records the commit boundary after both native and
// generation session rows have reached SQLite. This permits pruning rebuildable
// config only: an instance's Provider-owned data is retained independently.
// Failure leaves the directory conservatively ineligible for config pruning.
func (m *Manager) MarkSessionPersisted(key InstanceKey) error {
	m.retentionMu.Lock()
	defer m.retentionMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	instance := m.instances[key]
	if instance == nil || instance.activeRuns <= 0 {
		return errors.New("runtime instance is not active")
	}
	return writeInstanceManifest(instance.configDir, instanceManifest{
		Version: 1, Provider: m.provider, SessionKey: generationSessionKey(key),
		UpdatedAt: time.Now().UTC(), SessionPersisted: true, ConfigOnly: instance.configOnly,
	})
}

// InstanceConfig 是一次 Run 的实例配置快照。
type InstanceConfig = adapter.InstanceConfig

// writeProviderConfig 把 Agent 绑定的完整 Skill 制品复制进实例私有配置目录。
// 复制前校验内容摘要，避免数据库绑定与本机文件漂移。
func (m *Manager) writeProviderConfig(configDir string, config InstanceConfig) error {
	if len(config.Skills) == 0 {
		return nil
	}
	for _, skill := range config.Skills {
		skillDir := filepath.Join(configDir, "skills", sanitize(skill.Slug))
		if err := os.RemoveAll(skillDir); err != nil {
			return fmt.Errorf("clear instance Skill %s: %w", skill.Slug, err)
		}
		installedDir := filepath.Join(m.rootDir, "..", "skills", sanitize(skill.Slug))
		if info, err := inspectSkillTree(installedDir); err == nil && len(info.skillFiles) > 0 {
			if skill.ContentDigest != "" && skill.ContentDigest != info.digest {
				return fmt.Errorf("installed Skill %s digest mismatch: got %s, want %s", skill.Slug, info.digest, skill.ContentDigest)
			}
			if err := os.MkdirAll(skillDir, 0o700); err != nil {
				return err
			}
			if err := copySkillTreeContents(installedDir, skillDir); err != nil {
				return fmt.Errorf("copy installed Skill %s: %w", skill.Slug, err)
			}
			continue
		}
		return fmt.Errorf("installed Skill %s is missing; reinstall it from Local Skills", skill.Slug)
	}
	return nil
}

// newAdapter 由子类实现：根据 provider 创建带独立配置的 adapter。
func (m *Manager) newAdapter(key InstanceKey, configDir string, config InstanceConfig) (adapter.Adapter, error) {
	dataDir, err := m.prepareInstanceDataDir(key)
	if err != nil {
		return nil, fmt.Errorf("prepare provider data directory: %w", err)
	}
	if m.testAdapter != nil {
		return m.testAdapter, nil
	}
	definition, ok := catalog.Lookup(m.provider)
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q", m.provider)
	}
	item, err := definition.NewInstance(adapter.InstanceOptions{
		Executable: m.executable, ConfigDir: configDir, DataDir: dataDir,
		CacheDir: filepath.Join(m.rootDir, "provider-cache", m.provider),
		Config:   config, Logger: m.logger,
	})
	if err == nil && (item == nil || item.Name() != m.provider) {
		err = fmt.Errorf("provider %q returned an invalid instance", m.provider)
	}
	if err != nil {
		if item != nil {
			_ = item.Close()
		}
		return nil, err
	}
	return item, nil
}

// RecycleIdle 回收空闲超过 idleAfter 的实例（释放进程与内存）。
func (m *Manager) RecycleIdle(ctx context.Context) {
	_, _, _, err := m.recycle(ctx, false)
	if err != nil {
		m.logger.Printf("runtime manager idle recycle failed provider=%s", m.provider)
	}
}

func (m *Manager) recycle(ctx context.Context, force bool) (recycled, active, remaining int, result error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	m.mu.Lock()
	if m.closed {
		remaining = m.closingNow
		result = m.shutdownErr
		m.mu.Unlock()
		return 0, 0, remaining, result
	}
	var expired []InstanceKey
	for key, instance := range m.instances {
		if instance.activeRuns == 0 && (force || now.Sub(instance.lastUsed) >= m.idleAfter) {
			expired = append(expired, key)
		}
	}
	instances := make([]*managedInstance, 0, len(expired))
	for _, key := range expired {
		instance := m.instances[key]
		delete(m.instances, key)
		m.closingKeys[key] = make(chan struct{})
		if instance.idleTimer != nil {
			instance.idleTimer.Stop()
			instance.idleTimer = nil
		}
		instances = append(instances, instance)
	}
	m.closing.Add(len(instances))
	m.closingNow += len(instances)
	m.mu.Unlock()
	closeErr := m.closeInstances(expired, instances)
	for index := range instances {
		m.logger.Printf("runtime manager recycled provider=%s instance=%s", m.provider, expired[index].InstanceID)
	}
	result = errors.Join(result, closeErr)
	if err := m.pruneConfigDirs(ctx); err != nil {
		m.logger.Printf("runtime manager config retention failed provider=%s", m.provider)
		result = errors.Join(result, err)
	}
	m.mu.Lock()
	result = errors.Join(result, m.shutdownErr)
	remaining = len(m.instances) + m.closingNow
	for _, instance := range m.instances {
		if instance.activeRuns > 0 {
			active++
		}
	}
	m.mu.Unlock()
	return len(instances), active, remaining, result
}

func (m *Manager) closeInstances(keys []InstanceKey, instances []*managedInstance) error {
	if len(keys) != len(instances) {
		return errors.New("provider instance close set is inconsistent")
	}
	results := make(chan error, len(instances))
	for index, instance := range instances {
		key := keys[index]
		go func() {
			err := instance.adapter.Close()
			m.mu.Lock()
			if closed := m.closingKeys[key]; closed != nil {
				delete(m.closingKeys, key)
				close(closed)
			}
			m.closingNow--
			if m.closingNow < 0 {
				m.closingNow = 0
				err = errors.Join(err, errors.New("provider instance close accounting underflow"))
			}
			m.mu.Unlock()
			m.closing.Done()
			if err != nil {
				results <- fmt.Errorf("close provider instance %s: %w", key.InstanceID, err)
				return
			}
			results <- nil
		}()
	}
	var result error
	for range instances {
		result = errors.Join(result, <-results)
	}
	m.mu.Lock()
	if result != nil {
		m.shutdownErr = errors.Join(m.shutdownErr, result)
	}
	m.mu.Unlock()
	return result
}

// Close 关闭全部实例。
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		<-done
		m.mu.Lock()
		err := m.closeErr
		m.mu.Unlock()
		return err
	}
	m.closed = true
	instances := m.instances
	m.instances = make(map[InstanceKey]*managedInstance)
	m.runs = make(map[string]InstanceKey)
	for _, instance := range instances {
		if instance.idleTimer != nil {
			instance.idleTimer.Stop()
		}
	}
	keys := make([]InstanceKey, 0, len(instances))
	items := make([]*managedInstance, 0, len(instances))
	for key, instance := range instances {
		keys = append(keys, key)
		items = append(items, instance)
		m.closingKeys[key] = make(chan struct{})
	}
	m.closing.Add(len(items))
	m.closingNow += len(items)
	m.mu.Unlock()
	result := m.closeInstances(keys, items)
	m.closing.Wait()
	m.mu.Lock()
	result = errors.Join(result, m.shutdownErr)
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.pruneConfigDirs(ctx); err != nil {
		m.logger.Printf("runtime manager config retention failed provider=%s", m.provider)
		result = errors.Join(result, err)
	}
	m.mu.Lock()
	m.closeErr = result
	close(m.closeDone)
	m.mu.Unlock()
	return result
}

// MarshalJSON 便于调试输出。
func (m *Manager) MarshalJSON() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.instances))
	for key := range m.instances {
		keys = append(keys, key.InstanceID+"/"+key.Generation+"/"+key.ProjectID+"/"+key.GitScopeHash)
	}
	return json.Marshal(map[string]any{"provider": m.provider, "active_instances": keys})
}
