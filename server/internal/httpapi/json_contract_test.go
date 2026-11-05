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
	"unicode/utf8"

	"github.com/teamva-ai/teamva/node/mcpmanager"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

type jsonContractEmbedded struct {
	Title *string `json:"title"`
}

type jsonContractItem struct {
	Text string `json:"text"`
}

type jsonContractRequest struct {
	jsonContractEmbedded
	Name           string                      `json:"name"`
	Count          int                         `json:"count"`
	Enabled        bool                        `json:"enabled"`
	Optional       *string                     `json:"optional"`
	Nullable       *int64                      `json:"nullable" jsonnullable:"true"`
	NullableNested *jsonContractItem           `json:"nullable_nested" jsonnullable:"true"`
	Nested         *jsonContractItem           `json:"nested"`
	Items          []jsonContractItem          `json:"items"`
	Pointers       []*jsonContractItem         `json:"pointers"`
	Labels         map[string]string           `json:"labels"`
	Records        map[string]jsonContractItem `json:"records"`
	Free           map[string]any              `json:"free"`
	FreeRawMap     map[string]json.RawMessage  `json:"free_raw_map"`
	FreeRawItems   []json.RawMessage           `json:"free_raw_items"`
	Raw            json.RawMessage             `json:"raw"`
	NullableRaw    json.RawMessage             `json:"nullable_raw" jsonnullable:"true"`
	Any            any                         `json:"any"`
	At             time.Time                   `json:"at"`
}

func jsonContractContext(body string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/contract", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, recorder
}

func assertJSONContractError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var response testResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode contract error: %v", err)
	}
	if recorder.Code != status || response.Error == nil || response.Error.Code != code {
		t.Fatalf("contract error: status=%d error=%+v, want %d %s", recorder.Code, response.Error, status, code)
	}
}

func TestJSONContractRejectsExplicitNullBeforeDecoding(t *testing.T) {
	for index, body := range []string{
		`null`, `[]`, `"object"`, `false`, `1`,
		`{"name":null}`, `{"count":null}`, `{"enabled":null}`, `{"optional":null}`,
		`{"title":null}`, `{"nested":null}`, `{"nested":{"text":null}}`,
		`{"nullable_nested":{"text":null}}`, `{"\u006eame":null}`,
		`{"items":null}`, `{"items":[null]}`, `{"items":[{"text":null}]}`,
		`{"pointers":[null]}`, `{"pointers":[{"text":null}]}`,
		`{"labels":null}`, `{"labels":{"fixture":null}}`,
		`{"records":{"fixture":null}}`, `{"records":{"fixture":{"text":null}}}`,
		`{"free":null}`, `{"raw":null}`, `{"at":null}`,
		`{"free_raw_map":null}`, `{"free_raw_items":null}`,
		`{"Name":null}`, `{"TITLE":null}`, `{"nEsTeD":{"tExT":null}}`,
		// Inspect every occurrence: duplicate fields must not hide an invalid
		// null, even when encoding/json would overwrite or merge that field.
		`{"name":null,"name":"valid"}`, `{"name":"valid","name":null}`,
		`{"nested":{"text":null},"nested":{}}`,
		`{"name":"valid sibling","optional":null}`,
	} {
		t.Run(fmt.Sprintf("case_%02d", index), func(t *testing.T) {
			t.Parallel()
			target := jsonContractRequest{Name: "unchanged"}
			c, recorder := jsonContractContext(body)
			if bindJSON(c, &target) {
				t.Fatal("non-nullable request was accepted")
			}
			assertJSONContractError(t, recorder, http.StatusBadRequest, "invalid_argument")
			if target.Name != "unchanged" || target.Nested != nil {
				t.Fatal("null validation partially decoded the target")
			}
		})
	}
}

