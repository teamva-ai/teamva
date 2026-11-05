package store

import (
	"net/http"
	"strconv"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

const (
	// MaxRunEventPagePayloadBytes bounds exact compact payload wire bytes selected
	// by SQL and decoded by one Store page. Overflow rows are inspected through
	// covering size metadata only; their payload values are never fetched.
	MaxRunEventPagePayloadBytes = nodeprotocol.MaxRunEventPayloadBytes
	MaxFinalAssistantTextBytes  = 1 << 20
	MaxTerminalEventScanCount   = 5_000
)

func normalizeRunEventPageRequest(after int64, limit, maxPayloadBytes int) error {
	if after < 0 {
		return domain.Invalid("after sequence must be non-negative")
	}
	if limit < 1 || limit > 1000 {
		return domain.Invalid("event page limit must be between 1 and 1000")
	}
	if maxPayloadBytes < 1 || maxPayloadBytes > MaxRunEventPagePayloadBytes {
		return domain.Invalid("event page payload budget is invalid")
	}
	return nil
}

func runEventPageItemTooLarge(maxPayloadBytes int) error {
	return &domain.Error{
		Code: "run_event_too_large", Message: "单个 Run event payload 超过分页字节预算",
		HTTPStatus: http.StatusRequestEntityTooLarge, Retryable: false,
		Details: map[string]any{"max_payload_bytes": maxPayloadBytes},
	}
}

func finishRunEventPage(items []domain.RunEvent, truncated bool) domain.RunEventPage {
	if items == nil {
		items = []domain.RunEvent{}
	}
	page := domain.RunEventPage{Items: items, Truncated: truncated}
	if truncated && len(items) > 0 {
		page.NextCursor = strconv.FormatInt(items[len(items)-1].Sequence, 10)
	}
	return page
}
