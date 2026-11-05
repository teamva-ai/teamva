package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// Only this provider owns the on-disk format, provider packages and API wire
// protocols. It never modifies the user's existing opencode.json(c)/auth.json.
var modelSetupMu sync.Mutex

type modelSetup struct{}
type modelSetupError string

func (e modelSetupError) Error() string          { return string(e) }
func (e modelSetupError) ModelSetupCode() string { return string(e) }

var modelPresets = []nodeprotocol.ModelProviderPreset{
	{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Protocol: "openai", Models: []string{"gpt-4.1-mini", "gpt-4.1"}},
	{ID: "anthropic", Name: "Anthropic", BaseURL: "https://api.anthropic.com/v1", Protocol: "anthropic", Models: []string{"claude-sonnet-4-5", "claude-haiku-4-5"}},
	{ID: "google", Name: "Google Gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta", Protocol: "google", Models: []string{"gemini-2.5-flash", "gemini-2.5-pro"}},
	{ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", Protocol: "openai", Models: []string{"deepseek-chat", "deepseek-reasoner"}},
	{ID: "dashscope", Name: "阿里云百炼 / Qwen", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Protocol: "openai", Models: []string{"qwen-plus", "qwen-turbo"}},
	{ID: "moonshot", Name: "Moonshot / Kimi", BaseURL: "https://api.moonshot.cn/v1", Protocol: "openai", Models: []string{"kimi-k2.5", "moonshot-v1-8k"}},
	{ID: "zhipu", Name: "智谱 / GLM", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Protocol: "openai", Models: []string{"glm-4.5", "glm-4-flash"}},
	{ID: "openrouter", Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", Protocol: "openai", Models: []string{"openrouter/auto"}},
	{ID: "custom", Name: "OpenAI Compatible", BaseURL: "", Protocol: "openai", Models: []string{}},
}

type savedModelProvider struct {
	PresetID string   `json:"preset_id"`
	BaseURL  string   `json:"base_url"`
	Model    string   `json:"model"`
	Models   []string `json:"models,omitempty"`
	APIKey   string   `json:"api_key"`
}
type savedModelProviders struct {
	Version int                           `json:"version"`
	Items   map[string]savedModelProvider `json:"items"`
}

func modelSetupPath() (string, error) {
	home, err := configHome()
	if err != nil {
		return "", modelSetupError("model_provider_storage_failed")
	}
	return filepath.Join(home, "opencode", "teamva-model-providers.json"), nil
}

func readModelSetup() (savedModelProviders, error) {
	result := savedModelProviders{Version: 1, Items: map[string]savedModelProvider{}}
	path, err := modelSetupPath()
	if err != nil {
		return result, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1<<20 {
		return result, modelSetupError("model_provider_storage_failed")
	}
	file, err := os.Open(path)
	if err != nil {
		return result, modelSetupError("model_provider_storage_failed")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return result, modelSetupError("model_provider_storage_failed")
	}
	payload, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(payload) > 1<<20 || json.Unmarshal(payload, &result) != nil || result.Version != 1 || result.Items == nil || len(result.Items) > len(modelPresets) {
		return result, modelSetupError("model_provider_storage_failed")
	}
	return result, nil
}

func modelPreset(id string) (nodeprotocol.ModelProviderPreset, bool) {
	for _, p := range modelPresets {
		if p.ID == id {
			return p, true
		}
	}
	return nodeprotocol.ModelProviderPreset{}, false
}

func normalizeModelInput(input nodeprotocol.ModelProviderInput, stored savedModelProviders) (savedModelProvider, nodeprotocol.ModelProviderPreset, error) {
	p, ok := modelPreset(input.PresetID)
	if !ok {
		return savedModelProvider{}, p, modelSetupError("model_provider_invalid")
	}
	entry := savedModelProvider{PresetID: p.ID, BaseURL: strings.TrimRight(strings.TrimSpace(input.BaseURL), "/"), Model: strings.TrimSpace(input.Model), APIKey: strings.TrimSpace(input.APIKey)}
	if entry.BaseURL == "" {
		entry.BaseURL = p.BaseURL
	}
	endpoint, err := url.Parse(entry.BaseURL)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Opaque != "" {
		return entry, p, modelSetupError("model_provider_invalid")
	}
	host := endpoint.Hostname()
	ip := net.ParseIP(host)
	local := host == "localhost" || (ip != nil && ip.IsLoopback())
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && local) {
		return entry, p, modelSetupError("model_provider_invalid")
	}
	if ip != nil && (ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast()) {
		return entry, p, modelSetupError("model_provider_invalid")
	}
	if len(entry.BaseURL) > 2048 || len(entry.Model) == 0 || len(entry.Model) > 200 || strings.ContainsAny(entry.Model, "?#\\") || strings.IndexFunc(entry.Model, unicode.IsControl) >= 0 || len(entry.APIKey) > 8192 || strings.ContainsAny(entry.APIKey, "\r\n") {
		return entry, p, modelSetupError("model_provider_invalid")
	}
	if entry.APIKey == "" {
		// A blank key retains a credential only for the same origin/endpoint.
		// Editing a URL must never silently send a previous key to a new server.
		if old, ok := stored.Items[p.ID]; ok && old.BaseURL == entry.BaseURL {
			entry.APIKey = old.APIKey
		}
	}
	if entry.APIKey == "" && !(p.ID == "custom" && local) {
		return entry, p, modelSetupError("model_provider_key_required")
	}
	return entry, p, nil
}

func modelView(entry savedModelProvider, p nodeprotocol.ModelProviderPreset) nodeprotocol.ModelProviderView {
	return nodeprotocol.ModelProviderView{PresetID: p.ID, Name: p.Name, BaseURL: entry.BaseURL, Model: entry.Model, ModelRef: "teamva-" + p.ID + "/" + entry.Model, KeyConfigured: entry.APIKey != ""}
}

func (modelSetup) Settings(ctx context.Context) (nodeprotocol.ModelProviderSettings, error) {
	if err := ctx.Err(); err != nil {
		return nodeprotocol.ModelProviderSettings{}, err
	}
	modelSetupMu.Lock()
	defer modelSetupMu.Unlock()
	stored, err := readModelSetup()
	if err != nil {
		return nodeprotocol.ModelProviderSettings{}, err
	}
	result := nodeprotocol.ModelProviderSettings{Presets: append([]nodeprotocol.ModelProviderPreset{}, modelPresets...), Configured: []nodeprotocol.ModelProviderView{}}
	for _, p := range modelPresets {
		if entry, ok := stored.Items[p.ID]; ok {
			result.Configured = append(result.Configured, modelView(entry, p))
		}
	}
	return result, nil
}

func (modelSetup) Save(ctx context.Context, input nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderView, error) {
	modelSetupMu.Lock()
	defer modelSetupMu.Unlock()
	stored, err := readModelSetup()
	if err != nil {
		return nodeprotocol.ModelProviderView{}, err
	}
	entry, p, err := normalizeModelInput(input, stored)
	if err != nil {
		return nodeprotocol.ModelProviderView{}, err
	}
	if err = ctx.Err(); err != nil {
		return nodeprotocol.ModelProviderView{}, err
	}
	// Credentials are shared by provider, not by Agent. Adding a model for one
	// Agent must not remove models already used by another Agent.
	models := []string{entry.Model}
	if previous, ok := stored.Items[p.ID]; ok && previous.BaseURL == entry.BaseURL {
		models = append(append(append([]string{}, previous.Models...), previous.Model), entry.Model)
	}
	seen := map[string]bool{}
	for _, model := range models {
		if model != "" && !seen[model] {
			entry.Models = append(entry.Models, model)
			seen[model] = true
		}
	}
	if len(entry.Models) > 64 {
		return nodeprotocol.ModelProviderView{}, modelSetupError("model_provider_invalid")
	}
	stored.Items[p.ID] = entry
	payload, err := json.Marshal(stored)
	if err != nil {
		return nodeprotocol.ModelProviderView{}, modelSetupError("model_provider_storage_failed")
	}
	path, err := modelSetupPath()
	if err != nil {
		return nodeprotocol.ModelProviderView{}, err
	}
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return nodeprotocol.ModelProviderView{}, modelSetupError("model_provider_storage_failed")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nodeprotocol.ModelProviderView{}, modelSetupError("model_provider_storage_failed")
	}
	file, err := os.CreateTemp(dir, ".teamva-model-*.tmp")
	if err != nil {
		return nodeprotocol.ModelProviderView{}, modelSetupError("model_provider_storage_failed")
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(payload)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		return nodeprotocol.ModelProviderView{}, modelSetupError("model_provider_storage_failed")
	}
	return modelView(entry, p), nil
}

func (modelSetup) Test(ctx context.Context, input nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderTestResult, error) {
	modelSetupMu.Lock()
	stored, err := readModelSetup()
	modelSetupMu.Unlock()
	if err != nil {
		return nodeprotocol.ModelProviderTestResult{}, err
	}
	entry, p, err := normalizeModelInput(input, stored)
	if err != nil {
		return nodeprotocol.ModelProviderTestResult{}, err
	}
	started := time.Now()
	result := nodeprotocol.ModelProviderTestResult{Code: "unreachable"}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	endpoint := entry.BaseURL + "/chat/completions"
	body := map[string]any{"model": entry.Model, "messages": []any{map[string]string{"role": "user", "content": "Hello"}}, "stream": false, "max_tokens": 64}
	switch p.Protocol {
	case "anthropic":
		endpoint = entry.BaseURL + "/messages"
		delete(body, "stream")
	case "google":
		endpoint = entry.BaseURL + "/models/" + url.PathEscape(entry.Model) + ":generateContent"
		body = map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": "Hello"}}}}, "generationConfig": map[string]int{"maxOutputTokens": 64}}
	}
	payload, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return result, modelSetupError("model_provider_invalid")
	}
	request.Header.Set("Content-Type", "application/json")
	if entry.APIKey != "" {
		switch p.Protocol {
		case "anthropic":
			request.Header.Set("x-api-key", entry.APIKey)
			request.Header.Set("anthropic-version", "2023-06-01")
		case "google":
			request.Header.Set("x-goog-api-key", entry.APIKey)
		default:
			request.Header.Set("Authorization", "Bearer "+entry.APIKey)
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	result.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		if ctx.Err() != nil {
			result.Code = "timeout"
		}
		return result, nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		switch response.StatusCode {
		case 401, 403:
			result.Code = "authentication_failed"
		case 429:
			result.Code = "rate_limited"
		default:
			result.Code = "request_failed"
		}
		return result, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		result.Code = "invalid_response"
		if ctx.Err() != nil {
			result.Code = "timeout"
		}
		return result, nil
	}
	var value struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal(raw, &value) != nil {
		result.Code = "invalid_response"
		return result, nil
	}
	valid := false
	if p.Protocol == "openai" {
		for _, choice := range value.Choices {
			var text string
			if json.Unmarshal(choice.Message.Content, &text) == nil && strings.TrimSpace(text) != "" {
				valid = true
			}
		}
	}
	if p.Protocol == "anthropic" {
		for _, content := range value.Content {
			if content.Type == "text" && strings.TrimSpace(content.Text) != "" {
				valid = true
			}
		}
	}
	if p.Protocol == "google" {
		for _, candidate := range value.Candidates {
			for _, part := range candidate.Content.Parts {
				if strings.TrimSpace(part.Text) != "" {
					valid = true
				}
			}
		}
	}
	result.OK = valid
	result.Code = "invalid_response"
	if valid {
		result.Code = "success"
	}
	result.DurationMS = time.Since(started).Milliseconds()
	// Never return any upstream text, headers or errors (they can echo API keys).
	return result, nil
}

func mergeManagedModelProviders(cfg map[string]any) error {
	modelSetupMu.Lock()
	stored, err := readModelSetup()
	modelSetupMu.Unlock()
	if err != nil {
		return err
	}
	if len(stored.Items) == 0 {
		return nil
	}
	providers, _ := cfg["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
		cfg["providers"] = providers
	}
	for _, p := range modelPresets {
		entry, ok := stored.Items[p.ID]
		if !ok {
			continue
		}
		pkg := "@opencode/ai/providers/openai-compatible"
		if p.ID == "openai" {
			pkg = "@opencode/ai/providers/openai/chat"
		}
		if p.ID == "openrouter" {
			pkg = "@opencode/ai/providers/openrouter"
		}
		if p.Protocol == "anthropic" {
			pkg = "@opencode/ai/providers/anthropic"
		}
		if p.Protocol == "google" {
			pkg = "@opencode/ai/providers/google"
		}
		models := map[string]any{}
		for _, model := range append(append([]string{}, entry.Models...), entry.Model) {
			if model != "" {
				models[model] = map[string]any{"modelID": model, "name": model}
			}
		}
		providers["teamva-"+p.ID] = map[string]any{"name": p.Name, "package": pkg, "settings": map[string]any{"baseURL": entry.BaseURL, "apiKey": entry.APIKey}, "models": models}
	}
	return nil
}

var _ provider.ModelSetup = modelSetup{}