func TestJSONContractPreservesOptionalNullableAndFreeJSONValues(t *testing.T) {
	for index, body := range []string{
		`{}`, `{"name":"null is text, not a null token"}`, `{"name":"\\\"null\\\""}`,
		`{"name":"","count":0,"enabled":false,"optional":""}`,
		`{"nullable":null,"nullable_raw":null}`, `{"nullable":1000}`,
		`{"nullable_nested":null}`, `{"nullable_nested":{"text":"valid"}}`,
		`{"nested":{},"items":[],"pointers":[],"labels":{},"records":{}}`,
		`{"title":"embedded","nested":{"text":"valid"},"items":[{"text":"valid"}]}`,
		`{"free":{"nil":null,"nested":[null,{"text":null}]},"raw":{"field":null},"any":null}`,
		`{"raw":[null,{"field":null}],"any":{"nested":[null]}}`,
		`{"free_raw_map":{"value":null},"free_raw_items":[null,{"value":null}]}`,
		`{"at":"2026-11-02T00:00:00Z"}`,
		// Deliberately retain the standard decoder's existing key matching and
		// duplicate non-null semantics, rather than adding unrelated strictness.
		`{"NAME":"case insensitive","Title":"embedded case"}`,
		`{"name":"first","name":"last"}`,
		`{"nested":{"text":"first"},"nested":{}}`,
	} {
		t.Run(fmt.Sprintf("case_%02d", index), func(t *testing.T) {
			t.Parallel()
			var target jsonContractRequest
			c, _ := jsonContractContext(body)
			if !bindJSON(c, &target) {
				t.Fatal("valid optional/nullable/free JSON was rejected")
			}
			if body == `{"name":"first","name":"last"}` && target.Name != "last" {
				t.Fatal("duplicate non-null field semantics changed")
			}
		})
	}
	var free map[string]any
	c, _ := jsonContractContext(`{"arbitrary":null,"tree":[{"value":null}]}`)
	if !bindJSON(c, &free) || free["arbitrary"] != nil {
		t.Fatal("unconstrained map payload no longer accepts null values")
	}
	for _, body := range []string{`null`, `[]`} {
		var target map[string]any
		c, recorder := jsonContractContext(body)
		if bindJSON(c, &target) {
			t.Fatal("top-level map request must still be an object")
		}
		assertJSONContractError(t, recorder, http.StatusBadRequest, "invalid_argument")
	}
}

func TestJSONContractPromotedFieldAndTagResolution(t *testing.T) {
	type embedded struct {
		Value *string `json:"value"`
	}
	type shadowed struct {
		embedded
		Value *string `json:"value" jsonnullable:"true"`
	}
	type named struct {
		embedded `json:"patch"`
	}
	type untagged struct{ Value *string }
	type tagged struct {
		Value *string `json:"Value" jsonnullable:"true"`
	}
	type dominant struct {
		untagged
		tagged
	}
	type recursive struct {
		*recursive
		Value *string `json:"value"`
	}
	for _, test := range []struct {
		name, body string
		target     any
		accepted   bool
	}{
		{"shadowing nullable field", `{"value":null}`, &shadowed{}, true},
		{"tag dominance", `{"Value":null}`, &dominant{}, true},
		{"named anonymous field", `{"patch":{"value":null}}`, &named{}, false},
		{"named anonymous outer null", `{"patch":null}`, &named{}, false},
		{"named anonymous valid", `{"patch":{"value":"valid"}}`, &named{}, true},
		{"recursive anonymous field", `{"value":null}`, &recursive{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, recorder := jsonContractContext(test.body)
			if got := bindJSON(c, test.target); got != test.accepted {
				t.Fatalf("accepted=%v, want %v", got, test.accepted)
			}
			if !test.accepted {
				assertJSONContractError(t, recorder, http.StatusBadRequest, "invalid_argument")
			}
		})
	}
}

