package store

import (
	"bytes"
	"errors"
	"net/http"
	"strings"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// RunEventWire splits a compact protocol event around its exact payload bytes.
// Persisting the small framing strings lets the database verify both sizes
// without storing a second complete copy of a potentially 8 MiB event.
type RunEventWire struct {
	Event        []byte
	Payload      []byte
	Prefix       []byte
	Suffix       []byte
	EventBytes   int
	PayloadBytes int
}

// EncodeRunEventWire delegates the authoritative limits and compact encoding to
// protocol/node, then derives verifiable persistence framing from that result.
func EncodeRunEventWire(event nodeprotocol.RunEvent) (RunEventWire, error) {
	event.Payload = nodeprotocol.RedactInlineImageDataURLs(event.Payload)
	encodedEvent, encodedPayload, err := nodeprotocol.EncodeRunEvent(event)
	if errors.Is(err, nodeprotocol.ErrRunEventTooLarge) {
		return RunEventWire{}, runEventTooLargeError()
	}
	if err != nil {
		return RunEventWire{}, &domain.Error{
			Code: "invalid_run_event", Message: "Run event 无法编码",
			HTTPStatus: http.StatusUnprocessableEntity, Retryable: false,
		}
	}
	payloadMarker := []byte(`"payload":`)
	payloadStart := bytes.Index(encodedEvent, payloadMarker)
	if payloadStart < 0 {
		return RunEventWire{}, &domain.Error{
			Code: "invalid_run_event", Message: "Run event wire 缺少 payload",
			HTTPStatus: http.StatusUnprocessableEntity, Retryable: false,
		}
	}
	payloadStart += len(payloadMarker)
	payloadEnd := payloadStart + len(encodedPayload)
	if payloadEnd > len(encodedEvent) ||
		!bytes.Equal(encodedEvent[payloadStart:payloadEnd], encodedPayload) {
		return RunEventWire{}, &domain.Error{
			Code: "invalid_run_event", Message: "Run event wire payload 不一致",
			HTTPStatus: http.StatusUnprocessableEntity, Retryable: false,
		}
	}
	return RunEventWire{
		Event: encodedEvent, Payload: encodedPayload,
		Prefix: encodedEvent[:payloadStart], Suffix: encodedEvent[payloadEnd:],
		EventBytes: len(encodedEvent), PayloadBytes: len(encodedPayload),
	}, nil
}

func runEventTooLargeError() error {
	return &domain.Error{
		Code: "run_event_too_large", Message: "单个 Run event 超过协议大小上限",
		HTTPStatus: http.StatusRequestEntityTooLarge, Retryable: false,
		Details: map[string]any{
			"max_event_bytes":   nodeprotocol.MaxRunEventBytes,
			"max_payload_bytes": nodeprotocol.MaxRunEventPayloadBytes,
		},
	}
}

// MapRunEventWriteError turns database wire-integrity violations into bounded,
// non-retryable protocol errors. It never includes SQL text or payload data.
func MapRunEventWriteError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, nodeprotocol.ErrRunEventTooLarge) {
		return runEventTooLargeError()
	}
	if strings.Contains(strings.ToLower(err.Error()), "run_event_wire_size_invalid") {
		return &domain.Error{
			Code: "invalid_run_event", Message: "Run event wire 元数据不可信",
			HTTPStatus: http.StatusUnprocessableEntity, Retryable: false,
		}
	}
	return err
}
