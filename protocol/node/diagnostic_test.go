package nodeprotocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiagnosticSanitizationPreservesGatewayCauseWithoutCredentials(t *testing.T) {
	payload := map[string]any{
		"error":   map[string]any{"data": map[string]any{"message": "Gateway 429 quota exhausted", "additionalDetails": "request=req_fixture; retry after 10s"}},
		"headers": map[string]any{"Authorization": "Bearer fixture-auth", "Cookie": "session=fixture-cookie", "x-api-key": "fixture-key"},
		"echo":    "Gateway echoed fixture-key", "dsn": "fixture-connection",
		"detail": "http://fixture-user:fixture-pass@gateway.test/v1?api_key=fixture-query&model=example; password=fixture-pwd; API_KEY='fixture-assigned'; Authorization: Bearer fixture-bearer\nCookie: session=fixture-header-cookie; other=value",
	}
	before, _ := json.Marshal(payload)
	clean := NormalizeDiagnosticPayload(payload)
	encoded, _ := json.Marshal(clean)
	for _, secret := range []string{"fixture-auth", "fixture-cookie", "fixture-key", "fixture-connection", "fixture-user", "fixture-pass", "fixture-query", "fixture-pwd", "fixture-assigned", "fixture-bearer", "fixture-header-cookie"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("credential %s was not removed", secret)
		}
	}
	for _, useful := range []string{"Gateway 429 quota exhausted", "request=req_fixture", "gateway.test", "model=example"} {
		if !strings.Contains(string(encoded), useful) {
			t.Errorf("useful diagnostic %s lost: %s", useful, encoded)
		}
	}
	after, _ := json.Marshal(payload)
	if string(before) != string(after) {
		t.Fatal("sanitization mutated native input")
	}
}

func TestDiagnosticOpaqueKeyAndNativeResponseBody(t *testing.T) {
	payload := map[string]any{"error": map[string]any{"message": "Incorrect API key provided: fixture-only-opaque", "response": map[string]any{"body": `{"error":{"message":"Gateway quota exhausted"},"Authorization":"Bearer fixture-only-json-auth","Cookie":"session=fixture-only-json-cookie"}`}}}
	clean := NormalizeDiagnosticPayload(payload)
	encoded, _ := json.Marshal(clean)
	if strings.Contains(string(encoded), "fixture-only-") || !strings.Contains(clean["message"].(string), "Gateway quota exhausted") {
		t.Fatalf("unsafe or missing response diagnostic: %s", encoded)
	}
	first, _ := json.Marshal(clean)
	second, _ := json.Marshal(NormalizeDiagnosticPayload(clean))
	if string(first) != string(second) {
		t.Fatal("normalization must be deterministic/idempotent for durable replay")
	}
}
