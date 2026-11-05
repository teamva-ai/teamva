package store

import (
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestWorkQueryRejectsAmbiguousConditionShapes(t *testing.T) {
	leaf := domain.WorkCondition{Field: "action", Operator: "eq", Value: "create"}
	for _, filter := range []domain.WorkCondition{
		{Join: "and", Conditions: []domain.WorkCondition{leaf}, Field: "ignored"},
		{Join: "or", Conditions: []domain.WorkCondition{leaf}, Value: "ignored"},
		{Field: "action", Operator: "eq", Value: "create", Conditions: []domain.WorkCondition{leaf}},
	} {
		if _, _, err := workWhere(domain.WorkQuery{Filter: &filter}); err == nil {
			t.Fatal("ambiguous condition silently accepted")
		}
	}
}

func BenchmarkWorkQueryConditions(b *testing.B) {
	conditions := make([]domain.WorkCondition, 40)
	for i := range conditions {
		conditions[i] = domain.WorkCondition{Field: "project_id", Operator: "eq", Value: "project"}
	}
	query := domain.WorkQuery{Filter: &domain.WorkCondition{Join: "or", Conditions: conditions}}
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := workWhere(query); err != nil {
			b.Fatal(err)
		}
	}
}
