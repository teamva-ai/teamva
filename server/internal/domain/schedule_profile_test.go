package domain

import (
	"reflect"
	"testing"
	"time"
)

func scheduleInstant(t *testing.T, value string) time.Time {
	t.Helper()
	result, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDailySchedulePairsAndNormalization(t *testing.T) {
	task := ScheduledTask{ScheduleKind: "daily", Timezone: "Asia/Shanghai", Times: []string{"17:45", "08:15", "08:15"}, IssueMode: "new"}
	if err := NormalizeScheduledTaskConfig(&task); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(task.Times, []string{"08:15", "17:45"}) {
		t.Fatal(task.Times)
	}
	cursor := scheduleInstant(t, "2026-10-22T00:15:00Z")
	for _, expected := range []string{"2026-10-22T09:45:00Z", "2026-10-23T00:15:00Z", "2026-10-23T09:45:00Z"} {
		next, err := NextScheduledTaskTime(task, cursor)
		if err != nil || !next.Equal(scheduleInstant(t, expected)) {
			t.Fatalf("next=%v expected=%s err=%v", next, expected, err)
		}
		cursor = next
	}
}

func TestWeeklyScheduleDSTAndSunday(t *testing.T) {
	for _, test := range []struct {
		name, after, want string
		times             []string
	}{
		{"missing spring hour", "2026-03-08T06:00:00Z", "2026-03-15T06:30:00Z", []string{"02:30"}},
		{"first autumn hour", "2026-11-01T05:00:00Z", "2026-11-01T05:30:00Z", []string{"01:30"}},
		{"second autumn hour", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z", []string{"01:30"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			task := ScheduledTask{ScheduleKind: "weekly", Timezone: "America/New_York", Times: test.times, Weekdays: []int{0, 0}}
			next, err := NextScheduledTaskTime(task, scheduleInstant(t, test.after))
			if err != nil || !next.Equal(scheduleInstant(t, test.want)) {
				t.Fatalf("next=%v err=%v", next, err)
			}
		})
	}
}

func TestScheduleConfigRejectsInvalidAndOnceIsAbsolute(t *testing.T) {
	for _, task := range []ScheduledTask{
		{ScheduleKind: "daily"}, {ScheduleKind: "daily", Times: []string{"8:00"}},
		{ScheduleKind: "daily", Times: []string{"24:00"}}, {ScheduleKind: "weekly", Times: []string{"08:00"}},
		{ScheduleKind: "weekly", Times: []string{"08:00"}, Weekdays: []int{7}},
		{IssueMode: "existing"}, {IssueMode: "new", IssueID: "issue"}, {IssueMode: "bad"},
	} {
		if err := NormalizeScheduledTaskConfig(&task); err == nil {
			t.Fatalf("accepted %+v", task)
		}
	}
	task := ScheduledTask{ScheduleKind: "once", ScheduleExpression: "2030-11-03T01:30:00-04:00", Timezone: "America/New_York"}
	for _, cursor := range []string{"2030-11-01T00:00:00Z", "2030-11-02T00:00:00Z"} {
		next, err := NextScheduledTaskTime(task, scheduleInstant(t, cursor))
		if err != nil || !next.Equal(scheduleInstant(t, "2030-11-03T05:30:00Z")) {
			t.Fatalf("next=%v err=%v", next, err)
		}
	}
	for i := 0; i < 65; i++ {
		task.Times = append(task.Times, time.Date(2026, 1, 1, i/60, i%60, 0, 0, time.UTC).Format("15:04"))
	}
	task.ScheduleKind = "daily"
	if err := NormalizeScheduledTaskConfig(&task); err == nil {
		t.Fatal("accepted over 64 times")
	}
}