func TestJSONContractRetainsStrictDecodeAndBindingValidation(t *testing.T) {
	for index, body := range []string{
		``, `{`, `{"name":}`, `{"unknown":null}`, `{"nested":{"unknown":null}}`,
		`{} {}`, `{} null`, `{} []`, `{} trailing`,
		`{"name":1}`, `{"count":"1"}`, `{"count":1.5}`, `{"enabled":"false"}`,
		`{"nullable":"1000"}`, `{"nested":"object"}`, `{"items":{}}`,
	} {
		t.Run(fmt.Sprintf("case_%02d", index), func(t *testing.T) {
			var target jsonContractRequest
			c, recorder := jsonContractContext(body)
			if bindJSON(c, &target) {
				t.Fatal("invalid request was accepted")
			}
			assertJSONContractError(t, recorder, http.StatusBadRequest, "invalid_argument")
		})
	}
	for _, body := range []string{`{}`, `{"name":""}`, `{"name":"valid","expected_version":0}`} {
		var target struct {
			Name            string `json:"name" binding:"required"`
			ExpectedVersion int64  `json:"expected_version" binding:"required,min=1"`
		}
		c, recorder := jsonContractContext(body)
		if bindJSON(c, &target) {
			t.Fatal("binding validation was bypassed")
		}
		assertJSONContractError(t, recorder, http.StatusBadRequest, "invalid_argument")
	}
}

func TestJSONContractKeepsRequestByteLimits(t *testing.T) {
	const normalLimit = 1 << 20
	for _, test := range []struct {
		name       string
		body       string
		attachment bool
		status     int
	}{
		{"normal exact bound", `{"name":"` + strings.Repeat("x", normalLimit-len(`{"name":""}`)) + `"}`, false, http.StatusOK},
		{"normal oversized", `{"name":"` + strings.Repeat("x", normalLimit) + `"}`, false, http.StatusRequestEntityTooLarge},
		{"normal oversized whitespace", `{}` + strings.Repeat(" ", normalLimit), false, http.StatusRequestEntityTooLarge},
		{"attachment above normal", `{"name":"` + strings.Repeat("x", normalLimit+1) + `"}`, true, http.StatusOK},
		{"attachment oversized", `{"name":"` + strings.Repeat("x", maxAttachmentJSONRequestBytes) + `"}`, true, http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			var target jsonContractRequest
			c, recorder := jsonContractContext(test.body)
			accepted := false
			if test.attachment {
				accepted = bindAttachmentJSON(c, &target)
			} else {
				accepted = bindJSON(c, &target)
			}
			if test.status == http.StatusOK {
				if !accepted {
					t.Fatal("request inside byte budget was rejected")
				}
			} else {
				if accepted {
					t.Fatal("oversized request was accepted")
				}
				assertJSONContractError(t, recorder, test.status, "request_too_large")
				if target.Name != "" {
					t.Fatal("oversized request mutated target")
				}
			}
		})
	}
}

func TestJSONContractPreservesSafeWorkMetadata(t *testing.T) {
	for _, body := range []string{`{"name":"safe","raw":{"private":null}}`, `{"name":"safe","optional":null}`} {
		var target jsonContractRequest
		c, _ := jsonContractContext(body)
		metadata := map[string]any{"existing": "unchanged"}
		c.Set("work_input", metadata)
		accepted := bindJSON(c, &target)
		want := map[string]any{"existing": "unchanged"}
		if accepted {
			want["name"] = "safe"
			// Embedded optional fields are marshalled as null, and workMetadata
			// deliberately excludes them and arbitrary payload fields.
		}
		if !reflect.DeepEqual(metadata, want) {
			t.Fatal("request metadata changed or included arbitrary payload values")
		}
	}
}

