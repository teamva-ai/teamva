package httpapi

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

type schedulePolicyContractFixture struct {
	router     http.Handler
	auth       map[string]string
	policyPath string
}

func newSchedulePolicyContractFixture(t *testing.T) schedulePolicyContractFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	computer := createTestComputer(t, repository, auth, "schedule policy contract")
	project, err := repository.CreateProject(t.Context(), testLocalUserID(repository, t), domain.Project{
		ComputerID: computer.ID, Name: "schedule policy contract",
	})
	if err != nil {
		t.Fatal(err)
	}
	return schedulePolicyContractFixture{
		router: router, auth: auth, policyPath: "/v1/projects/" + project.ID + "/policy-rules",
	}
}

func (fixture schedulePolicyContractFixture) policies(t *testing.T) []domain.PolicyRule {
	t.Helper()
	return decodeData[domain.Page[domain.PolicyRule]](t, request(t, fixture.router, http.MethodGet,
		fixture.policyPath, nil, fixture.auth, http.StatusOK)).Items
}

func (fixture schedulePolicyContractFixture) policy(t *testing.T, id string) domain.PolicyRule {
	t.Helper()
	for _, item := range fixture.policies(t) {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("policy %s was not persisted", id)
	return domain.PolicyRule{}
}

func TestSchedulePreviewCountContract(t *testing.T) {
	fixture := newSchedulePolicyContractFixture(t)
	for _, test := range []struct {
		name       string
		count      any
		omit       bool
		unknown    bool
		status     int
		wantLength int
	}{
		{name: "omitted defaults to five", omit: true, status: http.StatusOK, wantLength: 5},
		{name: "minimum", count: 1, status: http.StatusOK, wantLength: 1},
		{name: "maximum", count: 20, status: http.StatusOK, wantLength: 20},
		{name: "zero", count: 0, status: http.StatusBadRequest},
		{name: "above maximum", count: 21, status: http.StatusBadRequest},
		{name: "negative", count: -1, status: http.StatusBadRequest},
		{name: "null is not omitted", count: nil, status: http.StatusBadRequest},
		{name: "fractional", count: 1.5, status: http.StatusBadRequest},
		{name: "string", count: "1", status: http.StatusBadRequest},
		{name: "unknown field", count: 1, unknown: true, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]any{
				"schedule_kind": "cron", "schedule_expression": "0 12 * * *", "timezone": "UTC",
			}
			if !test.omit {
				body["count"] = test.count
			}
			if test.unknown {
				body["unexpected"] = true
			}
			response := request(t, fixture.router, http.MethodPost, "/v1/scheduled-tasks/preview",
				body, fixture.auth, test.status)
			if test.status != http.StatusOK {
				if response.Error == nil || response.Error.Code != "invalid_argument" {
					t.Fatalf("preview error=%s", response.Recorder.Body.String())
				}
				return
			}
			preview := decodeData[struct {
				Times []time.Time `json:"times"`
			}](t, response)
			if len(preview.Times) != test.wantLength {
				t.Fatalf("preview count=%d, want %d", len(preview.Times), test.wantLength)
			}
			for index := 1; index < len(preview.Times); index++ {
				if !preview.Times[index].After(preview.Times[index-1]) {
					t.Fatalf("preview times are not increasing: %v", preview.Times)
				}
			}
		})
	}
}

