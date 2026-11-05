package nodeprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrRunEventTooLarge = errors.New("run event exceeds protocol size limit")

// EncodeRunEvent validates both the database payload and the complete wire
// event. Callers may reuse the returned encodings for hashing/persistence so an
// accepted event cannot later expand past a lower layer's boundary.
func EncodeRunEvent(event RunEvent) (encodedEvent, encodedPayload []byte, err error) {
	event.Payload = RedactInlineImageDataURLs(event.Payload)
	encodedPayload, err = json.Marshal(event.Payload)
	if err != nil {
		return nil, nil, err
	}
	if len(encodedPayload) > MaxRunEventPayloadBytes {
		return nil, nil, fmt.Errorf("%w: payload exceeds %d bytes", ErrRunEventTooLarge, MaxRunEventPayloadBytes)
	}
	encodedEvent, err = json.Marshal(event)
	if err != nil {
		return nil, nil, err
	}
	if len(encodedEvent) > MaxRunEventBytes {
		return nil, nil, fmt.Errorf("%w: event exceeds %d bytes", ErrRunEventTooLarge, MaxRunEventBytes)
	}
	return encodedEvent, encodedPayload, nil
}

func ValidateRunEventSize(event RunEvent) error {
	_, _, err := EncodeRunEvent(event)
	return err
}
