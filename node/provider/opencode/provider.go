package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/teamva-ai/teamva/node/provider"
)

func Definition(metadata provider.Metadata) provider.Definition {
	return provider.Definition{Metadata: metadata,
		NewProbe:    newModelAwareProbe,
		NewInstance: newInstance, Configure: WriteConfig,
		ModelSetup:         func() provider.ModelSetup { return modelSetup{} },
		NewOutputProjector: newOutputProjector,
	}
}

func newModelAwareProbe(executable string) (provider.Adapter, error) {
	client, err := New(executable)
	if err != nil {
		return nil, err
	}
	client.probeOnly = true
	// Probe and execution must see the same supported key/catalog projection.
	// Never let discovery open/migrate the user's native DB or advertise OAuth
	// models that an isolated execution instance cannot authenticate.
	dir, err := os.MkdirTemp("", "teamva-opencode-probe-")
	if err != nil {
		return nil, modelSetupError("model_provider_storage_failed")
	}
	if err := WriteConfig(dir, provider.InstanceConfig{}); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	client.WithConfigDir(dir)
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, modelSetupError("model_provider_storage_failed")
	}
	client.WithDataDir(dataDir)
	client.ownedConfigDir = dir
	return client, nil
}

func newInstance(options provider.InstanceOptions) (provider.Adapter, error) {
	client, err := New(options.Executable)
	if err != nil {
		return nil, err
	}
	client.WithConfigDir(options.ConfigDir)
	roots, err := writablePermissionRoots(options.Config.WritableRepositories)
	if err != nil {
		return nil, err
	}
	client.writableRoots = roots
	client.readOnly = options.Config.ReadOnly
	client.disableTools = options.Config.DisableTools
	client.systemPrompt = options.Config.SystemPrompt
	client.computerUse = options.Config.ComputerUse && !options.Config.DisableTools
	sources, err := accountAuthSourcePaths()
	if err != nil {
		return nil, err
	}
	client.authSourcePaths = sources
	bindings, err := configuredSkillBindings(options.ConfigDir, options.Config)
	if err != nil {
		return nil, err
	}
	client.boundSkills = bindings
	dirs := make([]string, 0, len(options.Config.Skills))
	for _, skill := range options.Config.Skills {
		dirs = append(dirs, filepath.Join(options.ConfigDir, "skills", provider.SafePathSegment(skill.Slug)))
	}
	client.WithReadOnlySkillDirs(dirs...)
	if err := WriteConfig(options.ConfigDir, options.Config); err != nil {
		return nil, fmt.Errorf("write opencode config: %w", err)
	}
	// Native databases live outside rebuildable instance config. The fallback
	// is only for callers constructing a factory directly without a Manager;
	// existing directories are never migrated or copied into the new home.
	dataDir := options.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(options.ConfigDir, "data")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, errors.New("prepare opencode data home failed")
	}
	info, err := os.Lstat(dataDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("opencode data home must be a private real directory")
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "opencode"), 0o700); err != nil {
		return nil, fmt.Errorf("prepare opencode data dir: %w", err)
	}
	info, err = os.Lstat(filepath.Join(dataDir, "opencode"))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("opencode native data directory must be a real directory")
	}
	client.WithDataDir(dataDir)
	if source := authSource(); source != "" {
		err = provider.LinkAuth(source, filepath.Join(dataDir, "opencode", "auth.json"))
	} else {
		err = os.ErrNotExist
	}
	if err != nil && options.Logger != nil {
		options.Logger.Printf("opencode auth link unavailable")
	}
	return client, nil
}

// configHome returns $XDG_CONFIG_HOME or ~/.config.
func configHome() (string, error) {
	if configured := os.Getenv("XDG_CONFIG_HOME"); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", fmt.Errorf("XDG_CONFIG_HOME must be absolute")
		}
		return configured, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// dataHome returns $XDG_DATA_HOME or ~/.local/share.
func dataHome() (string, error) {
	if configured := os.Getenv("XDG_DATA_HOME"); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", fmt.Errorf("XDG_DATA_HOME must be absolute")
		}
		return configured, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}

