package nodeprotocol

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

const (
	MaxRunAttachments       = 4
	MaxRunAttachmentBytes   = 512 << 10
	MaxRunAttachmentsBytes  = 768 << 10
	MaxRunAttachmentNameLen = 255
)

var supportedRunAttachmentMIMEs = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/webp": {},
}

func ValidateAttachments(items []Attachment) error {
	if len(items) > MaxRunAttachments {
		return errors.New("too many run attachments")
	}
	totalBytes := 0
	for _, item := range items {
		mimeType := strings.ToLower(strings.TrimSpace(item.MimeType))
		if len(item.MimeType) > 64 || mimeType != item.MimeType {
			return errors.New("run attachment MIME is not normalized")
		}
		if _, ok := supportedRunAttachmentMIMEs[mimeType]; !ok {
			return errors.New("unsupported run attachment MIME")
		}
		filename := strings.TrimSpace(item.Filename)
		if filename == "" || filename != item.Filename || len(filename) > MaxRunAttachmentNameLen || strings.ContainsAny(filename, "/\\\x00") {
			return errors.New("invalid run attachment filename")
		}
		prefix := "data:" + mimeType + ";base64,"
		if len(item.DataURL) > maxEncodedRunAttachmentDataURLBytes() || !strings.HasPrefix(item.DataURL, prefix) {
			return errors.New("invalid run attachment data URL")
		}
		decoded, err := base64.StdEncoding.DecodeString(item.DataURL[len(prefix):])
		if err != nil || len(decoded) == 0 || len(decoded) > MaxRunAttachmentBytes || !matchesAttachmentSignature(mimeType, decoded) {
			return errors.New("invalid run attachment data")
		}
		totalBytes += len(decoded)
		if totalBytes > MaxRunAttachmentsBytes {
			return errors.New("run attachments exceed total byte limit")
		}
	}
	return nil
}

func maxEncodedRunAttachmentDataURLBytes() int {
	return len("data:image/jpeg;base64,") + ((MaxRunAttachmentBytes+2)/3)*4
}

func DecodedAttachmentBytes(item Attachment) (int, error) {
	prefix := "data:" + item.MimeType + ";base64,"
	if !strings.HasPrefix(item.DataURL, prefix) {
		return 0, errors.New("invalid run attachment data URL")
	}
	decoded, err := base64.StdEncoding.DecodeString(item.DataURL[len(prefix):])
	if err != nil {
		return 0, err
	}
	return len(decoded), nil
}

// RedactInlineImageDataURLs returns a detached event payload projection that
// cannot retain an inline image. Providers may echo user input in arbitrary
// nested event fields, so both conventional data_url/url keys and standalone
// string values are covered recursively.
func RedactInlineImageDataURLs(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	return redactInlineImageDataURLValue(payload).(map[string]any)
}

func redactInlineImageDataURLValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if strings.EqualFold(key, "data_url") ||
				(strings.EqualFold(key, "url") && containsInlineImageDataURL(item)) {
				continue
			}
			result[key] = redactInlineImageDataURLValue(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = redactInlineImageDataURLValue(item)
		}
		return result
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(typed, &decoded) == nil {
			return redactInlineImageDataURLValue(decoded)
		}
		if containsInlineImageDataURL(string(typed)) {
			return json.RawMessage(`"[image attachment omitted]"`)
		}
		return append(json.RawMessage(nil), typed...)
	case []byte:
		var decoded any
		if json.Unmarshal(typed, &decoded) == nil {
			return redactInlineImageDataURLValue(decoded)
		}
		if containsInlineImageDataURL(string(typed)) {
			return []byte("[image attachment omitted]")
		}
		return append([]byte(nil), typed...)
	case string:
		if containsInlineImageDataURL(typed) {
			return "[image attachment omitted]"
		}
		return typed
	default:
		return value
	}
}

func containsInlineImageDataURL(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	return ContainsInlineImageDataURL(text)
}

func ContainsInlineImageDataURL(text string) bool {
	text = strings.ToLower(text)
	return strings.Contains(text, "data:image/") && strings.Contains(text, ";base64,")
}

func matchesAttachmentSignature(mimeType string, data []byte) bool {
	switch mimeType {
	case "image/png":
		return len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n"
	case "image/jpeg":
		return len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
	case "image/gif":
		return len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a")
	case "image/webp":
		return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
	default:
		return false
	}
}
