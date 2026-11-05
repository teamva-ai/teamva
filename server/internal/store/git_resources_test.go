package store

import (
	"errors"
	"strings"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestRunStartPayloadHonorsGitResourceAndFrameLimits(t *testing.T) {
	resources := make([]nodeprotocol.GitResourceRequest, nodeprotocol.MaxGitResourcesPerRun+1)
	for index := range resources {
		resources[index] = nodeprotocol.GitResourceRequest{
			ResourceID: "resource", Name: "Code", Path: "/tmp/code",
		}
	}
	assertGitResourceDomainCode(t, validateRunGitResources(resources), "git_resources_too_large")

	resources = resources[:1]
	resources[0].Path = "/" + strings.Repeat("x", nodeprotocol.MaxGitResourcePathBytes)
	assertGitResourceDomainCode(t, validateRunGitResources(resources), "git_resource_invalid")

	_, err := marshalStartRunPayload(nodeprotocol.StartRunPayload{
		RunID: "run", LeaseEpoch: 1, Provider: "codex",
		Prompt: strings.Repeat("x", nodeprotocol.MaxCommandPayloadBytes),
	})
	assertGitResourceDomainCode(t, err, "run_start_payload_too_large")
}

func assertGitResourceDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != code {
		t.Fatalf("error=%v want code=%s", err, code)
	}
}