func TestJSONContractLocalSessionRequiresOneEmptyObject(t *testing.T) {
	repo := newTestRepository(t)
	router := newRouterWithOrigins(repo, nil, "test", true, []string{testBrowserOrigin})
	for _, body := range []string{`null`, `[]`, `"object"`, `{"unknown":null}`, `{} {}`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/local-session", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:45678"
		req.Header.Set("Origin", testBrowserOrigin)
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		assertJSONContractError(t, recorder, http.StatusBadRequest, "invalid_argument")
		if len(recorder.Result().Cookies()) != 0 {
			t.Fatal("invalid bootstrap request issued a session cookie")
		}
	}
	var sessions int
	if err := repo.db.QueryRowContext(t.Context(), `SELECT count(*) FROM web_sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("invalid bootstrap created sessions: count=%d err=%v", sessions, err)
	}
	_ = testLoginHeaders(t, repo, router) // The existing {} bootstrap remains valid.
}

func TestJSONContractProjectNullPatchDoesNotPartiallyWrite(t *testing.T) {
	repo := newTestRepository(t)
	router := newRouterWithOrigins(repo, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repo, router)
	userID := testLocalUserID(repo, t)
	before, err := repo.CreateProject(t.Context(), userID, domain.Project{
		ComputerID: computerID(t, repo), Name: "original", Description: "original description",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/projects/" + before.ID
	for _, fields := range []map[string]any{
		{"name": nil}, {"description": nil},
		{"name": nil, "description": "must not save"},
		{"name": "must not save", "description": nil},
	} {
		fields["expected_version"] = before.Version
		response := request(t, router, http.MethodPatch, path, fields, auth, http.StatusBadRequest)
		if response.Error == nil || response.Error.Code != "invalid_argument" {
			t.Fatal("null project patch did not return invalid_argument")
		}
		after, err := repo.GetProject(t.Context(), userID, before.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("null patch changed project content/version/timestamps: err=%v", err)
		}
	}
	updated := decodeData[domain.Project](t, request(t, router, http.MethodPatch, path,
		map[string]any{"name": "valid", "expected_version": before.Version}, auth, http.StatusOK))
	if updated.Name != "valid" || updated.Description != before.Description || updated.Version != before.Version+1 {
		t.Fatal("omitted optional project field semantics changed")
	}
}

func TestJSONContractInspirationNullDoesNotWriteOrConsumeIdempotencyKey(t *testing.T) {
	repo := newTestRepository(t)
	router := newRouterWithOrigins(repo, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repo, router)
	auth["Idempotency-Key"] = "json-contract-inspiration"
	userID := testLocalUserID(repo, t)
	response := request(t, router, http.MethodPost, "/v1/inspirations",
		map[string]any{"title": "valid", "markdown": nil}, auth, http.StatusBadRequest)
	if response.Error == nil || response.Error.Code != "invalid_argument" {
		t.Fatal("null inspiration markdown did not return invalid_argument")
	}
	var count int
	if err := repo.db.QueryRowContext(t.Context(), `SELECT count(*) FROM inspirations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid inspiration create persisted a record: count=%d err=%v", count, err)
	}
	body := map[string]any{"title": "original", "markdown": "original markdown"}
	before := decodeData[domain.Inspiration](t, request(t, router, http.MethodPost, "/v1/inspirations", body, auth, http.StatusCreated))
	replayed := decodeData[domain.Inspiration](t, request(t, router, http.MethodPost, "/v1/inspirations", body, auth, http.StatusCreated))
	if !reflect.DeepEqual(before, replayed) {
		t.Fatal("valid retry did not preserve the inspiration idempotency contract")
	}
	path := "/v1/inspirations/" + before.ID
	for _, fields := range []map[string]any{
		{"markdown": nil}, {"markdown": nil, "title": "must not save"},
		{"title": nil, "markdown": "must not save"}, {"project_id": nil}, {"completed": nil, "title": "must not save"},
	} {
		fields["expected_version"] = before.Version
		request(t, router, http.MethodPatch, path, fields, auth, http.StatusBadRequest)
		after, err := repo.GetInspiration(t.Context(), userID, before.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("null patch changed inspiration content/version/timestamps: err=%v", err)
		}
	}
	updated := decodeData[domain.Inspiration](t, request(t, router, http.MethodPatch, path,
		map[string]any{"title": "valid", "expected_version": before.Version}, auth, http.StatusOK))
	if updated.Title != "valid" || updated.Markdown != before.Markdown || updated.Version != before.Version+1 {
		t.Fatal("omitted optional inspiration fields no longer preserve their contents")
	}
}

