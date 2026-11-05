// Package claude implements Claude Code's official bidirectional stream-json
// protocol. Native tools are deliberately absent: only host-implemented SDK MCP
// tools can access the workspace, and none starts a credential-inheriting child.
package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/teamva-ai/teamva/node/provider"
)

func Definition(metadata provider.Metadata) provider.Definition {
	return provider.Definition{Metadata: metadata, NewProbe: newProbe, NewInstance: newInstance,
		Configure: WriteConfig, NewOutputProjector: func(int) provider.OutputProjector { return outputProjector{} }}
}

func privateDirectory(path string) error {
	if path == "" {
		return errors.New("claude private directory is required")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return errors.New("prepare claude private directory failed")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("claude private directory must be a real directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return errors.New("protect claude private directory failed")
	}
	return nil
}

// Configure never imports user settings, hooks, permissions, plugins or MCP.
// Credentials remain in memory and the CLI's private environment, not this file.
func WriteConfig(configDir string, config provider.InstanceConfig) error {
	if len(config.Skills) != 0 || config.ComputerUse || len(config.MCPServers) != 0 {
		return provider.ErrUnsupported
	}
	if err := privateDirectory(configDir); err != nil {
		return err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return errors.New("encode claude instance config failed")
	}
	path := filepath.Join(configDir, "instance.json")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("claude instance config is not a regular file")
	}
	tmp, err := os.CreateTemp(configDir, ".instance-*")
	if err != nil {
		return errors.New("prepare claude instance config failed")
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return errors.New("write claude instance config failed")
	}
	if err = tmp.Close(); err != nil {
		return errors.New("close claude instance config failed")
	}
	if err = os.Rename(name, path); err != nil {
		return errors.New("replace claude instance config failed")
	}
	return nil
}

func newProbe(executable string) (provider.Adapter, error) {
	dir, err := os.MkdirTemp("", "teamva-claude-probe-")
	if err != nil {
		return nil, errors.New("prepare claude probe failed")
	}
	client, err := newInstance(provider.InstanceOptions{Executable: executable, ConfigDir: dir})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	c := client.(*Client)
	c.ownedDirectory = dir
	return c, nil
}

func newInstance(options provider.InstanceOptions) (provider.Adapter, error) {
	if err := WriteConfig(options.ConfigDir, options.Config); err != nil {
		return nil, err
	}
	data := options.DataDir
	if data == "" {
		data = filepath.Join(options.ConfigDir, "data")
	}
	for _, dir := range []string{data, filepath.Join(data, "home"), filepath.Join(data, "claude"), filepath.Join(data, "tmp"), filepath.Join(data, "sessions")} {
		if err := privateDirectory(dir); err != nil {
			return nil, err
		}
	}
	executable := options.Executable
	if executable == "" {
		executable = "claude"
	}
	return &Client{executable: executable, configDir: options.ConfigDir, dataDir: data}, nil
}

func (c *Client) config() (provider.InstanceConfig, error) {
	var config provider.InstanceConfig
	root, err := os.OpenRoot(c.configDir)
	if err != nil {
		return config, errors.New("open claude instance config failed")
	}
	defer root.Close()
	file, err := root.Open("instance.json")
	if err != nil {
		return config, errors.New("read claude instance config failed")
	}
	defer file.Close()
	if err = json.NewDecoder(file).Decode(&config); err != nil {
		return config, errors.New("decode claude instance config failed")
	}
	return config, nil
}
