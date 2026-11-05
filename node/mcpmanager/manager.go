// Package mcpmanager manages explicitly started, local-machine MCP connections.
// Effective is a privileged, in-memory-only API; never serialize its result to clients.
package mcpmanager

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	ErrNotFound = errors.New("mcp_not_found")
	ErrConflict = errors.New("mcp_version_conflict")
	ErrInvalid  = errors.New("mcp_invalid_config")
	ErrClosed   = errors.New("mcp_manager_closed")
	ErrStorage  = errors.New("mcp_storage_failed")
)

const KeepSecret = "__TEAMVA_KEEP_SECRET__"

type Config struct{ DataDir string }
type Input struct {
	Name      string          `json:"name"`
	Scope     string          `json:"scope"`
	ProjectID string          `json:"project_id,omitempty"`
	Version   int64           `json:"version"`
	Config    json.RawMessage `json:"config"`
}
type View struct {
	Input
	Status            string                  `json:"status"`
	Error             string                  `json:"error,omitempty"`
	Tools             []*mcp.Tool             `json:"tools"`
	Resources         []*mcp.Resource         `json:"resources"`
	ResourceTemplates []*mcp.ResourceTemplate `json:"resource_templates"`
	Prompts           []*mcp.Prompt           `json:"prompts"`
	ProtocolVersion   string                  `json:"protocol_version,omitempty"`
	Authenticated     bool                    `json:"authenticated"`
	Shadowed          bool                    `json:"shadowed,omitempty"`
}
type entry struct {
	Input
	Token      *storedToken `json:"token,omitempty"`
	tokenMu    sync.Mutex
	status     string
	errorCode  string
	session    *mcp.ClientSession
	cancel     context.CancelFunc
	generation uint64
	auth       *authAttempt
	tools      []*mcp.Tool
	resources  []*mcp.Resource
	templates  []*mcp.ResourceTemplate
	prompts    []*mcp.Prompt
	protocol   string
}
type Manager struct {
	mu      sync.Mutex
	dir     string
	lock    *os.File
	entries map[string]*entry
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
}
type disk struct {
	Format  int      `json:"format"`
	Entries []*entry `json:"entries"`
}

func New(c Config) (*Manager, error) {
	if c.DataDir == "" || !filepath.IsAbs(c.DataDir) {
		return nil, ErrInvalid
	}
	// macOS's OS-owned /var alias is used by os.TempDir. Normalize only this
	// fixed system alias, never EvalSymlinks on a caller-controlled data path.
	if runtime.GOOS == "darwin" && strings.HasPrefix(c.DataDir, "/var/") {
		c.DataDir = "/private" + c.DataDir
	}
	dir := filepath.Join(c.DataDir, "mcp")
	if err := safeDir(dir); err != nil {
		return nil, ErrStorage
	}
	if os.Chmod(dir, 0700) != nil {
		return nil, ErrStorage
	}
	lock, err := lockManagerFile(filepath.Join(dir, "manager.lock"))
	if errors.Is(err, errManagerLocked) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, ErrStorage
	}
	if info, err := lock.Stat(); err != nil || !info.Mode().IsRegular() {
		lock.Close()
		return nil, ErrStorage
	}
	if err = lock.Chmod(0600); err != nil {
		lock.Close()
		return nil, ErrStorage
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{dir: dir, lock: lock, entries: map[string]*entry{}, ctx: ctx, cancel: cancel}
	fail := func() (*Manager, error) { cancel(); lock.Close(); return nil, ErrStorage }
	file, err := openPrivate(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return fail()
	}
	defer file.Close()
	var d disk
	dec := json.NewDecoder(io.LimitReader(file, 32*1024*1024))
	dec.DisallowUnknownFields()
	if dec.Decode(&d) != nil || d.Format != 1 {
		return fail()
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return fail()
	}
	for _, e := range d.Entries {
		if e == nil || validateIdentity(e.Scope, e.ProjectID, e.Name) != nil || e.Version <= 0 || e.Version > maxVersion {
			return fail()
		}
		obj, err := decodeConfig(e.Config)
		if err != nil || validateConfig(obj) != nil {
			return fail()
		}
		if e.Token != nil && (e.Token.Token == nil || e.Token.Token.AccessToken == "" || oauthURL(e.Token.TokenURL) != nil) {
			return fail()
		}
		k := key(e.Scope, e.ProjectID, e.Name)
		if m.entries[k] != nil {
			return fail()
		}
		e.status = baseStatus(obj)
		m.entries[k] = e
	}
	return m, nil
}

func key(scope, project, name string) string { return scope + "\x00" + project + "\x00" + name }
func baseStatus(c map[string]any) string {
	if c["disabled"] == true {
		return "disabled"
	}
	return "disconnected"
}