func TestJSONContractConversationGoalAllowsNullableTokenBudget(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit_null=%v", explicit), func(t *testing.T) {
			repo, router, auth, graph, user := conversationForkHTTPFixture(t)
			nodeID := graph.Nodes[0].ID
			auth["Idempotency-Key"] = "json-contract-nullable-goal"
			body := map[string]any{"objective": "valid nullable budget"}
			if explicit {
				body["token_budget"] = nil
			}
			goal := decodeData[domain.ConversationGoal](t, request(t, router, http.MethodPost,
				"/v1/conversation-nodes/"+nodeID+"/goals", body, auth, http.StatusAccepted))
			stored, err := repo.GetCurrentConversationGoal(t.Context(), user.ID, nodeID)
			if err != nil || stored == nil || stored.ID != goal.ID {
				t.Fatalf("nullable conversation goal was not persisted: err=%v", err)
			}
		})
	}
}

func TestJSONContractMCPLeavesConfigSchemaToManager(t *testing.T) {
	// MCP's config is a private RawMessage with its own oneOf/type validation.
	// The generic decoder must not recursively reinterpret or log that payload.
	for _, config := range []string{
		`{"type":"remote","url":"https://example.test/mcp","oauth":false}`,
		`{"type":"remote","url":"https://example.test/mcp","oauth":{"client_secret":"null"}}`,
		`{"arbitrary":[null,{"value":null}]}`,
	} {
		body := `{"name":"fixture","scope":"user","version":0,"config":` + config + `}`
		var target mcpmanager.Input
		c, _ := jsonContractContext(body)
		if !bindJSON(c, &target) || !bytes.Equal(target.Config, []byte(config)) {
			t.Fatal("generic decoder altered or interpreted private MCP config")
		}
	}
	var target mcpmanager.Input
	c, recorder := jsonContractContext(`{"name":"fixture","scope":"user","version":0,"config":null}`)
	if bindJSON(c, &target) {
		t.Fatal("non-nullable outer MCP config accepted null")
	}
	assertJSONContractError(t, recorder, http.StatusBadRequest, "invalid_argument")
}

// Unlike request(), this helper never marshals the input: malformed transport
// bytes must reach the real HTTP router without being rewritten by encoding/json.
func jsonContractHTTPBytes(t *testing.T, client *http.Client, origin, method, path string, body []byte, headers map[string]string) (int, testResponse) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, origin+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal("construct raw JSON HTTP request")
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("send raw JSON HTTP request")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal("read raw JSON HTTP response")
	}
	var envelope testResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal("decode raw JSON HTTP response")
	}
	return response.StatusCode, envelope
}

