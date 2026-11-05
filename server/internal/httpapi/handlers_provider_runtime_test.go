package httpapi

import (
	"context"
	"net/http"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type providerRuntimeRelay struct {
	provider string
	calls    int
}

func (r *providerRuntimeRelay) NotifyIssueActivation(string) {}

func (r *providerRuntimeRelay) RecycleIdleProvider(_ context.Context, provider string) (nodeprotocol.ProviderRuntimeRecycleResult, error) {
	r.provider = provider
	r.calls++
	return nodeprotocol.ProviderRuntimeRecycleResult{
		Provider: provider, RecycledInstances: 2,
	}, nil
}

func TestRecycleIdleProgrammingToolIsTenantScopedAndUsesExactProvider(t *testing.T) {
	ginSetTestMode(t)
	repository := newTestRepository(t)
	owner := createTestIdentity(t, repository, "provider-owner@example.test")
	other := createTestIdentity(t, repository, "provider-other@example.test")
	headers := func(identity testIdentity) map[string]string {
		return map[string]string{
			"Cookie":       sessionCookieName + "=" + identity.token,
			"X-CSRF-Token": identity.csrf, "Origin": testBrowserOrigin,
		}
	}
	relay := &providerRuntimeRelay{}
	router := newRouterWithOrigins(repository, relay, "test", true, []string{testBrowserOrigin})
	computer := createTestComputer(t, repository, headers(owner), "Provider Runtime Mac")

	response := request(t, router, http.MethodPost,
		"/v1/computers/"+computer.ID+"/programming-tools/codex/recycle-idle",
		map[string]any{}, headers(owner), http.StatusOK)
	result := decodeData[nodeprotocol.ProviderRuntimeRecycleResult](t, response)
	if result.Provider != "codex" || result.RecycledInstances != 2 || relay.provider != "codex" || relay.calls != 1 {
		t.Fatalf("result=%+v relay=%+v", result, relay)
	}

	crossTenant := request(t, router, http.MethodPost,
		"/v1/computers/"+computer.ID+"/programming-tools/codex/recycle-idle",
		map[string]any{}, headers(other), http.StatusNotFound)
	if crossTenant.Error == nil || crossTenant.Error.Code != "not_found" || relay.calls != 1 {
		t.Fatalf("cross-tenant response=%+v calls=%d", crossTenant.Error, relay.calls)
	}
	request(t, router, http.MethodPost,
		"/v1/computers/"+computer.ID+"/programming-tools/unknown/recycle-idle",
		map[string]any{}, headers(owner), http.StatusBadRequest)
	if relay.calls != 1 {
		t.Fatalf("invalid provider reached runtime: calls=%d", relay.calls)
	}
}

var _ Runtime = (*providerRuntimeRelay)(nil)
