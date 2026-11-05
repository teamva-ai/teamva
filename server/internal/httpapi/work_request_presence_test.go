package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

type workRequestPresenceFixture struct {
	repo    *testRepository
	server  *httptest.Server
	client  *http.Client
	auth    map[string]string
	userID  string
	project domain.Project
}

type workRequestPresenceResponse struct {
	testResponse
	header http.Header
}

func newWorkRequestPresenceFixture(t *testing.T) *workRequestPresenceFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &workRequestPresenceFixture{repo: newTestRepository(t), auth: map[string]string{}}
	f.server = httptest.NewServer(newRouterWithOrigins(f.repo, nil, "work-request-presence", true, []string{testBrowserOrigin}))
	t.Cleanup(f.server.Close)
	f.client = f.server.Client()
	f.client.Timeout = 5 * time.Second
	login := f.request(t, http.MethodPost, "/v1/auth/local-session", map[string]any{}, http.StatusOK)
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.Data, &session); err != nil || session.CSRF == "" {
		t.Fatal("TCP bootstrap did not return a CSRF token")
	}
	for _, cookie := range (&http.Response{Header: login.header}).Cookies() {
		if cookie.Name == sessionCookieName && cookie.HttpOnly {
			f.auth["Cookie"] = cookie.Name + "=" + cookie.Value
		}
	}
	if f.auth["Cookie"] == "" {
		t.Fatal("TCP bootstrap did not set an HttpOnly session cookie")
	}
	f.auth["X-CSRF-Token"] = session.CSRF
	user, computer, err := f.repo.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	f.userID = user.ID
	// Use the public API's real project ID, not a synthetic destination that
	// would be rejected by navigation's ownership/link validation.
	f.project = decodeData[domain.Project](t, f.request(t, http.MethodPost, "/v1/projects", map[string]any{
		"computer_id": computer.ID, "name": "Work request destination",
	}, http.StatusCreated).testResponse)
	return f
}

func (f *workRequestPresenceFixture) request(t *testing.T, method, path string, body any, want int) workRequestPresenceResponse {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return f.rawRequest(t, method, path, raw, want)
}

func (f *workRequestPresenceFixture) rawRequest(t *testing.T, method, path string, raw []byte, want int) workRequestPresenceResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("construct work request")
	}
	// Only the transport address is ephemeral. The configured browser origin
	// and Host agree, without changing any production listener or credentials.
	req.Host = "example.com"
	req.Header.Set("Origin", testBrowserOrigin)
	req.Header.Set("Content-Type", "application/json")
	for name, value := range f.auth {
		req.Header.Set(name, value)
	}
	response, err := f.client.Do(req)
	if err != nil {
		t.Fatal("send work request over TCP")
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal("read work response")
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s status=%d, want=%d", method, path, response.StatusCode, want)
	}
	result := workRequestPresenceResponse{header: response.Header.Clone()}
	if len(encoded) != 0 {
		if err := json.Unmarshal(encoded, &result.testResponse); err != nil {
			t.Fatal("decode work response")
		}
	}
	if want == http.StatusBadRequest && (result.Error == nil || result.Error.Code != "invalid_argument") {
		t.Fatal("invalid work request must return invalid_argument")
	}
	return result
}

func workRequestPresenceSnapshot(t *testing.T, repo *testRepository) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range []string{"work_record_filters", "work_records"} {
		rows, err := repo.db.QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var records [][]any
		for rows.Next() {
			values, destinations := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			records = append(records, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}
		result[table] = string(raw)
	}
	return result
}

