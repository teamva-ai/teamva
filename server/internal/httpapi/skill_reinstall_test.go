package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

type skillReinstallTestRelay struct {
	*skillUninstallTestRelay
	install func(context.Context, nodeprotocol.InstallSkillPayload) (json.RawMessage, error)
}

func (r *skillReinstallTestRelay) ExecuteLocalCommand(ctx context.Context, name string, input any) (json.RawMessage, error) {
	if name != "skill.install" {
		return r.skillUninstallTestRelay.ExecuteLocalCommand(ctx, name, input)
	}
	payload, ok := input.(nodeprotocol.InstallSkillPayload)
	if !ok {
		return nil, errors.New("unexpected install payload")
	}
	return r.install(ctx, payload)
}

func TestSkillReinstallHTTPAfterUninstallRetainsVersionAndRestoresInstallation(t *testing.T) {
	f := newSkillUninstallHTTPFixture(t)
	artifact, err := f.repository.GetSkillPackageVersion(t.Context(), f.userID, f.installation.SkillPackageVersionID)
	if err != nil {
		t.Fatal(err)
	}
	var revisions []string
	relay := &skillReinstallTestRelay{skillUninstallTestRelay: f.relay,
		install: func(_ context.Context, payload nodeprotocol.InstallSkillPayload) (json.RawMessage, error) {
			if payload.Slug != f.packageItem.Slug {
				return nil, errors.New("wrong skill selected for reinstall")
			}
			revisions = append(revisions, payload.Revision)
			// This fake runtime restores only the disposable unit-test artifact;
			// it does not touch the QA service, network or real installed skills.
			if err := os.MkdirAll(f.directory, 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(f.directory, "SKILL.md"), []byte("test artifact"), 0o600); err != nil {
				return nil, err
			}
			return json.Marshal(nodeprotocol.InstallSkillResult{
				Slug: payload.Slug, Path: f.directory, CommitSHA: artifact.CommitSHA,
				ContentDigest: artifact.ContentDigest, Manifest: artifact.Manifest,
				FileCount: artifact.FileCount, SizeBytes: artifact.SizeBytes,
			})
		}}
	f.router = newRouter(f.repository, relay, "test", true)
	for cycle := 0; cycle < 2; cycle++ {
		request(t, f.router, http.MethodDelete, f.uninstallPath(), nil, f.headers, http.StatusNoContent)
		f.assertVisible(t, false)
		oldID := f.installation.ID
		oldVersion := f.installation.Version
		path := "/v1/computers/" + f.computerID + "/skill-packages"
		body := map[string]any{"skill_package_id": f.packageItem.ID}
		f.installation = decodeData[domain.ComputerSkillPackage](t, request(t, f.router, http.MethodPost, path, body, f.headers, http.StatusCreated))
		if f.installation.ID != oldID || f.installation.SkillPackageVersionID != artifact.ID || f.installation.Version != oldVersion+2 {
			t.Fatalf("reinstall did not reactivate the installation with a newer CAS version: %+v", f.installation)
		}
		f.assertVisible(t, true)
		if _, err := os.Stat(filepath.Join(f.directory, "SKILL.md")); err != nil {
			t.Fatalf("reinstall artifact missing: %v", err)
		}
		// Reinstalling the now-active version remains idempotent and pinned.
		replayed := decodeData[domain.ComputerSkillPackage](t, request(t, f.router, http.MethodPost, path, body, f.headers, http.StatusCreated))
		if replayed.ID != f.installation.ID || replayed.Version != f.installation.Version {
			t.Fatalf("idempotent reinstall changed installation: %+v", replayed)
		}
	}
	if f.relay.calls.Load() != 2 || len(revisions) != 4 || revisions[0] != "" || revisions[1] != artifact.CommitSHA ||
		revisions[2] != "" || revisions[3] != artifact.CommitSHA {
		t.Fatalf("unexpected lifecycle calls: uninstalls=%d install revisions=%v", f.relay.calls.Load(), revisions)
	}
}
