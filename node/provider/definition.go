package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

const APIVersion = 1

var providerID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func ValidID(id string) bool { return providerID.MatchString(id) }

// Metadata comes from the provider's own provider.json, not a core allow-list.
type Metadata struct {
	APIVersion      int                           `json:"api_version"`
	ID              string                        `json:"id"`
	DisplayName     string                        `json:"display_name"`
	Command         string                        `json:"command"`
	Description     string                        `json:"description"`
	DescriptionKey  string                        `json:"description_key,omitempty"`
	InstallHint     string                        `json:"install_hint,omitempty"`
	LoginHint       string                        `json:"login_hint,omitempty"`
	DocsURL         string                        `json:"docs_url,omitempty"`
	DiagnosticSteps []nodeprotocol.DiagnosticStep `json:"diagnostic_steps,omitempty"`
	Features        []string                      `json:"features,omitempty"`
}

func (m Metadata) Supports(feature string) bool {
	for _, value := range m.Features {
		if value == feature {
			return true
		}
	}
	return false
}

func (m Metadata) Validate() error {
	if m.APIVersion != APIVersion {
		return fmt.Errorf("provider %q: unsupported API version %d", m.ID, m.APIVersion)
	}
	if !ValidID(m.ID) || m.DisplayName == "" || m.Command == "" {
		return errors.New("provider metadata requires a valid ID, display name and command")
	}
	if len(m.InstallHint) > 400 || len(m.LoginHint) > 400 {
		return fmt.Errorf("provider %q: setup hints must be at most 400 characters", m.ID)
	}
	if m.DocsURL != "" {
		parsed, err := url.Parse(m.DocsURL)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return fmt.Errorf("provider %q: docs_url must be an http(s) URL", m.ID)
		}
	}
	return nil
}

// InstanceConfig is a run's immutable authorization/configuration snapshot.
// Providers must not broaden these roots or expose business-writing tools in
// ReadOnly. A host-supplied RunRequest.ToolRequestDir is private IPC, never a
// project root; an adapter may allow this narrowly scoped transport write.
type InstanceConfig struct {
	Model                 string
	SystemPrompt          string
	Skills                []nodeprotocol.SkillPayload
	WritableRepositories  []string
	WorkspaceInstructions string
	WorktreeHint          bool
	ReadOnly              bool
	// DisableTools removes the complete native tool catalog and all MCP/Computer
	// Use capabilities. Bound Skills may still contribute instruction text.
	DisableTools bool `json:",omitempty"`
	ComputerUse  bool
	// MCPServers is resolved immediately before a Run. It may contain secrets:
	// only provider-private config files may consume it, never durable commands,
	// database snapshots, events or diagnostics.
	MCPServers map[string]json.RawMessage `json:"-"`
}

type InstanceOptions struct {
	Executable string
	ConfigDir  string
	// DataDir is the private, durable native-session home for this exact
	// Provider/instance/generation/project/workspace scope. It is outside the
	// rebuildable ConfigDir and is never subject to config retention. Factories
	// may use a provider-local fallback only when direct callers leave it empty.
	DataDir string
	// CacheDir 是运行时可选的共享缓存目录；Provider 可把只依赖自身版本、
	// 可跨实例复用的文件（如 CLI 的插件依赖）放在这里，避免每个实例重复安装。
	CacheDir string
	Config   InstanceConfig
	Logger   *log.Logger
}

// Definition owns all provider-specific creation and configuration. Probe
// clients must not be reused as managed execution instances.
type Definition struct {
	Metadata    Metadata
	NewProbe    func(executable string) (Adapter, error)
	NewInstance func(InstanceOptions) (Adapter, error)
	Configure   func(configDir string, config InstanceConfig) error
	// Optional provider-owned model API configuration. Secrets bypass commands.
	ModelSetup func() ModelSetup
	// Optional when the adapter emits only output.delta/output.snapshot.
	NewOutputProjector func(metadataLimit int) OutputProjector
	TerminalStatus     func(map[string]any) (string, bool)
	FileChange         func(string, map[string]any) (FileChange, bool)
}

type ModelSetup interface {
	Settings(context.Context) (nodeprotocol.ModelProviderSettings, error)
	Save(context.Context, nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderView, error)
	Test(context.Context, nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderTestResult, error)
}

func (d Definition) Validate() error {
	if err := d.Metadata.Validate(); err != nil {
		return err
	}
	if d.NewProbe == nil || d.NewInstance == nil || d.Configure == nil {
		return fmt.Errorf("provider %q: incomplete factory", d.Metadata.ID)
	}
	return nil
}