// authSource locates the user's OpenCode credentials. OpenCode keeps them in
// the data directory; older layouts wrote them next to the config.
func authSource() string {
	homes := make([]string, 0, 2)
	for _, lookup := range []func() (string, error){dataHome, configHome} {
		if home, err := lookup(); err == nil {
			homes = append(homes, home)
		}
	}
	for _, home := range homes {
		candidate := filepath.Join(home, "opencode", "auth.json")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// userConfig loads the user's global OpenCode configuration so custom
// providers and models stay available inside the isolated instance config.
func userConfig() (map[string]any, error) {
	home, err := configHome()
	if err != nil {
		return nil, fmt.Errorf("locate global OpenCode configuration: %w", err)
	}
	var result map[string]any
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		payload, err := os.ReadFile(filepath.Join(home, "opencode", name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			// Filesystem errors can contain private paths. Only report the fixed
			// config filename, never the path or contents of the user's file.
			return nil, fmt.Errorf("read global OpenCode %s: configuration is not readable", name)
		}
		parsed, err := parseJSONCConfig(payload)
		if err != nil {
			return nil, fmt.Errorf("parse global OpenCode %s: %w", name, err)
		}
		if result == nil {
			result = parsed
		} else {
			// V2 loads both documents in this order: JSONC overlays JSON.
			mergeConfigObjects(result, parsed)
		}
	}
	return result, nil
}

func mergeConfigObjects(target, overlay map[string]any) {
	for key, value := range overlay {
		previous, previousObject := target[key].(map[string]any)
		next, nextObject := value.(map[string]any)
		if previousObject && nextObject {
			mergeConfigObjects(previous, next)
		} else {
			target[key] = value
		}
	}
}

// authCredentials reads only the legacy JSON store. WriteConfig overlays the
// selected V2 keys separately; neither path imports OAuth credentials.
func authCredentials() map[string]string {
	for _, lookup := range []func() (string, error){dataHome, configHome} {
		home, err := lookup()
		if err != nil {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(home, "opencode", "auth.json"))
		if err != nil {
			continue
		}
		var parsed map[string]struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		}
		if json.Unmarshal(payload, &parsed) != nil {
			continue
		}
		keys := make(map[string]string, len(parsed))
		for id, entry := range parsed {
			if entry.Type == "api" && entry.Key != "" {
				keys[id] = entry.Key
			}
		}
		if len(keys) > 0 {
			return keys
		}
	}
	return nil
}

// injectAuthKeys merges API keys into the config's provider options. Provider
// sections that already carry a key (for example a custom provider from the
// user's config) are left untouched.
func injectAuthKeys(cfg map[string]any, nativeKeys map[string]string) {
	keys := authCredentials()
	if keys == nil {
		keys = map[string]string{}
	}
	for id, key := range nativeKeys {
		if key == "" {
			delete(keys, id)
		} else {
			keys[id] = key
		}
	}
	if len(keys) == 0 {
		return
	}
	providers, _ := cfg["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
		cfg["providers"] = providers
	}
	for id, key := range keys {
		// Explicit canonical/legacy provider credentials always beat account
		// imports. Do not turn an explicit empty key into another account.
		explicit := false
		for _, section := range []string{"providers", "provider"} {
			entries, _ := cfg[section].(map[string]any)
			entry, _ := entries[id].(map[string]any)
			for _, field := range []string{"settings", "options"} {
				settings, _ := entry[field].(map[string]any)
				if _, exists := settings["apiKey"]; exists {
					explicit = true
				}
			}
		}
		if explicit {
			continue
		}
		entry, _ := providers[id].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
			providers[id] = entry
		}
		settings, _ := entry["settings"].(map[string]any)
		if settings == nil {
			settings = map[string]any{}
			entry["settings"] = settings
		}
		settings["apiKey"] = key
	}
}

