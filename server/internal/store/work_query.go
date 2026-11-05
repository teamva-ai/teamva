package store

import (
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

const (
	maxWorkConditions     = 60
	maxWorkConditionDepth = 5
	maxWorkQueryText      = 500
)

// Construct once, not once per condition. These SQL expressions are owned by
// the application; only condition values are supplied by users and bound as args.
var workQueryFields = func() map[string]string {
	fields := make(map[string]string)
	for _, column := range []string{"id", "action", "project_id", "object_type", "object_id", "actor_type", "actor_id", "outcome", "client_id", "page_id", "entry_type", "run_id", "title", "kind", "request_id"} {
		fields[column] = column
	}
	for _, key := range []string{"agent_id", "graph_id", "node_id", "issue_id", "fork_message_id", "parent_id", "agent_revision_id", "assistant_session_id"} {
		fields[key] = "COALESCE(json_extract(refs,'$." + key + "'),'')"
	}
	for _, key := range []string{"project_id", "issue_id", "graph_id", "node_id"} {
		fields["from_"+key] = "COALESCE(json_extract(before_values,'$.state." + key + "'),'')"
		fields["to_"+key] = "COALESCE(json_extract(after_values,'$.state." + key + "'),'')"
	}
	return fields
}()

type workConditionCompiler struct {
	count int
	args  []any
}

func (compiler *workConditionCompiler) compile(condition domain.WorkCondition, depth int) (string, error) {
	compiler.count++
	if compiler.count > maxWorkConditions || depth > maxWorkConditionDepth {
		return "", domain.Invalid("筛选条件过多或嵌套过深")
	}
	if condition.Join != "" {
		if (condition.Join != "and" && condition.Join != "or") || len(condition.Conditions) == 0 || condition.Field != "" || condition.Operator != "" || condition.Value != "" {
			return "", domain.Invalid("条件组不能混入单个条件字段")
		}
		parts := make([]string, 0, len(condition.Conditions))
		for _, child := range condition.Conditions {
			part, err := compiler.compile(child, depth+1)
			if err != nil {
				return "", err
			}
			parts = append(parts, part)
		}
		return "(" + strings.Join(parts, " "+strings.ToUpper(condition.Join)+" ") + ")", nil
	}
	field, allowed := workQueryFields[condition.Field]
	if !allowed || len(condition.Value) > maxWorkQueryText || len(condition.Conditions) != 0 {
		return "", domain.Invalid("无效筛选字段")
	}
	var sql string
	switch condition.Operator {
	case "eq":
		sql = field + " = ?"
	case "ne":
		sql = field + " <> ?"
	case "contains":
		sql = "instr(lower(" + field + "),lower(?)) > 0"
	case "not_contains":
		sql = "instr(lower(" + field + "),lower(?)) = 0"
	default:
		return "", domain.Invalid("无效筛选操作符")
	}
	compiler.args = append(compiler.args, condition.Value)
	return sql, nil
}

func workWhere(query domain.WorkQuery) (string, []any, error) {
	if len(query.Search) > maxWorkQueryText || query.Offset < 0 || query.Offset > 1_000_000 || query.Limit < 0 || query.Limit > 200 {
		return "", nil, domain.Invalid("工作记录查询超过限制")
	}
	parts := []string{"user_id = ?"}
	args := []any{}
	if !query.IncludeChanges {
		parts = append(parts, "kind IN ('operation','navigation')")
	}
	var from, until time.Time
	for _, bound := range []struct {
		value, operator string
		parsed          *time.Time
	}{
		{query.From, ">=", &from}, {query.Until, "<=", &until},
	} {
		if bound.value == "" {
			continue
		}
		stamp, err := time.Parse(time.RFC3339Nano, bound.value)
		if err != nil {
			return "", nil, domain.Invalid("日期必须为 ISO 时间")
		}
		*bound.parsed = stamp
		parts = append(parts, "occurred_at "+bound.operator+" ?")
		args = append(args, sqliteTimeValue(stamp))
	}
	if query.From != "" && query.Until != "" && from.After(until) {
		return "", nil, domain.Invalid("开始时间不能晚于结束时间")
	}
	if query.Search != "" {
		parts = append(parts, `(instr(lower(title),lower(?))>0 OR instr(lower(action),lower(?))>0
		 OR instr(lower(refs),lower(?))>0 OR instr(lower(before_values),lower(?))>0
		 OR instr(lower(after_values),lower(?))>0 OR id=? OR object_id=? OR request_id=? OR run_id=?)`)
		for range 9 {
			args = append(args, query.Search)
		}
	}
	if query.Filter != nil {
		compiler := workConditionCompiler{}
		condition, err := compiler.compile(*query.Filter, 0)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, condition)
		args = append(args, compiler.args...)
	}
	return strings.Join(parts, " AND "), args, nil
}
