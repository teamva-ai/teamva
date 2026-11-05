package domain

import (
	"testing"
	"time"
)

func BenchmarkNextScheduledTaskTime(b *testing.B) {
	for _, test := range []struct {
		name  string
		task  ScheduledTask
		after string
	}{
		{"daily", ScheduledTask{ScheduleKind: "daily", Timezone: "Asia/Shanghai", Times: []string{"08:15"}}, "2026-10-22T00:15:00Z"},
		{"weekly", ScheduledTask{ScheduleKind: "weekly", Timezone: "America/New_York", Times: []string{"02:30"}, Weekdays: []int{0}}, "2026-03-01T07:30:00Z"},
	} {
		b.Run(test.name, func(b *testing.B) {
			after, err := time.Parse(time.RFC3339, test.after)
			if err != nil {
				b.Fatal(err)
			}
			for _, batch := range []struct {
				name string
				size int
			}{{"1_task", 1}, {"100_tasks", 100}, {"1000_tasks", 1000}} {
				b.Run(batch.name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						for range batch.size {
							if _, err := NextScheduledTaskTime(test.task, after); err != nil {
								b.Fatal(err)
							}
						}
					}
				})
			}
		})
	}
}
