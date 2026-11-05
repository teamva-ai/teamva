package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

type enqueueOnlyRuntime struct{ executions, notifications int }

func (r *enqueueOnlyRuntime) NotifyIssueActivation(string) { r.notifications++ }
func (r *enqueueOnlyRuntime) ExecuteLocalCommand(context.Context, string, any) (json.RawMessage, error) {
	r.executions++
	return nil, errors.New("HTTP handler must not execute an enqueued command")
}

func TestDirectoryHTTPOnlyAdmitsDurableCommandsIncludingHiddenOption(t *testing.T) {
	repository := newTestRepository(t)
	runtime := &enqueueOnlyRuntime{}
	router := newRouter(repository, runtime, "test", true)
	auth := testLoginHeaders(t, repository, router)
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, router, http.MethodPost, "/v1/computers/"+computer.ID+"/directory-list", map[string]any{
		"path": "/tmp", "query": "demo", "show_hidden": true,
	}, auth, http.StatusAccepted)
	created := decodeData[struct {
		ID string `json:"command_id"`
	}](t, response)
	command, ok, err := repository.ClaimLocalCommand(t.Context(), time.Now(), time.Minute)
	if err != nil || !ok || command.ID != created.ID {
		t.Fatalf("queued command: ok=%v err=%v", ok, err)
	}
	var payload struct {
		Path, Query string
		Hidden      bool `json:"show_hidden"`
	}
	if err := json.Unmarshal(command.Payload, &payload); err != nil || !payload.Hidden || payload.Path != "/tmp" || payload.Query != "demo" {
		t.Fatal("picker options were not persisted")
	}
	if runtime.executions != 0 || runtime.notifications != 1 {
		t.Fatalf("executions=%d notifications=%d", runtime.executions, runtime.notifications)
	}
	if err := repository.CompleteLocalCommand(t.Context(), command.ID, command.ClaimToken, json.RawMessage(`{"items":[]}`)); err != nil {
		t.Fatal(err)
	}
	status, _, err := repository.GetDirectoryListResult(t.Context(), user.ID, computer.ID, command.ID)
	if err != nil || status != "completed" {
		t.Fatalf("completed status=%s err=%v", status, err)
	}
}
