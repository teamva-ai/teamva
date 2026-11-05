package claude

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/node/provider"
)

const mcpServerName = "teamva"
const maxToolBytes = 1 << 20

type toolScope struct {
	roots              []string
	writableRoots      []string
	cwd                string
	protected          []string
	readOnly, disabled bool
	ipc                string
	ipcTools           map[string]bool
	ipcRoot            *os.Root
	ipcPin             *pinnedDirectory
	pins               map[string]*pinnedDirectory
}

var bridgeReadTools = map[string]bool{"issue_get": true, "issue_agents_list": true, "issue_agent_status": true, "issue_agent_context": true,
	"work_records_query": true, "work_record_get": true, "work_history_read": true, "inspiration_list": true, "inspiration_get": true}
var bridgeWriteTools = map[string]bool{"issue_comment_create": true, "issue_comment_update_own": true}

func newToolScope(c *Client, config provider.InstanceConfig, request provider.RunRequest) (*toolScope, error) {
	s := &toolScope{readOnly: config.ReadOnly || request.ReadOnly, disabled: config.DisableTools || request.DisableTools, ipcTools: map[string]bool{}, pins: map[string]*pinnedDirectory{}}
	s.protected = []string{c.configDir, c.dataDir}
	if home, err := os.UserHomeDir(); err == nil {
		for _, name := range []string{".claude", ".codex", ".config/opencode", ".local/share/opencode"} {
			s.protected = append(s.protected, filepath.Join(home, name))
		}
	}
	if s.disabled {
		return s, nil
	}
	ready := false
	defer func() {
		if !ready {
			s.close()
		}
	}()
	for i, path := range s.protected {
		normalized, err := absoluteScopePath(path)
		if err != nil {
			return nil, err
		}
		s.protected[i] = normalized
	}
	for _, dir := range append([]string{request.CWD}, config.WritableRepositories...) {
		root, err := absoluteScopePath(dir)
		if err != nil {
			return nil, errors.New("claude workspace root is invalid")
		}
		if s.pins[root] == nil {
			pin, err := pinDirectory(root)
			if err != nil {
				return nil, err
			}
			s.pins[root] = pin
		}
		s.roots = append(s.roots, root)
		if s.cwd == "" {
			s.cwd = root
		} else {
			s.writableRoots = append(s.writableRoots, root)
		}
	}
	sort.Slice(s.roots, func(i, j int) bool { return len(s.roots[i]) > len(s.roots[j]) })
	if request.ToolRequestDir != "" {
		var err error
		s.ipc, err = absoluteScopePath(request.ToolRequestDir)
		if err != nil {
			return nil, err
		}
		pin := s.pins[s.ipc]
		if pin == nil {
			pin, err = pinDirectory(s.ipc)
			if err != nil {
				return nil, err
			}
			s.pins[s.ipc] = pin
		}
		s.ipcPin = pin
		s.ipcRoot = pin.root
		s.protected = append(s.protected, s.ipc)
		candidates := map[string]bool{}
		for name := range bridgeReadTools {
			candidates[name] = true
		}
		for name := range bridgeWriteTools {
			candidates[name] = true
		}
		for name := range candidates {
			info, err := s.ipcRoot.Lstat(name)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			// A ReadOnly scope never acquires business-writing tools, even if a
			// malformed caller gives it an Issue bridge instead of read IPC.
			if bridgeReadTools[name] || (!s.readOnly && bridgeWriteTools[name]) {
				s.ipcTools[name] = true
			}
		}
	}
	ready = true
	return s, nil
}
func (s *toolScope) close() {
	for _, pin := range s.pins {
		pin.close()
	}
	s.pins = nil
}
func containsPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func sensitivePath(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		lower := strings.ToLower(segment)
		if lower == ".teamva" || lower == ".git" || lower == ".claude" || lower == ".ssh" || lower == ".aws" || lower == ".env" || strings.HasPrefix(lower, ".env.") || lower == "auth.json" || lower == "credentials.json" {
			return true
		}
	}
	return false
}
func (s *toolScope) resolve(path string, write bool) (*os.Root, string, error) {
	if s.disabled || (write && s.readOnly) || path == "" {
		return nil, "", errors.New("tool access denied")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.cwd, path)
	}
	var err error
	path, err = absoluteScopePath(path)
	if err != nil {
		return nil, "", err
	}
	if write {
		allowed := false
		for _, root := range s.writableRoots {
			if containsPath(root, path) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, "", errors.New("path is outside explicitly writable repositories")
		}
	}
	for _, protected := range s.protected {
		if containsPath(protected, path) {
			return nil, "", errors.New("private provider path denied")
		}
	}
	for _, root := range s.roots {
		if !containsPath(root, path) {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		if sensitivePath(rel) {
			return nil, "", errors.New("private workspace path denied")
		}
		// Root guards against symlink races and escape. Also reject in-root
		// symlinks: otherwise a harmless name could alias private .git/IPC.
		dir, err := s.pins[root].clone()
		if err != nil {
			return nil, "", errors.New("open authorized workspace failed")
		}
		parts := strings.Split(rel, string(filepath.Separator))
		for _, part := range parts[:len(parts)-1] {
			next, err := openChildRoot(dir, part)
			_ = dir.Close()
			if err != nil {
				return nil, "", errors.New("workspace ancestor is unavailable or is a symlink")
			}
			dir = next
		}
		leaf := parts[len(parts)-1]
		info, err := dir.Lstat(leaf)
		if err != nil && !(write && errors.Is(err, os.ErrNotExist)) {
			dir.Close()
			return nil, "", errors.New("workspace path is unavailable")
		}
		if info != nil && info.Mode()&os.ModeSymlink != 0 {
			dir.Close()
			return nil, "", errors.New("workspace symlink access denied")
		}
		return dir, leaf, nil
	}
	return nil, "", errors.New("path is outside authorized workspace")
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func (s *toolScope) tools() []any {
	tools := []any{}
	if s.disabled {
		return tools
	}
	add := func(name, description string, schema map[string]any, readOnly bool) {
		tools = append(tools, map[string]any{"name": name, "description": description, "inputSchema": schema, "annotations": map[string]any{"readOnlyHint": readOnly, "openWorldHint": false}})
	}
	pathSchema := objectSchema(map[string]any{"path": map[string]any{"type": "string", "description": "Absolute path or path relative to the current working directory."}}, "path")
	add("read_file", "Read a bounded UTF-8 workspace file. Secret/private paths and symlinks are denied.", pathSchema, true)
	add("list_directory", "List one authorized workspace directory (not recursive).", pathSchema, true)
	if !s.readOnly {
		add("write_file", "Atomically create or replace a workspace text file; no shell, secret/private paths or symlinks.", objectSchema(map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, "path", "content"), false)
	}
	names := make([]string, 0, len(s.ipcTools))
	for name := range s.ipcTools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		add(name, "Teamva host tool. Pass exactly the JSON arguments documented in the Issue/work-history instructions. This calls the real FileBridge and returns its receipt; use issue_comment_create for a public summary (assistant text is NOT a comment).", map[string]any{"type": "object", "additionalProperties": true}, bridgeReadTools[name])
	}
	return tools
}
func (s *toolScope) permits(name string) bool {
	if s.disabled {
		return false
	}
	return name == "read_file" || name == "list_directory" || (name == "write_file" && !s.readOnly) || s.ipcTools[name]
}
func (s *toolScope) call(ctx context.Context, name string, args map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !s.permits(name) {
		return "", errors.New("tool is not in the authorized catalog")
	}
	if s.ipcTools[name] {
		return s.callBridge(ctx, name, args)
	}
	path, _ := args["path"].(string)
	root, rel, err := s.resolve(path, name == "write_file")
	if err != nil {
		return "", err
	}
	defer root.Close()
	switch name {
	case "read_file":
		before, err := root.Lstat(rel)
		if err != nil || !before.Mode().IsRegular() || !singleLink(before) || before.Size() > maxToolBytes {
			return "", errors.New("workspace file is not a bounded single-link regular file")
		}
		file, err := root.OpenFile(rel, noFollowReadFlags(false), 0)
		if err != nil {
			return "", errors.New("open workspace file failed")
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || !singleLink(info) || info.Size() > maxToolBytes {
			return "", errors.New("workspace file is not a bounded single-link regular file")
		}
		data, err := io.ReadAll(io.LimitReader(file, maxToolBytes+1))
		if err != nil || len(data) > maxToolBytes {
			return "", errors.New("read workspace file failed")
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !utf8.Valid(data) {
			return "", errors.New("workspace file must contain valid UTF-8 text")
		}
		return string(data), nil
	case "list_directory":
		file, err := root.OpenFile(rel, noFollowReadFlags(true), 0)
		if err != nil {
			return "", errors.New("open workspace directory failed")
		}
		defer file.Close()
		entries, err := file.ReadDir(1001)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", errors.New("list workspace directory failed")
		}
		if len(entries) > 1000 {
			return "", errors.New("directory has too many entries; choose a smaller directory")
		}
		names := []string{}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink == 0 && !sensitivePath(entry.Name()) {
				n := entry.Name()
				if entry.IsDir() {
					n += "/"
				}
				names = append(names, n)
			}
		}
		sort.Strings(names)
		return strings.Join(names, "\n"), nil
	case "write_file":
		content, ok := args["content"].(string)
		if !ok || len(content) > maxToolBytes || !utf8.ValidString(content) {
			return "", errors.New("write requires bounded text content")
		}
		tmp := filepath.Join(filepath.Dir(rel), ".teamva-write-"+randomID())
		file, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return "", errors.New("prepare workspace write failed")
		}
		defer root.Remove(tmp)
		if info, e := root.Stat(rel); e == nil && info.Mode().IsRegular() {
			_ = file.Chmod(info.Mode().Perm())
		}
		_, err = file.WriteString(content)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return "", errors.New("write workspace file failed")
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err = root.Rename(tmp, rel); err != nil {
			return "", errors.New("replace workspace file failed")
		}
		return "File saved.", nil
	}
	return "", errors.New("unknown tool")
}

