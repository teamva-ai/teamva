package openapi_test

import (
	"os"
	"strings"
	"testing"
)

// Check these public schema boundaries without adding a YAML dependency to the
// runtime protocol module. Component blocks use the spec's fixed indentation.
func TestAppearanceAndProjectColorContract(t *testing.T) {
	encoded, err := os.ReadFile("teamva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(encoded)
	_, schemas, found := strings.Cut(spec, "\n  schemas:\n")
	if !found {
		t.Fatal("missing component schemas")
	}
	schema := func(name string) string {
		t.Helper()
		_, rest, found := strings.Cut(schemas, "\n    "+name+":\n")
		if !found {
			t.Fatalf("missing schema %s", name)
		}
		lines := strings.Split(rest, "\n")
		for index, line := range lines {
			if strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") && strings.HasSuffix(line, ":") {
				return strings.Join(lines[:index], "\n")
			}
		}
		return rest
	}
	for _, name := range []string{"Project", "CreateProjectRequest", "UpdateProjectRequest"} {
		block := schema(name)
		if strings.Contains(block, "theme") || !strings.Contains(block, "additionalProperties: false") {
			t.Fatalf("%s must reject all project color fields: %s", name, block)
		}
	}
	if strings.Contains(spec, "ProjectTheme") {
		t.Fatal("retired project color schema remains in public protocol")
	}
	appearance := schema("Appearance")
	for _, required := range []string{
		"additionalProperties: false", "required: [palette]",
		"enum: [default, hallmark, chamber, flexoki, ocean, forest, iris]", "default: default",
		"control: {type: string, pattern: '^$|^#[0-9A-Fa-f]{6}$'",
	} {
		if !strings.Contains(appearance, required) {
			t.Fatalf("appearance schema missing %q: %s", required, appearance)
		}
	}
}
