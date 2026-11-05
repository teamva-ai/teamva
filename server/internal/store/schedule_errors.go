package store

import (
	"errors"
	"strings"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// ScheduleDispatchFailureCode is safe to persist and expose. It deliberately
// excludes raw SQL, Provider, filesystem and command error text.
func ScheduleDispatchFailureCode(err error) string {
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		code := strings.TrimSpace(domainErr.Code)
		if code != "" {
			if len(code) > 128 {
				code = code[:128]
			}
			return code
		}
	}
	return "schedule_dispatch_failed"
}