func TestJSONContractInvalidUTF8HTTPDoesNotPartiallyWrite(t *testing.T) {
	repo := newTestRepository(t)
	router := newRouterWithOrigins(repo, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repo, router)
	userID := testLocalUserID(repo, t)
	project, err := repo.CreateProject(t.Context(), userID, domain.Project{
		ComputerID: computerID(t, repo), Name: "original", Description: "original description",
	})
	if err != nil {
		t.Fatal("create UTF-8 project fixture")
	}
	inspiration, err := repo.CreateInspiration(t.Context(), userID, "json-utf8-original", domain.Inspiration{
		Title: "original", Markdown: "original markdown",
	})
	if err != nil {
		t.Fatal("create UTF-8 inspiration fixture")
	}
	server := httptest.NewServer(router)
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	for _, test := range []struct {
		name, method, path, body string
	}{
		{"project invalid name", http.MethodPatch, "/v1/projects/" + project.ID,
			fmt.Sprintf("{\"expected_version\":%d,\"name\":\"\xff\",\"description\":\"valid sibling\"}", project.Version)},
		{"project invalid description", http.MethodPatch, "/v1/projects/" + project.ID,
			fmt.Sprintf("{\"expected_version\":%d,\"name\":\"valid sibling\",\"description\":\"\xff\"}", project.Version)},
		{"inspiration invalid patch markdown", http.MethodPatch, "/v1/inspirations/" + inspiration.ID,
			fmt.Sprintf("{\"expected_version\":%d,\"title\":\"valid sibling\",\"markdown\":\"\xff\"}", inspiration.Version)},
		{"inspiration invalid create markdown", http.MethodPost, "/v1/inspirations",
			"{\"title\":\"valid sibling\",\"markdown\":\"\xff\"}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			auth["Idempotency-Key"] = "json-utf8-invalid-create"
			status, envelope := jsonContractHTTPBytes(t, client, server.URL, test.method, test.path, []byte(test.body), auth)
			if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != "invalid_argument" {
				t.Errorf("invalid UTF-8 status=%d, want 400 invalid_argument", status)
			}
			afterProject, err := repo.GetProject(t.Context(), userID, project.ID)
			if err != nil || !reflect.DeepEqual(project, afterProject) {
				t.Error("invalid UTF-8 changed project content, version or timestamps")
			}
			afterInspiration, err := repo.GetInspiration(t.Context(), userID, inspiration.ID)
			if err != nil || !reflect.DeepEqual(inspiration, afterInspiration) {
				t.Error("invalid UTF-8 changed inspiration content, version or timestamps")
			}
			var count int
			if err := repo.db.QueryRowContext(t.Context(), `SELECT count(*) FROM inspirations`).Scan(&count); err != nil || count != 1 {
				t.Error("invalid UTF-8 create persisted an inspiration")
			}
		})
	}
	// The rejected create must not consume its key, and valid UTF-8 must be
	// retained byte-for-byte rather than blocking Chinese, emoji or U+FFFD text.
	status, envelope := jsonContractHTTPBytes(t, client, server.URL, http.MethodPost, "/v1/inspirations",
		[]byte(`{"title":"合法中文 🧪","markdown":"正文 👩🏽‍💻 �"}`), auth)
	if status != http.StatusCreated {
		t.Fatalf("valid Unicode create using rejected key status=%d, want 201", status)
	}
	created := decodeData[domain.Inspiration](t, envelope)
	if created.Title != "合法中文 🧪" || created.Markdown != "正文 👩🏽‍💻 �" {
		t.Fatal("valid Unicode fields were rewritten")
	}
}

func TestJSONContractRejectsInvalidUTF8InTypedAndFreePayloads(t *testing.T) {
	invalidSequences := [][]byte{
		{0xff}, {0x80}, {0xe2, 0x82}, {0xc0, 0xaf}, {0xed, 0xa0, 0x80}, {0xf4, 0x90, 0x80, 0x80},
	}
	for sequenceIndex, invalid := range invalidSequences {
		for shapeIndex, shape := range []struct{ prefix, suffix string }{
			{`{"name":"`, `","optional":"valid sibling"}`},
			{`{"name":"valid sibling","optional":"`, `"}`},
			{`{"name":"valid sibling","free":{"`, `":null}}`},
			{`{"name":"valid sibling","free":{"nested":[null,{"value":"`, `"}]}}`},
			{`{"name":"valid sibling","raw":{"nested":"`, `"}}`},
			{`{"name":"valid sibling","any":"`, `"}`},
		} {
			for _, binder := range []struct {
				name string
				bind func(*gin.Context, any) bool
			}{{"normal", bindJSON}, {"attachment", bindAttachmentJSON}} {
				t.Run(fmt.Sprintf("sequence_%d_shape_%d_%s", sequenceIndex, shapeIndex, binder.name), func(t *testing.T) {
					body := append([]byte(shape.prefix), invalid...)
					body = append(body, shape.suffix...)
					target := jsonContractRequest{Name: "unchanged"}
					c, recorder := jsonContractContext(string(body))
					metadata := map[string]any{"existing": "unchanged"}
					c.Set("work_input", metadata)
					if binder.bind(c, &target) {
						t.Fatal("malformed UTF-8 was accepted or rewritten")
					}
					assertJSONContractError(t, recorder, http.StatusBadRequest, "invalid_argument")
					if target.Name != "unchanged" || target.Optional != nil || target.Free != nil || target.Raw != nil || target.Any != nil {
						t.Fatal("malformed UTF-8 partially decoded the request")
					}
					if !reflect.DeepEqual(metadata, map[string]any{"existing": "unchanged"}) {
						t.Fatal("malformed UTF-8 changed work metadata")
					}
				})
			}
		}
	}
	var free map[string]any
	c, recorder := jsonContractContext("{\"arbitrary\":\"\xff\"}")
	if bindJSON(c, &free) || free != nil {
		t.Fatal("unconstrained map accepted or rewrote malformed UTF-8")
	}
	assertJSONContractError(t, recorder, http.StatusBadRequest, "invalid_argument")
}

