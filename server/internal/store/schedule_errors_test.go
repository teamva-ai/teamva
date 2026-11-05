package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestScheduleDispatchFailureCodeNeverPersistsRawDiagnostics(t *testing.T) {
	if got := ScheduleDispatchFailureCode(errors.New("secret filesystem path")); got != "schedule_dispatch_failed" {
		t.Fatalf("raw failure code=%q", got)
	}
	got := ScheduleDispatchFailureCode(&domain.Error{Code: strings.Repeat("x", 200)})
	if len(got) != 128 {
		t.Fatalf("bounded code bytes=%d", len(got))
	}
}
