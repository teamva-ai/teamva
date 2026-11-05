package codex

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/teamva-ai/teamva/node/provider"
)

func Definition(metadata provider.Metadata) provider.Definition {
	return provider.Definition{Metadata: metadata,
		NewProbe:    func(executable string) (provider.Adapter, error) { return New(executable), nil },
		NewInstance: newInstance, Configure: WriteConfig,
		NewOutputProjector: func(int) provider.OutputProjector { return outputProjector{} },
		TerminalStatus: func(payload map[string]any) (string, bool) {
			turn, ok := payload["turn"].(map[string]any)
			if !ok {
				return "", false
			}
			status, present := turn["status"].(string)
			return status, present
		},
		FileChange: func(eventType string, payload map[string]any) (provider.FileChange, bool) {
			item, _ := payload["item"].(map[string]any)
			if eventType != "provider.item" || item["type"] != "fileChange" {
				return provider.FileChange{}, false
			}
			turnID := provider.FirstString(item, "turnId", "turn_id")
			if turnID == "" {
				turnID = provider.FirstString(payload, "turnId", "turn_id")
			}
			return provider.FileChange{Source: "provider.item.fileChange", ItemID: provider.FirstString(item, "id", "itemId", "item_id"),
				TurnID: turnID, Owner: item, OwnerKey: "item", AllowStatusOnly: true}, true
		},
	}
}

func newInstance(options provider.InstanceOptions) (provider.Adapter, error) {
	if err := WriteConfig(options.ConfigDir, options.Config); err != nil {
		return nil, fmt.Errorf("write codex config: %w", err)
	}
	// CODEX_HOME owns native sessions and SQLite databases as well as config.
	// Keep it durable, and link only the host-generated, rebuildable entries.
	// Direct factory callers without DataDir retain their explicit ConfigDir
	// home; no existing home or native data is copied, moved or removed.
	dataDir := options.DataDir
	if dataDir == "" {
		dataDir = options.ConfigDir
	} else {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return nil, errors.New("prepare codex data home failed")
		}
		info, err := os.Lstat(dataDir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("codex data home must be a private real directory")
		}
		for _, name := range []string{"config.toml", "AGENTS.md", "skills"} {
			if err := linkInstanceConfig(options.ConfigDir, dataDir, name); err != nil {
				return nil, err
			}
		}
	}
	client := New(options.Executable).WithConfigDir(dataDir)
	client.readOnly = options.Config.ReadOnly
	client.disableTools = options.Config.DisableTools
	client.systemPrompt = options.Config.SystemPrompt
	client.writableRepositories = provider.CanonicalWritableRepositories(options.Config.WritableRepositories)
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.New("resolve codex authentication home failed")
	}
	if err == nil {
		client.protectedPaths, err = codexProtectedPaths(options.ConfigDir, dataDir, filepath.Join(home, ".codex", "auth.json"), filepath.Join(dataDir, "auth.json"))
		if err != nil {
			return nil, err
		}
		if _, err = codexPrivateCarveouts(client.protectedPaths, client.writableRepositories); err != nil {
			return nil, err
		}
		err = provider.LinkAuth(filepath.Join(home, ".codex", "auth.json"), filepath.Join(dataDir, "auth.json"))
	}
	if err != nil && options.Logger != nil {
		options.Logger.Printf("codex auth link unavailable")
	}
	return client, nil
}

func linkInstanceConfig(configDir, dataDir, name string) error {
	source, err := filepath.Abs(filepath.Join(configDir, name))
	if err != nil {
		return errors.New("resolve codex instance config failed")
	}
	target := filepath.Join(dataDir, name)
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			linked, err := os.Readlink(target)
			if err == nil && linked == source {
				return nil // The same link remains valid after config recreation.
			}
		}
		// Never overwrite an existing native/user entry, including a planted
		// link to some other scope. Only links created for this exact config fit.
		return fmt.Errorf("codex data home contains an unmanaged %s entry", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect codex instance %s link failed", name)
	}
	if err := os.Symlink(source, target); err != nil {
		return fmt.Errorf("link codex instance %s failed", name)
	}
	return nil
}

func WriteConfig(configDir string, config provider.InstanceConfig) error {
	if err := os.MkdirAll(filepath.Join(configDir, "skills"), 0o700); err != nil {
		return err
	}
	// Rewriting an empty prompt must also clear the prior instance prompt.
	if err := os.WriteFile(filepath.Join(configDir, "AGENTS.md"), []byte("# Agent Instructions\n\n"+config.SystemPrompt+"\n"), 0o600); err != nil {
		return err
	}
	var cfg bytes.Buffer
	// No Run is active while loading config. Fail closed instead of granting
	// implicit CWD/temp writes; each thread AND turn replaces its exact roots.
	cfg.WriteString("default_permissions = \":read-only\"\n\n[permissions.teamva.filesystem]\n\":root\" = \"read\"\n\n[permissions.teamva.filesystem.\":workspace_roots\"]\n\".\" = \"write\"\n\".git\" = \"write\"\n\n[permissions.teamva.network]\nenabled = false\n")
	if err := writeMCPConfig(&cfg, config); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(configDir, "config.toml"), cfg.Bytes(), 0o600)
}