const maxVersion int64 = (1 << 53) - 1

func newVersion() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random unavailable")
	}
	// Random creation identity prevents practical delete/recreate ABA. Updates
	// monotonically increment, leaving ample headroom below JS's exact limit.
	return int64(binary.LittleEndian.Uint64(b[:])&((1<<48)-1)) | 1
}

func (m *Manager) List(ctx context.Context, projectID string) ([]View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []View{}
	projectNames := map[string]bool{}
	if projectID != "" {
		for _, e := range m.entries {
			if e.Scope == "project" && e.ProjectID == projectID {
				projectNames[e.Name] = true
			}
		}
	}
	for _, e := range m.entries {
		if e.Scope == "user" || (projectID != "" && e.ProjectID == projectID) {
			v := m.viewLocked(e)
			v.Shadowed = e.Scope == "user" && projectNames[e.Name]
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
func (m *Manager) viewLocked(e *entry) View {
	obj, _ := decodeConfig(e.Config)
	maskSecrets(obj)
	raw, _ := json.Marshal(obj)
	v := View{Input: e.Input, Status: e.status, Error: e.errorCode, Tools: e.tools, Resources: e.resources, ResourceTemplates: e.templates, Prompts: e.prompts, ProtocolVersion: e.protocol, Authenticated: e.Token != nil}
	v.Config = raw
	if v.Tools == nil {
		v.Tools = []*mcp.Tool{}
	}
	if v.Resources == nil {
		v.Resources = []*mcp.Resource{}
	}
	if v.ResourceTemplates == nil {
		v.ResourceTemplates = []*mcp.ResourceTemplate{}
	}
	if v.Prompts == nil {
		v.Prompts = []*mcp.Prompt{}
	}
	// Catalog text is untrusted; it must not echo a configured credential either.
	catalogs, _ := json.Marshal(map[string]any{"tools": v.Tools, "resources": v.Resources, "resource_templates": v.ResourceTemplates, "prompts": v.Prompts})
	catalogs = redactKnown(catalogs, e)
	var clean struct {
		Tools     []*mcp.Tool             `json:"tools"`
		Resources []*mcp.Resource         `json:"resources"`
		Templates []*mcp.ResourceTemplate `json:"resource_templates"`
		Prompts   []*mcp.Prompt           `json:"prompts"`
	}
	if json.Unmarshal(catalogs, &clean) == nil {
		v.Tools = clean.Tools
		v.Resources = clean.Resources
		v.ResourceTemplates = clean.Templates
		v.Prompts = clean.Prompts
	}
	return v
}
func (m *Manager) Save(ctx context.Context, in Input) (View, error) {
	if validateIdentity(in.Scope, in.ProjectID, in.Name) != nil || in.Version < 0 {
		return View{}, ErrInvalid
	}
	obj, err := decodeConfig(in.Config)
	if err != nil {
		return View{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return View{}, ErrClosed
	}
	if ctx.Err() != nil {
		return View{}, ctx.Err()
	}
	k := key(in.Scope, in.ProjectID, in.Name)
	old := m.entries[k]
	if (old == nil && in.Version != 0) || (old != nil && (in.Version == 0 || old.Version != in.Version)) {
		return View{}, ErrConflict
	}
	var prev map[string]any
	if old != nil {
		prev, _ = decodeConfig(old.Config)
	}
	if restoreSecrets(obj, prev) != nil || validateConfig(obj) != nil {
		return View{}, ErrInvalid
	}
	in.Config, _ = json.Marshal(obj)
	in.Version = newVersion()
	if old != nil {
		if old.Version >= maxVersion {
			return View{}, ErrConflict
		}
		in.Version = old.Version + 1
	}
	e := &entry{Input: in, status: baseStatus(obj)}
	// Retain grants only when all authentication-bearing identity is unchanged.
	// Display/transport policy and timeout edits still disconnect, not log out.
	if old != nil && sameAuthIdentity(prev, obj) {
		e.Token = old.Token
	}
	m.entries[k] = e
	if m.persistLocked() != nil {
		if old == nil {
			delete(m.entries, k)
		} else {
			m.entries[k] = old
		}
		return View{}, ErrStorage
	}
	if old != nil {
		stopEntry(old)
	}
	return m.viewLocked(e), nil
}
func sameAuthIdentity(a, b map[string]any) bool {
	if a["type"] != "remote" || b["type"] != "remote" {
		return false
	}
	for _, k := range []string{"url", "oauth", "environment", "headers"} {
		if !reflect.DeepEqual(a[k], b[k]) {
			return false
		}
	}
	return true
}
func (m *Manager) Delete(ctx context.Context, scope, projectID, name string, version int64) error {
	if validateIdentity(scope, projectID, name) != nil {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	k := key(scope, projectID, name)
	e := m.entries[k]
	if e == nil {
		return ErrNotFound
	}
	if version <= 0 || version != e.Version {
		return ErrConflict
	}
	delete(m.entries, k)
	if m.persistLocked() != nil {
		m.entries[k] = e
		return ErrStorage
	}
	stopEntry(e)
	return nil
}
func (m *Manager) Effective(ctx context.Context, projectID string) (map[string]json.RawMessage, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if ctx.Err() != nil {
		m.mu.Unlock()
		return nil, ctx.Err()
	}
	selected := map[string]*entry{}
	for _, scope := range []string{"user", "project"} {
		for _, e := range m.entries {
			if e.Scope == scope && (scope == "user" || (projectID != "" && e.ProjectID == projectID)) {
				selected[e.Name] = e
			}
		}
	}
	m.mu.Unlock()
	out := map[string]json.RawMessage{}
	for name, e := range selected {
		m.mu.Lock()
		if m.closed || m.entries[key(e.Scope, e.ProjectID, e.Name)] != e {
			m.mu.Unlock()
			return nil, ErrConflict
		}
		c, _ := decodeConfig(e.Config)
		st := e.Token
		m.mu.Unlock()
		if st != nil && c["type"] == "remote" && c["disabled"] != true {
			token, err := st.source(ctx, m, key(e.Scope, e.ProjectID, e.Name), e).Token()
			if err != nil {
				return nil, err
			}
			headers, _ := c["headers"].(map[string]any)
			if headers == nil {
				headers = map[string]any{}
			}
			for h := range headers {
				if strings.EqualFold(h, "Authorization") {
					delete(headers, h)
				}
			}
			headers["Authorization"] = "Bearer " + token.AccessToken
			c["headers"] = headers
			c["oauth"] = false
		}
		m.mu.Lock()
		valid := !m.closed && m.entries[key(e.Scope, e.ProjectID, e.Name)] == e
		if st != nil && c["disabled"] != true {
			valid = valid && e.Token != nil
		}
		m.mu.Unlock()
		if !valid {
			return nil, ErrConflict
		}
		out[name], _ = json.Marshal(c)
	}
	return out, nil
}
func (m *Manager) Disconnect(ctx context.Context, scope, projectID, name string) (View, error) {
	if validateIdentity(scope, projectID, name) != nil {
		return View{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return View{}, ErrClosed
	}
	if ctx.Err() != nil {
		return View{}, ctx.Err()
	}
	e := m.entries[key(scope, projectID, name)]
	if e == nil {
		return View{}, ErrNotFound
	}
	stopEntry(e)
	obj, _ := decodeConfig(e.Config)
	e.status = baseStatus(obj)
	e.errorCode = ""
	return m.viewLocked(e), nil
}
func stopEntry(e *entry) {
	e.generation++
	if e.auth != nil {
		e.auth.stop()
		e.auth = nil
	}
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
	if e.session != nil {
		_ = e.session.Close()
		e.session = nil
	}
	e.tools = nil
	e.resources = nil
	e.templates = nil
	e.prompts = nil
	e.protocol = ""
}
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	m.cancel()
	for _, e := range m.entries {
		stopEntry(e)
	}
	return closeManagerLock(m.lock)
}

func safeDir(path string) error {
	// Lstat every component: MkdirAll alone follows attacker-supplied symlinks.
	if path == filepath.Dir(path) {
		return nil
	}
	if err := safeDir(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.Mkdir(path, 0700)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrStorage
	}
	return nil
}

func (m *Manager) persistLocked() error {
	if safeDir(m.dir) != nil {
		return ErrStorage
	}
	info, err := os.Lstat(m.dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrStorage
	}
	if err = os.Chmod(m.dir, 0700); err != nil {
		return ErrStorage
	}
	target := filepath.Join(m.dir, "config.json")
	if i, e := os.Lstat(target); e == nil && (!i.Mode().IsRegular() || i.Mode()&os.ModeSymlink != 0) {
		return ErrStorage
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return ErrStorage
	}
	entries := make([]*entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return key(entries[i].Scope, entries[i].ProjectID, entries[i].Name) < key(entries[j].Scope, entries[j].ProjectID, entries[j].Name)
	})
	b, err := json.Marshal(disk{Format: 1, Entries: entries})
	if err != nil {
		return ErrStorage
	}
	f, err := os.CreateTemp(m.dir, ".config-*")
	if err != nil {
		return ErrStorage
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, target)
	}
	if err != nil {
		return ErrStorage
	}
	return syncDir(m.dir)
}
