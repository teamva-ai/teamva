package runtime

import (
	"context"
	"errors"
	"testing"

	adapter "github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/catalog"
)

func TestSetProviderExecutableSwapsResolutionWithoutRestart(t *testing.T) {
	t.Chdir(t.TempDir()) // keep bundled-runtime discovery out of the repository tree
	definition, ok := catalog.Lookup("opencode")
	if !ok {
		t.Fatal("opencode definition missing")
	}
	probe, err := definition.NewProbe("/opt/one/opencode")
	if err != nil {
		t.Fatal(err)
	}
	engine := New("cmp-executable", nil, []adapter.Adapter{probe}, t.TempDir(), map[string]string{"opencode": "/opt/one/opencode"})
	defer func() { _ = engine.Close() }()

	if adapter, ok := engine.probeAdapter("opencode"); !ok || adapter.Name() != "opencode" {
		t.Fatalf("initial probe adapter=%v ok=%v", adapter, ok)
	}
	if got := engine.executableFor("opencode"); got != "/opt/one/opencode" {
		t.Fatalf("initial executable=%q", got)
	}

	manager, err := engine.managerFor("opencode", engine.executableFor("opencode"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if manager.executable != "/opt/one/opencode" {
		t.Fatalf("manager executable=%q", manager.executable)
	}

	if err := engine.SetProviderExecutable(t.Context(), "opencode", "/opt/two/opencode"); err != nil {
		t.Fatal(err)
	}
	if got := engine.executableFor("opencode"); got != "/opt/two/opencode" {
		t.Fatalf("updated executable=%q", got)
	}
	if manager.executable != "/opt/two/opencode" {
		t.Fatalf("manager executable was not swapped: %q", manager.executable)
	}
	if snapshot := engine.AdapterSnapshot(); len(snapshot) != 1 || snapshot[0].Name() != "opencode" {
		t.Fatalf("adapter snapshot=%v", snapshot)
	}

	if err := engine.SetProviderExecutable(t.Context(), "unknown", "/opt/two/opencode"); err == nil {
		t.Fatal("unknown provider must be rejected")
	}
	if err := engine.SetProviderExecutable(t.Context(), "opencode", ""); err == nil {
		t.Fatal("empty executable must be rejected; the Controller resolves defaults")
	}

	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	// A closed Runtime must not accept a swap that would leak a probe adapter.
	closedErr := engine.SetProviderExecutable(context.Background(), "opencode", "/opt/three/opencode")
	var runtimeError *Error
	if !errors.As(closedErr, &runtimeError) || runtimeError.Code != "runtime_closed" {
		t.Fatalf("closed runtime error=%v", closedErr)
	}
}