func TestJSONContractPreservesUnicodeInTypedAndFreePayloads(t *testing.T) {
	const text = "中文 👩🏽‍💻 🧪 �"
	const body = `{"name":"中文 👩🏽‍💻 🧪 �","free":{"中文":null,"emoji":"中文 👩🏽‍💻 🧪 �"},"raw":{"nested":"中文 👩🏽‍💻 🧪 �"}}`
	var target jsonContractRequest
	c, _ := jsonContractContext(body)
	if !bindJSON(c, &target) || target.Name != text || target.Free["emoji"] != text || target.Free["中文"] != nil {
		t.Fatal("valid Unicode or arbitrary null map values were rejected or changed")
	}
	if !bytes.Equal(target.Raw, []byte(`{"nested":"中文 👩🏽‍💻 🧪 �"}`)) {
		t.Fatal("valid UTF-8 RawMessage bytes changed")
	}
	var free map[string]any
	c, _ = jsonContractContext(`{"中文":[null,"👩🏽‍💻"],"replacement":"�"}`)
	if !bindJSON(c, &free) || free["replacement"] != "�" {
		t.Fatal("valid unconstrained Unicode map was rejected or changed")
	}
}

func FuzzCommonJSONContract(f *testing.F) {
	// A small explicit budget exercises the same bounded binder without making
	// every mutation allocate a full 1 MiB. Production-size bounds have separate
	// regression tests. All seeds are synthetic and contain no credentials.
	const byteBudget = 1024
	for _, seed := range [][]byte{
		[]byte(`{}`), []byte(`null`), []byte(`[]`), []byte(`{"name":null}`),
		[]byte(`{"count":"wrong type"}`), []byte(`{"unknown":true}`),
		[]byte(`{"nested":{"text":null}}`), []byte(`{"items":[{"text":"中文 🧪"}]}`),
		[]byte(`{"free":{"value":null},"raw":[null,{"nested":null}]}`),
		[]byte(`{"expires_at":null}`), []byte(`{"expires_at":"2030-02-28T12:34:56Z"}`),
		[]byte("{\"name\":\"\xff\",\"optional\":\"valid\"}"),
		[]byte("{\"free\":{\"value\":\"\xff\"}}"),
		[]byte(`{} {}`), []byte(`{} null`),
		[]byte(`{"name":"` + strings.Repeat("x", byteBudget) + `"}`),
		[]byte(`{}` + strings.Repeat(" ", byteBudget)),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		target := struct {
			jsonContractRequest
			ExpiresAt json.RawMessage `json:"expires_at" jsonnullable:"true"`
		}{jsonContractRequest: jsonContractRequest{Name: "unchanged"}}
		c, recorder := jsonContractContext(string(body))
		metadata := map[string]any{"existing": "unchanged"}
		c.Set("work_input", metadata)
		accepted := bindJSONBytes(c, &target, byteBudget)
		if accepted {
			if !utf8.Valid(body) || len(body) > byteBudget || !json.Valid(body) {
				t.Fatal("accepted request violates UTF-8, byte budget or single-document JSON contract")
			}
			var object map[string]json.RawMessage
			if json.Unmarshal(body, &object) != nil || object == nil {
				t.Fatal("accepted request is not a non-null JSON object")
			}
		} else {
			if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("invalid JSON status=%d, want 400 or 413", recorder.Code)
			}
			if !reflect.DeepEqual(metadata, map[string]any{"existing": "unchanged"}) {
				t.Fatal("rejected JSON changed work metadata")
			}
		}
		if !utf8.Valid(body) && (accepted || target.Name != "unchanged" || target.Free != nil || target.Raw != nil) {
			t.Fatal("malformed UTF-8 was accepted or partially decoded")
		}
	})
}
