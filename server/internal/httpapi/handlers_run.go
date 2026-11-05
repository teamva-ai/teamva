package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/node/provider/catalog"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/gin-gonic/gin"
)

func (a *API) getRun(c *gin.Context) {
	item, err := a.store.GetRun(c.Request.Context(), authenticatedUser(c).ID, c.Param("run_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) listRunEvents(c *gin.Context) {
	after := int64(0)
	if raw := strings.TrimSpace(c.Query("after")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			respondError(c, domain.Invalid("after 必须是非负事件序号"))
			return
		}
		after = value
	}
	limit := 100
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			respondError(c, domain.Invalid("limit 必须在 1 到 500 之间"))
			return
		}
		limit = value
	}
	page, err := a.store.ListRunEventPage(
		c.Request.Context(), authenticatedUser(c).ID, c.Param("run_id"), after, limit,
		store.MaxRunEventPagePayloadBytes,
	)
	if err != nil {
		respondError(c, err)
		return
	}
	if c.Query("view") == "activity" {
		page.Items = activityRunEvents(page.Items)
	}
	respond(c, http.StatusOK, page, 0)
}

func activityRunEvents(items []domain.RunEvent) []domain.RunEvent {
	result := make([]domain.RunEvent, len(items))
	copy(result, items)
	for index := range result {
		change, ok := catalog.ProjectFileChange(result[index].Type, result[index].Payload)
		if !ok {
			continue
		}
		payload := cloneEventPayloadWithoutPatch(result[index].Payload)
		owner := cloneEventPayloadWithoutPatch(change.Owner)
		if changes, ok := cloneEventChangesWithoutPatch(change.Owner["changes"]); ok {
			owner["changes"] = changes
		}
		if change.OwnerKey != "" {
			payload[change.OwnerKey] = owner
		} else {
			payload = owner
		}
		result[index].Payload = payload
	}
	return result
}

func cloneEventChangesWithoutPatch(value any) ([]any, bool) {
	var source []any
	switch changes := value.(type) {
	case []any:
		source = changes
	case []map[string]any:
		source = make([]any, len(changes))
		for index := range changes {
			source[index] = changes[index]
		}
	default:
		return nil, false
	}
	result := make([]any, len(source))
	for index, raw := range source {
		if change, ok := raw.(map[string]any); ok {
			result[index] = cloneEventPayloadWithoutPatch(change)
		} else {
			result[index] = raw
		}
	}
	return result, true
}

func cloneEventPayloadWithoutPatch(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		if key == "patch" || key == "diff" {
			continue
		}
		result[key] = value
	}
	return result
}

func (a *API) streamRunEvents(c *gin.Context) {
	a.streamRunEventsWithHeartbeat(c, 15*time.Second)
}

func (a *API) streamRunEventsWithHeartbeat(c *gin.Context, heartbeatInterval time.Duration) {
	const eventPageSize = 256
	after := int64(0)
	if raw := c.Query("after_seq"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			respondError(c, domain.Invalid("after_seq 必须是非负整数"))
			return
		}
		after = value
	}
	runID := c.Param("run_id")
	userID := authenticatedUser(c).ID
	var wake <-chan struct{}
	unsubscribe := func() {}
	if subscriber, ok := a.runtime.(RunEventSubscriber); ok {
		wake, unsubscribe = subscriber.SubscribeRunEvents(runID)
	}
	defer unsubscribe()
	page, err := a.store.ListRunEventPage(
		c.Request.Context(), userID, runID, after, eventPageSize,
		store.MaxRunEventPagePayloadBytes,
	)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Writer.Flush()

	var poll <-chan time.Time
	var pollTicker *time.Ticker
	if wake == nil {
		pollTicker = time.NewTicker(time.Second)
		defer pollTicker.Stop()
		poll = pollTicker.C
	}
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	for {
		if len(page.Items) == 0 {
			run, runErr := a.store.GetRun(c.Request.Context(), userID, runID)
			if runErr != nil {
				return
			}
			if domain.IsRunTerminal(run.Status) {
				// A reconnect can already be at the terminal cursor, with no
				// future wakeup. Still re-read after observing terminal so a
				// concurrently committed final event is never lost.
				page, err = a.store.ListRunEventPage(c.Request.Context(), userID, runID, after, eventPageSize, store.MaxRunEventPagePayloadBytes)
				if err != nil || len(page.Items) == 0 {
					return
				}
			}
		}
		for _, event := range page.Items {
			c.SSEvent(event.Type, browserRunEvent(event))
			after = event.Sequence
			if event.Type == "run.completed" || event.Type == "run.finished" {
				c.Writer.Flush()
				return
			}
		}
		c.Writer.Flush()
		if page.Truncated {
			if len(page.Items) == 0 {
				return
			}
			page, err = a.store.ListRunEventPage(
				c.Request.Context(), userID, runID, after, eventPageSize,
				store.MaxRunEventPagePayloadBytes,
			)
			if err != nil {
				return
			}
			continue
		}
		// This page is consumed; heartbeats must never replay it.
		page.Items = nil
		select {
		case <-c.Request.Context().Done():
			return
		case <-wake:
		case <-poll:
		case <-heartbeat.C:
			_, _ = c.Writer.Write([]byte(": heartbeat\n\n"))
			c.Writer.Flush()
			// Drain committed events before considering a terminal Run. A
			// heartbeat may win select concurrently with the completion wakeup.
		}
		page, err = a.store.ListRunEventPage(
			c.Request.Context(), userID, runID, after, eventPageSize,
			store.MaxRunEventPagePayloadBytes,
		)
		if err != nil {
			return
		}
	}
}

func (a *API) stopRun(c *gin.Context) {
	var request struct {
		Mode string `json:"mode" binding:"required"`
	}
	if !bindJSON(c, &request) {
		return
	}
	item, err := a.store.StopRun(c.Request.Context(), authenticatedUser(c).ID, c.Param("run_id"), request.Mode)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusAccepted, item, item.Version)
}

func (a *API) steerRun(c *gin.Context) {
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey == "" {
		respondError(c, domain.Invalid("缺少 Idempotency-Key"))
		return
	}
	var request struct {
		Input       string                `json:"input"`
		Attachments []domain.ContentBlock `json:"attachments"`
		Internal    bool                  `json:"internal"`
	}
	if !bindAttachmentJSON(c, &request) {
		return
	}
	if request.Internal && len(request.Attachments) > 0 {
		respondError(c, domain.Invalid("内部控制消息不能携带附件"))
		return
	}
	if err := validateRequestedAttachments(request.Attachments); err != nil {
		respondError(c, err)
		return
	}
	input := []domain.ContentBlock{{Type: "text", Text: request.Input, Internal: request.Internal}}
	input = append(input, request.Attachments...)
	item, err := a.store.SteerRun(c.Request.Context(), authenticatedUser(c).ID,
		c.Param("run_id"), idempotencyKey, input, request.Internal)
	if err != nil {
		respondError(c, err)
		return
	}
	if a.runtime != nil {
		a.runtime.NotifyIssueActivation(item.IssueID)
	}
	respond(c, http.StatusAccepted, item, item.Version)
}
