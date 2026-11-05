package store

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

type runPageCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func encodeRunPageCursor(item domain.Run) string {
	payload, _ := json.Marshal(runPageCursor{
		CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano),
		ID:        item.ID,
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeRunPageCursor(value string) (time.Time, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, "", nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(payload) > 1024 {
		return time.Time{}, "", domain.Invalid("无效的 Run 分页游标")
	}
	var cursor runPageCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.ID == "" || len(cursor.ID) > 256 {
		return time.Time{}, "", domain.Invalid("无效的 Run 分页游标")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt)
	if err != nil {
		return time.Time{}, "", domain.Invalid("无效的 Run 分页游标")
	}
	return createdAt.UTC(), cursor.ID, nil
}

func finishRunPage(items []domain.Run, limit int) domain.Page[domain.Run] {
	page := domain.Page[domain.Run]{Items: items}
	if len(items) <= limit {
		return page
	}
	page.Items = items[:limit]
	page.NextCursor = encodeRunPageCursor(page.Items[len(page.Items)-1])
	return page
}