func (s *toolScope) callBridge(ctx context.Context, name string, args map[string]any) (string, error) {
	if s.ipcRoot == nil || !s.ipcTools[name] {
		return "", errors.New("host tool denied")
	}
	if err := s.ipcPin.validate(); err != nil {
		return "", err
	}
	// OpenRoot + no symlink to the tool directory retains the host's exact IPC
	// scope. No caller supplies a directory, request filename, or response path.
	info, err := s.ipcRoot.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("host tool directory unavailable")
	}
	dir, err := openChildRoot(s.ipcRoot, name)
	if err != nil {
		return "", errors.New("open host tool directory failed")
	}
	defer dir.Close()
	data, err := json.Marshal(args)
	if err != nil || len(data) > maxToolBytes {
		return "", errors.New("host tool arguments too large")
	}
	request := "req-" + randomID() + ".json"
	// Publish atomically: FileBridge must never consume a partially written JSON.
	tmp := "." + request + ".tmp"
	f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", errors.New("prepare host tool request failed")
	}
	defer dir.Remove(tmp)
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return "", errors.New("write host tool request failed")
	}
	if err = dir.Rename(tmp, request); err != nil {
		return "", errors.New("publish host tool request failed")
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if info, err := dir.Lstat(request + ".response.json"); err == nil && (!info.Mode().IsRegular() || !singleLink(info)) {
			return "", errors.New("unsafe host tool response")
		}
		response, err := dir.OpenFile(request+".response.json", noFollowReadFlags(false), 0)
		if err == nil {
			info, statErr := response.Stat()
			if statErr != nil || !info.Mode().IsRegular() || !singleLink(info) {
				response.Close()
				return "", errors.New("unsafe host tool response")
			}
			data, readErr := io.ReadAll(io.LimitReader(response, 8<<20+1))
			response.Close()
			if readErr != nil || len(data) > 8<<20 {
				return "", errors.New("host tool response too large")
			}
			if !utf8.Valid(data) || !json.Valid(data) {
				return "", errors.New("host tool response is not valid UTF-8 JSON")
			}
			return string(data), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("read host tool response failed")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeout.C:
			return "", errors.New("host tool receipt timed out; do not blindly resubmit")
		case <-ticker.C:
		}
	}
}

func randomID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("secure randomness unavailable")
	}
	return hex.EncodeToString(value[:])
}

func (s *toolScope) mcp(ctx context.Context, message map[string]any) map[string]any {
	id, present := message["id"]
	if !present {
		return map[string]any{"jsonrpc": "2.0", "result": map[string]any{}}
	}
	response := map[string]any{"jsonrpc": "2.0", "id": id}
	params, _ := message["params"].(map[string]any)
	switch message["method"] {
	case "initialize":
		version, _ := params["protocolVersion"].(string)
		if version == "" {
			version = "2024-11-05"
		}
		response["result"] = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "teamva", "version": "1.0.0"}}
	case "ping":
		response["result"] = map[string]any{}
	case "tools/list":
		response["result"] = map[string]any{"tools": s.tools()}
	case "tools/call":
		name, _ := params["name"].(string)
		args, _ := params["arguments"].(map[string]any)
		text, err := s.call(ctx, name, args)
		if err != nil {
			text = err.Error()
		}
		response["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": err != nil}
	default:
		response["error"] = map[string]any{"code": -32601, "message": fmt.Sprintf("Unsupported MCP method %v", message["method"])}
	}
	return response
}
