package domain

import (
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// NormalizeScheduledTaskConfig validates the public calendar/profile, without
// defaulting IssueMode: an empty mode is reserved for pre-profile legacy tasks.
func NormalizeScheduledTaskConfig(task *ScheduledTask) error {
	switch task.IssueMode {
	case "":
		if task.IssueID != "" {
			return Invalid("issue_id 需要 existing 模式")
		}
	case "new":
		if task.IssueID != "" {
			return Invalid("new 模式不能指定 issue_id")
		}
	case "existing":
		if strings.TrimSpace(task.IssueID) == "" {
			return Invalid("existing 模式必须指定 issue_id")
		}
	default:
		return Invalid("issue_mode 必须为 new 或 existing")
	}
	if task.ScheduleKind != "daily" && task.ScheduleKind != "weekly" {
		return nil
	}
	unique := map[string]bool{}
	for _, value := range task.Times {
		parsed, err := time.Parse("15:04", value)
		if err != nil || parsed.Format("15:04") != value {
			return Invalid("times 必须使用 HH:mm")
		}
		unique[value] = true
	}
	if len(unique) == 0 || len(unique) > 64 {
		return Invalid("times 必须包含 1 至 64 个时间")
	}
	task.Times = make([]string, 0, len(unique))
	for value := range unique {
		task.Times = append(task.Times, value)
	}
	sort.Strings(task.Times)
	days := map[int]bool{}
	for _, day := range task.Weekdays {
		if day < 0 || day > 6 {
			return Invalid("weekdays 必须为 0 至 6")
		}
		days[day] = true
	}
	if task.ScheduleKind == "weekly" && len(days) == 0 {
		return Invalid("weekly 必须选择星期")
	}
	task.Weekdays = make([]int, 0, len(days))
	for day := range days {
		task.Weekdays = append(task.Weekdays, day)
	}
	sort.Ints(task.Weekdays)
	task.ScheduleExpression = ""
	return nil
}

// NextScheduledTaskTime is the single source for preview and dispatch. Scan
// absolute minutes so nonexistent local times are skipped and both occurrences
// of a repeated DST minute are eligible; hour/minute pairs never form a product.
func NextScheduledTaskTime(task ScheduledTask, after time.Time) (time.Time, error) {
	if err := NormalizeScheduledTaskConfig(&task); err != nil {
		return time.Time{}, err
	}
	if task.ScheduleKind != "daily" && task.ScheduleKind != "weekly" {
		return NextScheduleTime(task.ScheduleKind, task.ScheduleExpression, task.Timezone, after)
	}
	location, err := scheduleLocation(task.Timezone)
	if err != nil {
		return time.Time{}, Invalid("无效 IANA 时区")
	}
	times := map[string]bool{}
	for _, value := range task.Times {
		times[value] = true
	}
	days := map[time.Weekday]bool{}
	for _, day := range task.Weekdays {
		days[time.Weekday(day)] = true
	}
	// More than two weeks covers a skipped DST occurrence even for weekly jobs.
	cursor := after.UTC().Truncate(time.Minute).Add(time.Minute)
	for limit := cursor.Add(16 * 24 * time.Hour); cursor.Before(limit); cursor = cursor.Add(time.Minute) {
		local := cursor.In(location)
		if (task.ScheduleKind == "daily" || days[local.Weekday()]) && times[local.Format("15:04")] {
			return cursor, nil
		}
	}
	return time.Time{}, Invalid("指定日历没有下一次触发时间")
}

// NextScheduleTime is shared by HTTP validation and durable scheduling. Cron's
// Next returns the zero time for syntactically valid but impossible calendars;
// that is an invalid schedule, not an overdue occurrence to run_once.
func NextScheduleTime(kind, expression, timezone string, after time.Time) (time.Time, error) {
	location, err := scheduleLocation(timezone)
	if err != nil {
		return time.Time{}, Invalid("无效 IANA 时区")
	}
	switch kind {
	case "once":
		value, err := time.Parse(time.RFC3339, expression)
		if err != nil {
			return time.Time{}, Invalid("once 表达式必须是 RFC3339 时间")
		}
		if !value.After(after) {
			return time.Time{}, Invalid("once 时间必须晚于当前时间")
		}
		return value.UTC(), nil
	case "cron":
		schedule, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(expression)
		if err != nil {
			return time.Time{}, Invalid("无效 Cron 表达式")
		}
		next := schedule.Next(after.In(location))
		if next.IsZero() || !next.After(after) {
			return time.Time{}, &Error{Code: "schedule_no_next_run", Message: "Cron 表达式在指定时区内没有下一次触发时间", HTTPStatus: 400}
		}
		return next.UTC(), nil
	default:
		return time.Time{}, Invalid("schedule_kind 必须为 once 或 cron")
	}
}

func scheduleLocation(timezone string) (*time.Location, error) {
	if timezone == "" || timezone == "Local" {
		return nil, Invalid("无效 IANA 时区")
	}
	return time.LoadLocation(timezone)
}