// WriteConfig writes the instance configuration where OpenCode reads it
// ($XDG_CONFIG_HOME/opencode/opencode.json) and carries over the user's own
// provider definitions so locally configured providers keep working.
func WriteConfig(configDir string, config provider.InstanceConfig) error {
	bindings, err := configuredSkillBindings(configDir, config)
	if err != nil {
		return err
	}
	user, err := userConfig()
	if err != nil {
		return err
	}
	promptFile := filepath.Join(configDir, "instructions.md")
	if config.SystemPrompt != "" {
		if err := os.WriteFile(promptFile, []byte(config.SystemPrompt), 0o600); err != nil {
			return err
		}
	}
	cfg := map[string]any{"$schema": "https://opencode.ai/config.json"}
	// Ordinary coding retains native formatter/LSP/snapshot behavior: every
	// spawned child now inherits the real OS write sandbox. Read-only assistants
	// do not need those automatic coding side effects.
	if config.ReadOnly || config.DisableTools {
		cfg["formatter"] = false
		cfg["lsp"] = false
		cfg["snapshots"] = false
	}
	cfg["update"] = "disable"
	guardDir := filepath.Join(configDir, "permission-guard")
	if err := os.MkdirAll(guardDir, 0o700); err != nil {
		return errors.New("prepare opencode permission guard failed")
	}
	if err := os.WriteFile(filepath.Join(guardDir, "index.js"), permissionGuardSource, 0o600); err != nil {
		return errors.New("write opencode permission guard failed")
	}
	// Config.instructions is accepted by the pinned V2.0.18 normalizer but is
	// not consumed by its instruction plugin. It is never a system override.
	// Agent.system is consumed by baseTranscript and REPLACES the coding prompt.
	if config.SystemPrompt != "" {
		cfg["agents"] = map[string]any{"build": map[string]any{"system": config.SystemPrompt}}
	}
	additional := []string{}
	if config.WorkspaceInstructions != "" {
		content, err := os.ReadFile(config.WorkspaceInstructions)
		if err != nil {
			return errors.New("read host workspace instructions failed")
		}
		additional = append(additional, string(content))
	}
	if config.DisableTools {
		cfg["permissions"] = []map[string]string{{"action": "*", "resource": "*", "effect": "deny"}}
		// No tool may load a Skill in this mode. Inject only the host-bound,
		// digest-verified SKILL.md text; accompanying scripts gain no capability.
		for _, binding := range bindings {
			content, err := os.ReadFile(binding.Path)
			if err != nil {
				return errors.New("read bound Skill instructions failed")
			}
			additional = append(additional, string(content))
		}
	}
	cfg["plugins"] = []any{map[string]any{"package": guardDir, "options": map[string]any{"disableTools": config.DisableTools, "instructions": additional, "boundSkills": bindings}}}
	if user != nil {
		// Older layouts wrote `provider`, newer ones normalize to `providers`;
		// keep whichever the user's file uses so custom providers survive the
		// instance.
		for _, key := range []string{"provider", "providers", "model"} {
			if value, ok := user[key]; ok {
				cfg[key] = value
			}
		}
	}
	account, err := readNativeAccount()
	if err != nil {
		return err
	}
	injectAuthKeys(cfg, account.keys)
	if err := mergeManagedModelProviders(cfg); err != nil {
		return err
	}
	// Never scan stale siblings under skills/. Native auto-discovery and built-in
	// Skills are independently removed by the managed skill registry transform.
	sources := make([]string, 0, len(config.Skills))
	for _, skill := range config.Skills {
		root, err := permissionDirectory(filepath.Join(configDir, "skills", provider.SafePathSegment(skill.Slug)))
		if err != nil {
			return errors.New("invalid opencode bound Skill source")
		}
		sources = append(sources, root)
	}
	cfg["skills"] = sources
	servers := map[string]any{}
	if !config.ReadOnly && !config.DisableTools {
		for name, entry := range config.MCPServers {
			if name == "open-computer-use" {
				continue // Reserved even when Computer Use is disabled.
			}
			if !json.Valid(entry) {
				return errors.New("invalid managed MCP server configuration")
			}
			servers[name] = entry
		}
	}
	if config.ComputerUse && !config.DisableTools {
		servers["open-computer-use"] = map[string]any{"type": "local", "command": []string{"open-computer-use", "mcp"}}
	}
	if len(servers) > 0 {
		cfg["mcp"] = map[string]any{"servers": servers}
	}
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(configDir, "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if len(account.catalog) > 0 {
		if err := writePrivateNativeFile(dir, nativeCatalogFilename, account.catalog); err != nil {
			return err
		}
	} else if err := os.Remove(filepath.Join(dir, nativeCatalogFilename)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("remove stale opencode model metadata failed")
	}
	return writePrivateNativeFile(dir, "opencode.json", payload)
}
