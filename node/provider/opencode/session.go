package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/teamva-ai/teamva/node/provider"
)

// Model.Info.id is the selectable catalog ID; modelID may name a different
// upstream model. Session create/switch takes Model.Ref, not Model.Info.
type modelReference struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

func parseModelReference(value string) (modelReference, error) {
	providerID, model, ok := strings.Cut(value, "/")
	id, variant, hasVariant := strings.Cut(model, "#")
	if !ok || providerID == "" || strings.Contains(providerID, "#") || id == "" || (hasVariant && (variant == "" || strings.Contains(variant, "#"))) {
		return modelReference{}, errors.New("opencode model must use provider/model format")
	}
	return modelReference{ID: id, ProviderID: providerID, Variant: variant}, nil
}

func modelLocationPath(path, directory string) string {
	if directory != "" {
		return path + "?location[directory]=" + url.QueryEscape(directory)
	}
	return path
}

func (c *Client) defaultModel(ctx context.Context, directory string) (*modelReference, error) {
	var response struct {
		Data json.RawMessage `json:"data"`
	}
	if err := c.getJSON(ctx, modelLocationPath("/api/model/default", directory), &response); err != nil {
		return nil, err
	}
	// The V2 contract is a location envelope containing Model.Info | null,
	// never a provider/model string or a map of provider defaults.
	if bytes.Equal(bytes.TrimSpace(response.Data), []byte("null")) {
		return nil, nil
	}
	var model modelReference
	if json.Unmarshal(response.Data, &model) != nil || model.ID == "" || model.ProviderID == "" {
		return nil, errors.New("opencode native default model response invalid")
	}
	return &model, nil
}

func (c *Client) configuredModel(ctx context.Context, directory string) (*modelReference, error) {
	// V2 config.get returns documents in ascending priority, not a merged object
	// or an envelope. Decode only the root selection; never retain/log providers,
	// credentials, or the other private document contents.
	var entries []struct {
		Type string `json:"type"`
		Info struct {
			Model json.RawMessage `json:"model"`
		} `json:"info"`
	}
	if err := c.getJSON(ctx, modelLocationPath("/api/config", directory), &entries); err != nil {
		return nil, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.Type != "document" || len(entry.Info.Model) == 0 {
			continue
		}
		var short string
		if json.Unmarshal(entry.Info.Model, &short) == nil {
			model, err := parseModelReference(short)
			return &model, err
		}
		// Config.ModelSelection uses `model`; Session/Model.Ref uses `id`.
		var selected struct {
			ProviderID string `json:"providerID"`
			Model      string `json:"model"`
			Variant    string `json:"variant"`
		}
		if json.Unmarshal(entry.Info.Model, &selected) != nil || selected.ProviderID == "" || selected.Model == "" {
			return nil, errors.New("opencode native configured model response invalid")
		}
		return &modelReference{ProviderID: selected.ProviderID, ID: selected.Model, Variant: selected.Variant}, nil
	}
	return nil, nil
}

func (c *Client) resolveModel(ctx context.Context, request provider.RunRequest) (modelReference, error) {
	if request.Model != "" {
		return parseModelReference(request.Model)
	}
	configured, err := c.configuredModel(ctx, request.CWD)
	if err != nil {
		return modelReference{}, err
	}
	if configured != nil {
		// An explicit root configuration is user intent, even if its model has
		// disappeared. Surface its eventual error instead of changing providers.
		return *configured, nil
	}
	model, err := c.defaultModel(ctx, request.CWD)
	if err != nil {
		return modelReference{}, err
	}
	// A non-null native default is NOT evidence of an explicit configuration:
	// the pinned binary also returns its own obsolete fallback here. Every
	// automatic candidate must belong to the current dynamic catalog.
	models, err := c.listModelsAt(ctx, request.CWD)
	if err != nil {
		return modelReference{}, err
	}
	if len(models) == 0 {
		return modelReference{}, errors.New("opencode has no available model; configure a provider or select a model")
	}
	if model != nil && slices.Contains(models, model.ProviderID+"/"+model.ID) {
		return *model, nil
	}
	// Native initialization may have settled while the catalog warmed. This
	// second candidate still needs the same membership check (null is not the
	// only unsafe result).
	model, err = c.defaultModel(ctx, request.CWD)
	if err != nil {
		return modelReference{}, err
	}
	if model != nil && slices.Contains(models, model.ProviderID+"/"+model.ID) {
		return *model, nil
	}
	return parseModelReference(models[0])
}

// prepareSession runs before event subscription/prompt submission. An existing
// explicit selection is durable and is never replaced by a newly chosen default.
func (c *Client) prepareSession(ctx context.Context, request provider.RunRequest) (string, usageTotals, error) {
	sessionID := request.NativeSessionID
	if sessionID == "" {
		model, err := c.resolveModel(ctx, request)
		if err != nil {
			return "", usageTotals{}, err
		}
		body := c.sessionCreateBody(request)
		body["model"] = model
		var created struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := c.requestJSON(ctx, http.MethodPost, "/api/session", body, &created); err != nil {
			return "", usageTotals{}, err
		}
		if created.Data.ID == "" {
			return "", usageTotals{}, errors.New("opencode session create returned no id")
		}
		return created.Data.ID, usageTotals{}, nil
	}
	path := "/api/session/" + url.PathEscape(sessionID)
	var session struct {
		Data struct {
			ID       string          `json:"id"`
			Agent    string          `json:"agent"`
			Model    *modelReference `json:"model"`
			Tokens   json.RawMessage `json:"tokens"`
			Location struct {
				Directory string `json:"directory"`
			} `json:"location"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, path, &session); err != nil {
		return "", usageTotals{}, errors.New("opencode native session usage baseline unavailable")
	}
	if session.Data.ID != sessionID {
		return "", usageTotals{}, errors.New("opencode native session is missing")
	}
	if session.Data.Location.Directory != request.CWD {
		return "", usageTotals{}, errors.New("opencode native session working directory changed")
	}
	// V2.0.18 usage updates and GET Session.Info.tokens are session-cumulative,
	// including across restarts. Never guess zero for a missing durable baseline.
	baseline, err := decodeUsageTotals(session.Data.Tokens)
	if err != nil {
		return "", usageTotals{}, errors.New("opencode native session usage baseline invalid")
	}
	if session.Data.Model != nil && (session.Data.Model.ID == "" || session.Data.Model.ProviderID == "") {
		return "", usageTotals{}, errors.New("opencode native session model invalid")
	}
	if session.Data.Agent != "" && session.Data.Agent != "build" {
		if err := c.requestJSON(ctx, http.MethodPost, path+"/agent", map[string]any{"agent": "build"}, nil); err != nil {
			return "", usageTotals{}, err
		}
	}
	// Resume replaces old permissions before any prompt; previous writable/IPC
	// grants must not survive the current host-provided authorization snapshot.
	if err := c.requestJSON(ctx, http.MethodPatch, path,
		map[string]any{"permissions": c.permissionRules(request)}, nil); err != nil {
		return "", usageTotals{}, err
	}
	if session.Data.Model == nil {
		model, err := c.resolveModel(ctx, request)
		if err != nil {
			return "", usageTotals{}, err
		}
		if err := c.requestJSON(ctx, http.MethodPost, path+"/model", map[string]any{"model": model}, nil); err != nil {
			return "", usageTotals{}, err
		}
	}
	return sessionID, baseline, nil
}
