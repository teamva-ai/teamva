package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type localCommandExecutor interface {
	ExecuteLocalCommand(context.Context, string, any) (json.RawMessage, error)
}

// listDirectory enqueues a filesystem.list command on the user's computer and
// returns the command ID. The web client polls it with listDirectoryResult.
func (a *API) listDirectory(c *gin.Context) {
	var request struct {
		Path       string `json:"path"`
		Query      string `json:"query"`
		ShowHidden bool   `json:"show_hidden"`
	}
	if !bindJSON(c, &request) {
		return
	}
	commandID, err := a.store.CreateDirectoryListCommand(
		c.Request.Context(), authenticatedUser(c).ID, c.Param("computer_id"),
		strings.TrimSpace(request.Path), strings.TrimSpace(request.Query), request.ShowHidden,
	)
	if err != nil {
		respondError(c, err)
		return
	}
	a.notifyQueuedCommands()
	respond(c, http.StatusAccepted, gin.H{"command_id": commandID}, 0)
}

// registerResource asks the local runtime to validate and canonicalize the
// directory selected by the user.
func (a *API) registerResource(c *gin.Context) {
	var request struct {
		Path string `json:"path" binding:"required"`
	}
	if !bindJSON(c, &request) {
		return
	}
	commandID, err := a.store.CreateResourceRegisterCommand(
		c.Request.Context(), authenticatedUser(c).ID, c.Param("computer_id"),
		strings.TrimSpace(request.Path),
	)
	if err != nil {
		respondError(c, err)
		return
	}
	a.notifyQueuedCommands()
	respond(c, http.StatusAccepted, gin.H{"command_id": commandID}, 0)
}

// listDirectoryResult returns the status and payload of a filesystem.list or
// resource.register command. Clients poll until it is completed, failed or
// expired.
func (a *API) listDirectoryResult(c *gin.Context) {
	status, result, err := a.store.GetDirectoryListResult(
		c.Request.Context(), authenticatedUser(c).ID,
		c.Param("computer_id"), c.Param("command_id"),
	)
	if err != nil {
		respondError(c, err)
		return
	}
	var payload any
	if len(result) > 0 {
		if err := json.Unmarshal(result, &payload); err != nil {
			payload = string(result)
		}
	}
	respond(c, http.StatusOK, gin.H{"status": status, "result": payload}, 0)
}

func (a *API) notifyQueuedCommands() {
	if a.runtime != nil {
		// Empty issue ID wakes the durable dispatcher without an Issue event.
		a.runtime.NotifyIssueActivation("")
	}
}
