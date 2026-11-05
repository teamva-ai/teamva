package opencode

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/teamva-ai/teamva/node/provider"
)

type skillBinding struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// A source directory is additive in V2, not an isolation boundary. Bind the
// exact host-installed entry file as well as its native path-derived ID; an
// ambient definition with the same ID must never substitute for that file.
func configuredSkillBindings(configDir string, config provider.InstanceConfig) ([]skillBinding, error) {
	bindings := make([]skillBinding, 0, len(config.Skills))
	seen := map[string]bool{}
	packages := map[string]bool{}
	for _, skill := range config.Skills {
		packageID := provider.SafePathSegment(skill.Slug)
		if packages[packageID] {
			return nil, errors.New("duplicate opencode Skill binding")
		}
		packages[packageID] = true
		root, err := filepath.EvalSymlinks(configDir)
		if err != nil {
			return nil, errors.New("invalid opencode Skill binding")
		}
		root, err = filepath.Abs(root)
		if err != nil {
			return nil, errors.New("invalid opencode Skill binding")
		}
		directory := filepath.Join(root, "skills", packageID)
		canonical, err := filepath.EvalSymlinks(directory)
		// Canonicalize only platform aliases above the private config home; do
		// not authorize a symlink substituted for a bound Skill or its directory.
		if err != nil || canonical != directory {
			return nil, errors.New("opencode bound Skill must be a private real entry file")
		}
		before := len(bindings)
		// Installed artifacts can be multi-Skill packages. The host already
		// verifies/copies their complete tree; preserve every SKILL.md, but not
		// unbound siblings or root README.md files auto-discovered as V2 Skills.
		err = filepath.WalkDir(directory, func(entry string, info fs.DirEntry, walkErr error) error {
			if walkErr != nil || info.Type()&os.ModeSymlink != 0 {
				return errors.New("unsafe bound Skill tree")
			}
			if info.IsDir() || info.Name() != "SKILL.md" {
				return nil
			}
			if !info.Type().IsRegular() {
				return errors.New("unsafe bound Skill entry")
			}
			id := filepath.Base(filepath.Dir(entry)) // Pinned V2 SkillFile.parse.
			if seen[id] || strings.ContainsAny(id, "*?\\") {
				return errors.New("ambiguous native Skill ID")
			}
			seen[id] = true
			bindings = append(bindings, skillBinding{ID: id, Path: entry})
			return nil
		})
		if err != nil || len(bindings) == before {
			return nil, errors.New("opencode bound Skill must have unambiguous private real entry files")
		}
	}
	return bindings, nil
}

// Run only after the managed plugin is active, and before creating/admitting a
// prompt. A broken/unsupported native transform must fail closed, not just hide
// names from a generated prompt while the native skill tool can still load them.
func (c *Client) verifySkillBindings(ctx context.Context, cwd string) error {
	var sealed struct {
		Output bool `json:"output"`
	}
	if err := c.requestJSON(ctx, http.MethodPost, "/api/rpc/teamva.skill-bindings/seal?location[directory]="+url.QueryEscape(cwd),
		map[string]any{"input": map[string]any{}}, &sealed); err != nil || !sealed.Output {
		return errors.New("opencode Skill binding activation failed")
	}
	var catalog struct {
		Data []skillBinding `json:"data"`
	}
	if err := c.getJSON(ctx, "/api/skill?location[directory]="+url.QueryEscape(cwd), &catalog); err != nil {
		return errors.New("opencode Skill binding verification failed")
	}
	expected := make(map[string]string, len(c.boundSkills))
	for _, binding := range c.boundSkills {
		expected[binding.ID] = binding.Path
	}
	if len(catalog.Data) != len(expected) {
		return errors.New("opencode native Skill registry does not match host bindings")
	}
	for _, skill := range catalog.Data {
		if path, ok := expected[skill.ID]; !ok || skill.Path != path {
			return errors.New("opencode native Skill registry does not match host bindings")
		}
		delete(expected, skill.ID)
	}
	return nil
}
