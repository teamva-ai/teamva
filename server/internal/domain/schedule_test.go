package domain

import (
	"errors"
	"testing"
	"time"
)

func TestNextScheduleTimeRejectsImpossibleCronAndKeepsDST(t *testing.T) {
	for _, test := range []struct {
		name       string
		expression string
		zone       string
		after      string
		want       string
	}{
		{"february 31", "0 0 31 2 *", "UTC", "2026-01-01T00:00:00Z", ""},
		{"february 30", "0 0 30 2 *", "Asia/Shanghai", "2026-01-01T00:00:00Z", ""},
		{"leap day", "0 0 29 2 *", "UTC", "2026-01-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"spring gap", "30 2 * * *", "America/New_York", "2026-03-08T05:00:00Z", "2026-03-09T06:30:00Z"},
		{"fall first", "30 1 * * *", "America/New_York", "2026-11-01T04:00:00Z", "2026-11-01T05:30:00Z"},
		{"fall repeated", "30 1 * * *", "America/New_York", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			after, err := time.Parse(time.RFC3339, test.after)
			if err != nil {
				t.Fatal(err)
			}
			next, err := NextScheduleTime("cron", test.expression, test.zone, after)
			if test.want == "" {
				var invalid *Error
				if !errors.As(err, &invalid) || invalid.Code != "schedule_no_next_run" || !next.IsZero() {
					t.Fatalf("impossible calendar next=%v err=%v", next, err)
				}
				return
			}
			if err != nil || next.Format(time.RFC3339) != test.want {
				t.Fatalf("next=%v err=%v, want %s", next, err, test.want)
			}
		})
	}
}
