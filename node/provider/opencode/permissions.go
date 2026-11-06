package opencode

import (
	"context"
	_ "embed"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
)

//go:embed permission-guard/index.js
var permissionGuardSource []byte

const permissionGuardID = "teamva.permission-boundary"

// V2 wildcards have no literal escape for '*'/'?' and normalize backslashes.
// Reject unrepresentable roots instead of silently turning a directory name into
// an authorization pattern. Error messages must not disclose private paths.
func permissionDirectory(value string) (string, error) {
	if !filepath.IsAbs(value) || strings.ContainsAny(filepath.ToSlash(value), "*?\\") {
		return "", errors.New("invalid permission directory")
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(value))
	if err != nil || strings.ContainsAny(filepath.ToSlash(canonical), "*?\\") {
		return "", errors.New("invalid permission directory")
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() || filepath.Dir(canonical) == canonical {
		return "", errors.New("invalid permission directory")
	}
	return canonical, nil
}

func writablePermissionRoots(values []string) ([]string, error) {
	unique := make(map[string]bool, len(values))
	for _, value := range values {
		root, err := permissionDirectory(value)
		if err != nil {
			return nil, errors.New("opencode writable repository must be a safe real directory")
		}
		unique[root] = true
	}
	roots := make([]string, 0, len(unique))
	for root := range unique {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots, nil
}

// permissionRules uses the V2 Permission.Ruleset (action/resource/effect), not
// V1's permission map. Native file tools check Location-relative resources even
// outside the Location when they are still in a larger project worktree. Merely
// denying external_directory therefore does not constrain a nested Location.
func (c *Client) permissionRules(request provider.RunRequest) []map[string]string {
	rule := func(action, resource, effect string) map[string]string {
		return map[string]string{"action": action, "resource": resource, "effect": effect}
	}
	if request.DisableTools || c.disableTools {
		// V2.0.18 Tool.snapshot wholly-disables every native/direct/Code Mode
		// tool, including the synthesized execute tool, with this final rule.
		// Return before ALL read/Skill/IPC/Computer Use exceptions.
		return []map[string]string{rule("*", "*", "deny")}
	}
	rules := []map[string]string{
		rule("edit", "*", "deny"),
		rule("external_directory", "*", "deny"),
		// Never grant shell based only on the V2 best-effort command scanner.
		// A normal Run enables it below only after a real OS sandbox is active.
		rule("shell", "*", "deny"),
		rule("subagent", "*", "deny"),
	}
	readOnly := request.ReadOnly || c.readOnly
	c.mu.Lock()
	sandboxReady := c.sandboxReady
	c.mu.Unlock()
	if sandboxReady && !readOnly {
		rules = append(rules, rule("shell", "*", "allow"), rule("subagent", "*", "allow"))
	}
	if readOnly {
		// Also constrain a writable instance resumed as read-only: its already
		// loaded custom MCP servers must not retain business-writing actions.
		// Explicit Computer Use is a separate host capability, not an edit grant.
		rules = append(rules, rule("*", "*", "deny"))
		for _, action := range []string{"read", "glob", "grep", "skill", "execute"} {
			rules = append(rules, rule(action, "*", "allow"))
		}
		// The assistant's only MCP server is the run-scoped work tool host,
		// which exposes read-only tools only. Native Code Mode filters the
		// namespace by a server-level action, so allow the teamva-issue prefix;
		// the blanket deny above would otherwise remove it from the catalog.
		rules = append(rules, rule("teamva-issue_*", "*", "allow"))
		// A read-only assistant still needs a real shell for FileBridge heredocs.
		// Only a running OS sandbox with the exact host IPC may enable it: the
		// profile grants IPC/native data, NEVER the assistant's business/CWD roots.
		if _, err := permissionDirectory(request.ToolRequestDir); sandboxReady && err == nil {
			rules = append(rules, rule("shell", "*", "allow"))
		}
		rules = append(rules, rule("read", "*.env", "ask"), rule("read", "*.env.*", "ask"),
			rule("read", "*.env.example", "allow"))
		if c.computerUse {
			rules = append(rules, rule("open_computer_use_*", "*", "allow"))
		}
	}
	// Even a read-only Skill load is an explicit host-bound capability, not an
	// invitation to discover global/native Skills. IDs are safe path segments.
	rules = append(rules, rule("skill", "*", "deny"))
	for _, binding := range c.boundSkills {
		rules = append(rules, rule("skill", binding.ID, "allow"))
	}
	cwd, err := permissionDirectory(request.CWD)
	if err != nil {
		return rules // Invalid direct callers cannot acquire a write exception.
	}
	roots := []string{}
	if !readOnly {
		roots = append(roots, c.writableRoots...)
	}
	if request.ToolRequestDir != "" {
		if ipc, err := permissionDirectory(request.ToolRequestDir); err == nil {
			roots = append(roots, ipc)
		}
	}
	// First allow normalized relative paths. An ancestor grant requires a
	// relative '*' plus a deny for the first traversal outside that ancestor.
	// The blanket absolute deny keeps '*' from granting external paths.
	ancestorDepth := -1
	for _, root := range roots {
		rel, err := filepath.Rel(root, cwd)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			depth := 0
			if rel != "." {
				depth = len(strings.Split(rel, string(filepath.Separator)))
			}
			if depth > ancestorDepth {
				ancestorDepth = depth
			}
		}
	}
	if ancestorDepth >= 0 {
		rules = append(rules, rule("edit", "*", "allow"), rule("edit", "/*", "deny"),
			rule("edit", "?:/*", "deny"),
			rule("edit", strings.Repeat("../", ancestorDepth+1)+"*", "deny"))
	}
	for _, root := range roots {
		rel, err := filepath.Rel(cwd, root)
		if err == nil && rel != "." && rel != ".." && !isAncestorRelative(rel) {
			rules = append(rules, rule("edit", filepath.ToSlash(rel)+"/*", "allow"))
		}
		rules = append(rules, rule("edit", filepath.ToSlash(root)+"/*", "allow"),
			rule("external_directory", filepath.ToSlash(root)+"/*", "allow"))
	}
	// Even when CWD itself is writable, its IPC siblings belong to other Runs.
	// Never restore the old blanket .teamva/tool-requests/* exception.
	transport := filepath.Join(cwd, ".teamva", "tool-requests")
	rules = append(rules, rule("edit", ".teamva/tool-requests/*", "deny"),
		rule("edit", filepath.ToSlash(transport)+"/*", "deny"))
	if ipc, err := permissionDirectory(request.ToolRequestDir); err == nil {
		if rel, err := filepath.Rel(cwd, ipc); err == nil && rel != "." && !isAncestorRelative(rel) {
			rules = append(rules, rule("edit", filepath.ToSlash(rel)+"/*", "allow"))
		}
		rules = append(rules, rule("edit", filepath.ToSlash(ipc)+"/*", "allow"))
	}
	// Host configuration and bound Skills are always read-only, even if a
	// caller also supplied an encompassing writable repository.
	protected := append([]string{c.configDir, c.dataDir}, c.readOnlySkillDirs...)
	for _, value := range protected {
		if value == "" {
			continue
		}
		dir, err := permissionDirectory(value)
		if err != nil {
			// An unverifiable private directory must not disappear from the
			// protection set and inherit a broader repository's write grant.
			rules = append(rules, rule("edit", "*", "deny"))
			continue
		}
		rules = append(rules, rule("edit", filepath.ToSlash(dir)+"/*", "deny"))
		if rel, err := filepath.Rel(cwd, dir); err == nil {
			if rel == "." || isAncestorRelative(rel) {
				// Managed workspaces must not be inside private config/data.
				rules = append(rules, rule("edit", "*", "deny"))
			} else {
				rules = append(rules, rule("edit", filepath.ToSlash(rel)+"/*", "deny"))
			}
		}
	}
	for _, value := range c.readOnlySkillDirs {
		if dir, err := permissionDirectory(value); err == nil {
			rules = append(rules, rule("read", filepath.ToSlash(dir)+"/*", "allow"),
				rule("external_directory", filepath.ToSlash(dir)+"/*", "allow"))
		}
	}
	authPaths, err := c.protectedAuthPaths()
	if err != nil {
		return append(rules, rule("*", "*", "deny"))
	}
	for _, path := range authPaths {
		rules = append(rules, rule("edit", filepath.ToSlash(path), "deny"))
		if rel, err := filepath.Rel(cwd, path); err == nil {
			rules = append(rules, rule("edit", filepath.ToSlash(rel), "deny"))
		}
	}
	return rules
}

func isAncestorRelative(value string) bool {
	for value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		if value == ".." {
			return true
		}
		value = strings.TrimPrefix(value, ".."+string(filepath.Separator))
	}
	return false
}

func (c *Client) verifyPermissionGuard(ctx context.Context, cwd string) error {
	var result struct {
		Data []struct {
			ID    string `json:"id"`
			State struct {
				Status string `json:"status"`
			} `json:"state"`
		} `json:"data"`
	}
	if c.configDir == "" {
		return errors.New("opencode runs require managed permission configuration")
	}
	// A cold V2 Location activates plugins asynchronously; listing plugins does
	// not await activation. Never mistake an early empty inventory for readiness.
	ctx, cancel := context.WithTimeout(ctx, serverStartTimeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := c.getJSON(ctx, "/api/plugin?location[directory]="+url.QueryEscape(cwd), &result); err != nil {
			return errors.New("opencode permission guard verification failed")
		}
		for _, plugin := range result.Data {
			if plugin.ID == permissionGuardID {
				if plugin.State.Status == "active" {
					return nil
				}
				return errors.New("opencode permission guard is not active")
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("opencode permission guard is not active")
		case <-ticker.C:
		}
	}
}