func TestWorkRequestPresenceFilterTCP(t *testing.T) {
	f := newWorkRequestPresenceFixture(t)
	seed := decodeData[domain.WorkFilter](t, f.request(t, http.MethodPost, "/v1/work-record-filters", map[string]any{
		"name": "Original filter", "query": map[string]any{"search": "original", "include_changes": true},
	}, http.StatusOK).testResponse)
	for _, shape := range []struct {
		name, fields string
	}{
		{"missing-query", `"name":"must-not-persist"`},
		{"null-query", `"name":"must-not-persist","query":null`},
		{"array-query", `"name":"must-not-persist","query":[]`},
		{"string-query", `"name":"must-not-persist","query":"wrong"`},
		{"number-query", `"name":"must-not-persist","query":1`},
		{"bool-query", `"name":"must-not-persist","query":false`},
		{"malformed-query-field", `"name":"must-not-persist","query":{"search":false}`},
		{"unknown-query-field", `"name":"must-not-persist","query":{"body":"not accepted"}`},
		{"missing-name", `"query":{}`},
		{"null-name", `"name":null,"query":{}`},
		{"wrong-name-type", `"name":false,"query":{}`},
	} {
		for _, upsert := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/upsert=%t", shape.name, upsert), func(t *testing.T) {
				before := workRequestPresenceSnapshot(t, f.repo)
				fields := shape.fields
				if upsert {
					fields = fmt.Sprintf(`"id":%q,`, seed.ID) + fields
				}
				f.rawRequest(t, http.MethodPost, "/v1/work-record-filters", []byte("{"+fields+"}"), http.StatusBadRequest)
				if after := workRequestPresenceSnapshot(t, f.repo); !reflect.DeepEqual(before, after) {
					t.Fatal("invalid filter request changed saved filters, timestamps or work records")
				}
			})
		}
	}
	empty := decodeData[domain.WorkFilter](t, f.request(t, http.MethodPost, "/v1/work-record-filters", map[string]any{
		"name": "Explicit empty query", "query": map[string]any{},
	}, http.StatusOK).testResponse)
	if empty.ID == "" || empty.ID == seed.ID || !reflect.DeepEqual(empty.Query, domain.WorkQuery{}) {
		t.Fatal("explicit empty query must create a separate valid filter")
	}
	updated := decodeData[domain.WorkFilter](t, f.request(t, http.MethodPost, "/v1/work-record-filters", map[string]any{
		"id": seed.ID, "name": "  Updated filter  ", "query": map[string]any{},
	}, http.StatusOK).testResponse)
	if updated.ID != seed.ID || updated.Name != "Updated filter" || !reflect.DeepEqual(updated.Query, domain.WorkQuery{}) {
		t.Fatal("valid explicit empty-query upsert changed semantics")
	}
	filters := decodeData[domain.Page[domain.WorkFilter]](t, f.request(t, http.MethodGet, "/v1/work-record-filters", nil, http.StatusOK).testResponse)
	if len(filters.Items) != 2 {
		t.Fatal("upsert must update its existing filter, not create a duplicate")
	}
}

func workRequestPresenceNavigation(projectID string, equal bool) map[string]any {
	from := map[string]any{}
	if equal {
		from["project_id"] = projectID
	}
	return map[string]any{
		"id": "navigation-presence", "sequence": 1, "occurred_at": "2026-11-02T09:10:11.123456789+08:00",
		"from": from, "to": map[string]any{"project_id": projectID},
	}
}

func TestWorkRequestPresenceNavigationRejectsIncompleteTCP(t *testing.T) {
	f := newWorkRequestPresenceFixture(t)
	for _, equal := range []bool{false, true} {
		for _, field := range []string{"id", "sequence", "occurred_at", "from", "to"} {
			for _, shape := range []string{"missing", "null", "wrong-type"} {
				t.Run(fmt.Sprintf("equal=%t/%s/%s", equal, field, shape), func(t *testing.T) {
					before := workRequestPresenceSnapshot(t, f.repo)
					body := workRequestPresenceNavigation(f.project.ID, equal)
					switch shape {
					case "missing":
						delete(body, field)
					case "null":
						body[field] = nil
					case "wrong-type":
						body[field] = []any{}
					}
					f.request(t, http.MethodPost, "/v1/work-records/navigation", body, http.StatusBadRequest)
					if after := workRequestPresenceSnapshot(t, f.repo); !reflect.DeepEqual(before, after) {
						t.Fatal("incomplete navigation request changed persisted records")
					}
				})
			}
		}
	}
	for _, equal := range []bool{false, true} {
		for _, tc := range []struct {
			name, field string
			value       any
		}{
			{"empty-id", "id", ""}, {"invalid-id", "id", "contains spaces"}, {"long-id", "id", strings.Repeat("a", 101)},
			{"zero-sequence", "sequence", 0}, {"negative-sequence", "sequence", -1}, {"fractional-sequence", "sequence", 1.5},
			{"empty-time", "occurred_at", ""}, {"invalid-time", "occurred_at", "not-a-time"},
			{"date-only", "occurred_at", "2026-11-02"}, {"invalid-month", "occurred_at", "2026-13-02T00:00:00Z"},
		} {
			t.Run(fmt.Sprintf("equal=%t/%s", equal, tc.name), func(t *testing.T) {
				before := workRequestPresenceSnapshot(t, f.repo)
				body := workRequestPresenceNavigation(f.project.ID, equal)
				body[tc.field] = tc.value
				f.request(t, http.MethodPost, "/v1/work-records/navigation", body, http.StatusBadRequest)
				if after := workRequestPresenceSnapshot(t, f.repo); !reflect.DeepEqual(before, after) {
					t.Fatal("invalid navigation metadata changed persisted records")
				}
			})
		}
	}
	t.Run("empty-top-level-object", func(t *testing.T) {
		before := workRequestPresenceSnapshot(t, f.repo)
		f.request(t, http.MethodPost, "/v1/work-records/navigation", map[string]any{}, http.StatusBadRequest)
		if after := workRequestPresenceSnapshot(t, f.repo); !reflect.DeepEqual(before, after) {
			t.Fatal("empty request changed persisted records")
		}
	})
}

