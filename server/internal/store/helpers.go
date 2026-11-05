package store

import (
	"crypto/subtle"
	"strings"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/google/uuid"
)

type scanner interface{ Scan(...any) error }

func newID(prefix string) string {
	id, err := uuid.NewV7()
	if err != nil {
		panic(err)
	}
	return prefix + "_" + id.String()
}

func versionConflict(current int64) *domain.Error {
	return domain.Conflict("version_conflict", "资源版本已变化", map[string]any{"current_version": current})
}

func constantTimeEqual(expected, actual string) bool {
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func boundedMentionFailure(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "mention_retry"
	}
	if len(value) > 512 {
		return value[:512]
	}
	return value
}