func TestPolicyRuleCreateContract(t *testing.T) {
	fixture := newSchedulePolicyContractFixture(t)
	for _, test := range []struct {
		name      string
		expiresAt any
		omit      bool
	}{
		{name: "omitted expiry", omit: true},
		{name: "null expiry", expiresAt: nil},
		{name: "seconds", expiresAt: "2030-02-28T12:34:56Z"},
		{name: "milliseconds and offset", expiresAt: "2030-02-28T12:34:56.120-03:30"},
		{name: "nanoseconds and leap date", expiresAt: "2032-02-29T12:34:56.123456789+05:45"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]any{
				"action_kind": "filesystem.write", "effect": "deny", "conditions": map[string]any{},
			}
			if !test.omit {
				body["expires_at"] = test.expiresAt
			}
			created := decodeData[domain.PolicyRule](t, request(t, fixture.router, http.MethodPost,
				fixture.policyPath, body, fixture.auth, http.StatusCreated))
			if created.Version != 1 || created.Conditions == nil || len(created.Conditions) != 0 || !created.Enabled {
				t.Fatalf("created policy=%+v", created)
			}
			assertPolicyRuleExpiry(t, created, test.expiresAt)
			stored := fixture.policy(t, created.ID)
			if !reflect.DeepEqual(stored, created) {
				t.Fatalf("persisted policy=%+v, response=%+v", stored, created)
			}
			assertPolicyRuleExpiry(t, stored, test.expiresAt)
		})
	}

	for _, test := range []struct {
		name   string
		fields map[string]any
		omit   string
	}{
		{name: "missing conditions", omit: "conditions"},
		{name: "null conditions", fields: map[string]any{"conditions": nil}},
		{name: "array conditions", fields: map[string]any{"conditions": []any{}}},
		{name: "string conditions", fields: map[string]any{"conditions": "{}"}},
		{name: "unknown field", fields: map[string]any{"unexpected": true}},
		{name: "invalid effect", fields: map[string]any{"effect": "ignore"}},
		{name: "empty timestamp", fields: map[string]any{"expires_at": ""}},
		{name: "date without time", fields: map[string]any{"expires_at": "2030-01-01"}},
		{name: "non leap date", fields: map[string]any{"expires_at": "2030-02-29T00:00:00Z"}},
		{name: "invalid month", fields: map[string]any{"expires_at": "2030-13-01T00:00:00Z"}},
		{name: "invalid hour", fields: map[string]any{"expires_at": "2030-01-01T24:00:00Z"}},
		{name: "missing timezone", fields: map[string]any{"expires_at": "2030-01-01T00:00:00"}},
		{name: "boolean timestamp", fields: map[string]any{"expires_at": false}},
		{name: "numeric timestamp", fields: map[string]any{"expires_at": 123}},
		{name: "object timestamp", fields: map[string]any{"expires_at": map[string]any{}}},
		{name: "array timestamp", fields: map[string]any{"expires_at": []any{}}},
		{name: "UTC year overflow", fields: map[string]any{"expires_at": "9999-12-31T23:59:59-01:00"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := fixture.policies(t)
			body := map[string]any{
				"action_kind": "filesystem.write", "effect": "deny", "conditions": map[string]any{},
			}
			for key, value := range test.fields {
				body[key] = value
			}
			if test.omit != "" {
				delete(body, test.omit)
			}
			response := request(t, fixture.router, http.MethodPost, fixture.policyPath,
				body, fixture.auth, http.StatusBadRequest)
			if response.Error == nil || response.Error.Code != "invalid_argument" {
				t.Fatalf("create error=%s", response.Recorder.Body.String())
			}
			if after := fixture.policies(t); !reflect.DeepEqual(before, after) {
				t.Fatalf("invalid create changed policies: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestPolicyRuleCreateActionKindContract(t *testing.T) {
	fixture := newSchedulePolicyContractFixture(t)
	for _, test := range []struct {
		name   string
		fields map[string]any
	}{
		{name: "ASCII space action", fields: map[string]any{"action_kind": " "}},
		{name: "tab and newline action", fields: map[string]any{"action_kind": "\t\n"}},
		{name: "Unicode ideographic space action", fields: map[string]any{"action_kind": "\u3000"}},
		{name: "empty action", fields: map[string]any{"action_kind": ""}},
		{name: "allow leading space", fields: map[string]any{"effect": " allow"}},
		{name: "allow trailing space", fields: map[string]any{"effect": "allow "}},
		{name: "deny leading space", fields: map[string]any{"effect": " deny"}},
		{name: "deny trailing space", fields: map[string]any{"effect": "deny "}},
		{name: "approval leading space", fields: map[string]any{"effect": " require_approval"}},
		{name: "approval trailing space", fields: map[string]any{"effect": "require_approval "}},
		{name: "effect tab and newline", fields: map[string]any{"effect": "\tallow\n"}},
		{name: "effect Unicode whitespace", fields: map[string]any{"effect": "\u3000deny\u3000"}},
		{name: "custom action with null conditions", fields: map[string]any{"conditions": nil}},
		{name: "custom action with array conditions", fields: map[string]any{"conditions": []any{}}},
		{name: "custom action with invalid expiry", fields: map[string]any{"expires_at": "2030-02-29T00:00:00Z"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := fixture.policies(t)
			// Check persistence even when the pre-fix status assertion fails, so
			// the red regression records both 500s and illegal inserted values.
			defer func() {
				if after := fixture.policies(t); !reflect.DeepEqual(before, after) {
					t.Errorf("invalid create changed policies: before=%+v after=%+v", before, after)
				}
			}()
			body := map[string]any{
				"action_kind": " \t\u3000vendor.namespace/custom action v2:部署\u3000\n ", "effect": "deny",
				"conditions": map[string]any{"path_prefix": "must-not-be-written/", "optional": nil},
				"expires_at": "2035-01-01T00:00:00.123456789+05:45",
			}
			for key, value := range test.fields {
				body[key] = value
			}
			response := request(t, fixture.router, http.MethodPost, fixture.policyPath,
				body, fixture.auth, http.StatusBadRequest)
			if response.Error == nil || response.Error.Code != "invalid_argument" {
				t.Fatalf("create error=%s", response.Recorder.Body.String())
			}
		})
	}

	for _, test := range []struct {
		name       string
		actionKind string
		want       string
	}{
		{name: "custom action is not an enum", actionKind: "company.custom:perform", want: "company.custom:perform"},
		{
			name:       "trim outer whitespace without changing custom action",
			actionKind: " \t\u3000vendor.namespace/custom action v2:部署\u3000\n ",
			want:       "vendor.namespace/custom action v2:部署",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			conditions := map[string]any{"path_prefix": "src/", "optional": nil}
			expiry := "2035-01-01T00:00:00.123456789+05:45"
			created := decodeData[domain.PolicyRule](t, request(t, fixture.router, http.MethodPost,
				fixture.policyPath, map[string]any{
					"action_kind": test.actionKind, "effect": "require_approval",
					"conditions": conditions, "expires_at": expiry,
				}, fixture.auth, http.StatusCreated))
			if created.ActionKind != test.want || created.Effect != "require_approval" || created.Version != 1 ||
				!reflect.DeepEqual(created.Conditions, conditions) {
				t.Fatalf("created custom policy=%+v, want action_kind=%q conditions=%+v", created, test.want, conditions)
			}
			assertPolicyRuleExpiry(t, created, expiry)
			if stored := fixture.policy(t, created.ID); !reflect.DeepEqual(created, stored) {
				t.Fatalf("persisted custom policy=%+v, response=%+v", stored, created)
			}
		})
	}
}

func TestPolicyRuleUpdateContract(t *testing.T) {
	fixture := newSchedulePolicyContractFixture(t)
	initialExpiry := "2032-02-29T12:34:56.123456789+05:45"
	created := decodeData[domain.PolicyRule](t, request(t, fixture.router, http.MethodPost,
		fixture.policyPath, map[string]any{
			"action_kind": "filesystem.write", "effect": "require_approval",
			"conditions": map[string]any{"path_prefix": "src/", "optional": nil}, "expires_at": initialExpiry,
		}, fixture.auth, http.StatusCreated))
	path := "/v1/policy-rules/" + created.ID
	current := created
	for _, test := range []struct {
		name       string
		fields     map[string]any
		conditions map[string]any
		expiresAt  any
		enabled    bool
	}{
		{
			name: "omitted conditions and expiry preserve both", fields: map[string]any{"effect": "deny", "enabled": false},
			conditions: created.Conditions, expiresAt: initialExpiry,
		},
		{
			name:       "empty conditions replace and expiry retains nanoseconds",
			fields:     map[string]any{"conditions": map[string]any{}, "expires_at": "2033-03-01T12:34:56.120003004-03:30"},
			conditions: map[string]any{}, expiresAt: "2033-03-01T12:34:56.120003004-03:30",
		},
		{
			name: "explicit null clears expiry", fields: map[string]any{"expires_at": nil},
			conditions: map[string]any{},
		},
		{
			name: "omitted expiry preserves no expiry", fields: map[string]any{"enabled": true},
			conditions: map[string]any{}, enabled: true,
		},
		{
			name: "seconds expiry can be restored", fields: map[string]any{"expires_at": "2034-01-01T00:00:00Z"},
			conditions: map[string]any{}, expiresAt: "2034-01-01T00:00:00Z", enabled: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]any{"expected_version": current.Version}
			for key, value := range test.fields {
				body[key] = value
			}
			updated := decodeData[domain.PolicyRule](t, request(t, fixture.router, http.MethodPatch,
				path, body, fixture.auth, http.StatusOK))
			if updated.Version != current.Version+1 || updated.Effect != "deny" || updated.Enabled != test.enabled ||
				!reflect.DeepEqual(updated.Conditions, test.conditions) {
				t.Fatalf("updated policy=%+v, previous=%+v", updated, current)
			}
			assertPolicyRuleExpiry(t, updated, test.expiresAt)
			if stored := fixture.policy(t, created.ID); !reflect.DeepEqual(stored, updated) {
				t.Fatalf("persisted policy=%+v, response=%+v", stored, updated)
			}
			current = updated
		})
	}

	for _, test := range []struct {
		name   string
		fields map[string]any
		omit   bool
		status int
	}{
		{name: "null conditions cannot partially write", fields: map[string]any{"conditions": nil}},
		{name: "array conditions cannot partially write", fields: map[string]any{"conditions": []any{}}},
		{name: "bad timestamp cannot partially write", fields: map[string]any{"expires_at": "2030-02-29T00:00:00Z"}},
		{name: "empty timestamp cannot partially write", fields: map[string]any{"expires_at": ""}},
		{name: "wrong timestamp type cannot partially write", fields: map[string]any{"expires_at": false}},
		{name: "UTC overflow cannot partially write", fields: map[string]any{"expires_at": "9999-12-31T23:59:59-01:00"}},
		{name: "unknown field cannot partially write", fields: map[string]any{"unexpected": true}},
		{name: "missing version", omit: true},
		{name: "null version", fields: map[string]any{"expected_version": nil}},
		{name: "zero version", fields: map[string]any{"expected_version": 0}},
		{name: "negative version", fields: map[string]any{"expected_version": -1}},
		{name: "string version", fields: map[string]any{"expected_version": "1"}},
		{name: "stale version", fields: map[string]any{"expected_version": created.Version, "expires_at": nil}, status: http.StatusConflict},
		{name: "future version", fields: map[string]any{"expected_version": current.Version + 1, "expires_at": nil}, status: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := fixture.policy(t, created.ID)
			body := map[string]any{
				"expected_version": current.Version, "effect": "allow", "enabled": false,
				"conditions": map[string]any{"path_prefix": "must-not-be-written/"},
				"expires_at": "2035-01-01T00:00:00.123456789Z",
			}
			for key, value := range test.fields {
				body[key] = value
			}
			if test.omit {
				delete(body, "expected_version")
			}
			status, code := http.StatusBadRequest, "invalid_argument"
			if test.status != 0 {
				status, code = test.status, "version_conflict"
			}
			response := request(t, fixture.router, http.MethodPatch, path, body, fixture.auth, status)
			if response.Error == nil || response.Error.Code != code {
				t.Fatalf("update error=%s", response.Recorder.Body.String())
			}
			if after := fixture.policy(t, created.ID); !reflect.DeepEqual(before, after) {
				t.Fatalf("invalid update partially wrote policy: before=%+v after=%+v", before, after)
			}
		})
	}
}

func assertPolicyRuleExpiry(t *testing.T, item domain.PolicyRule, want any) {
	t.Helper()
	if want == nil {
		if item.ExpiresAt != nil {
			t.Fatalf("expiry=%v, want no expiry", item.ExpiresAt)
		}
		return
	}
	expected, err := time.Parse(time.RFC3339Nano, want.(string))
	if err != nil {
		t.Fatal(err)
	}
	if item.ExpiresAt == nil || !item.ExpiresAt.Equal(expected) || item.ExpiresAt.Location() != time.UTC {
		t.Fatalf("expiry=%v, want UTC %v with full nanosecond precision", item.ExpiresAt, expected.UTC())
	}
}