func TestWorkRequestPresenceNavigationValidNoopAndReplayTCP(t *testing.T) {
	f := newWorkRequestPresenceFixture(t)
	for _, empty := range []bool{false, true} {
		body := workRequestPresenceNavigation(f.project.ID, true)
		if empty {
			body["from"], body["to"] = map[string]any{}, map[string]any{}
		}
		before := workRequestPresenceSnapshot(t, f.repo)
		f.request(t, http.MethodPost, "/v1/work-records/navigation", body, http.StatusNoContent)
		if after := workRequestPresenceSnapshot(t, f.repo); !reflect.DeepEqual(before, after) {
			t.Fatal("complete equal navigation must be a no-op")
		}
	}
	f.auth["X-Work-Client"], f.auth["X-Work-Page"], f.auth["X-Work-Entry"] = "synthetic-client", "synthetic-page", "browser"
	body := workRequestPresenceNavigation(f.project.ID, false)
	for range 2 {
		f.request(t, http.MethodPost, "/v1/work-records/navigation", body, http.StatusNoContent)
	}
	page := decodeData[domain.WorkPage](t, f.request(t, http.MethodPost, "/v1/work-records/query", map[string]any{
		"filter": map[string]any{"field": "kind", "operator": "eq", "value": "navigation"},
	}, http.StatusOK).testResponse)
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatal("valid empty-from navigation and its retry must persist exactly one record")
	}
	record := page.Items[0]
	wantTime, err := time.Parse(time.RFC3339Nano, body["occurred_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	gotTime, err := time.Parse(time.RFC3339Nano, record.OccurredAt)
	if err != nil || !gotTime.Equal(wantTime) || record.Sequence != 1 || record.ProjectID != f.project.ID || record.ObjectID != f.project.ID || record.ActorID != f.userID || record.ActorType != "user" || record.Kind != "navigation" || record.ClientID != "synthetic-client" || record.PageID != "synthetic-page" || record.EntryType != "browser" {
		t.Fatal("navigation did not preserve authenticated ownership, time and client metadata")
	}
	afterState, ok := record.After["state"].(map[string]any)
	if !ok || afterState["project_id"] != f.project.ID || afterState["project_name"] != f.project.Name || record.Refs["project_id"] != f.project.ID {
		t.Fatal("navigation location pointer mapping lost its public metadata")
	}
	beforeState, ok := record.Before["state"].(map[string]any)
	if !ok || len(beforeState) != 0 {
		t.Fatal("explicit empty from object must remain an empty source location")
	}
	before := workRequestPresenceSnapshot(t, f.repo)
	body["sequence"], body["occurred_at"] = 99, "2026-11-03T00:00:00Z"
	f.request(t, http.MethodPost, "/v1/work-records/navigation", body, http.StatusNoContent)
	if after := workRequestPresenceSnapshot(t, f.repo); !reflect.DeepEqual(before, after) {
		t.Fatal("duplicate navigation ID rewrote its original record")
	}
	// A rejected observation must not reserve the idempotency ID or create a
	// record with an implicit server timestamp.
	body["id"] = "reusable-after-rejection"
	delete(body, "occurred_at")
	f.request(t, http.MethodPost, "/v1/work-records/navigation", body, http.StatusBadRequest)
	if after := workRequestPresenceSnapshot(t, f.repo); !reflect.DeepEqual(before, after) {
		t.Fatal("missing navigation time partially wrote a record")
	}
	body["occurred_at"] = "2026-11-03T00:00:00Z"
	f.request(t, http.MethodPost, "/v1/work-records/navigation", body, http.StatusNoContent)
	page = decodeData[domain.WorkPage](t, f.request(t, http.MethodPost, "/v1/work-records/query", map[string]any{
		"filter": map[string]any{"field": "kind", "operator": "eq", "value": "navigation"},
	}, http.StatusOK).testResponse)
	if page.Total != 2 {
		t.Fatal("valid navigation could not reuse a rejected observation ID")
	}
}

func TestWorkRequestPresenceInternalRecordDefaultTime(t *testing.T) {
	repo := newTestRepository(t)
	userID := testLocalUserID(repo, t)
	if err := repo.AppendWorkRecord(t.Context(), userID, domain.WorkRecord{
		ID: "system-default-time", Kind: "change", Action: "system.test", ActorType: "system", Outcome: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	record, err := repo.GetWorkRecord(t.Context(), userID, "system-default-time")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339Nano, record.OccurredAt); err != nil {
		t.Fatal("internal system records must retain their default timestamp")
	}
}
