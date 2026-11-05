package nodeprotocol

// Model setup travels through the in-process facade, never the durable command
// inbox. APIKey is write-only HTTP input and must not enter logs or snapshots.
type ModelProviderInput struct {
	PresetID string `json:"preset_id"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key,omitempty"`
}

type ModelProviderPreset struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	BaseURL  string   `json:"base_url"`
	Protocol string   `json:"protocol"`
	Models   []string `json:"models"`
}

type ModelProviderView struct {
	PresetID      string `json:"preset_id"`
	Name          string `json:"name"`
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	ModelRef      string `json:"model_ref"`
	KeyConfigured bool   `json:"key_configured"`
}

type ModelProviderSettings struct {
	Presets    []ModelProviderPreset `json:"presets"`
	Configured []ModelProviderView   `json:"configured"`
}

type ModelProviderTestResult struct {
	OK         bool   `json:"ok"`
	Code       string `json:"code"`
	DurationMS int64  `json:"duration_ms"`
}
