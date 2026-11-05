package provider

// FileChange identifies an explicit provider change report. HTTP remains
// responsible for path authorization, noise filtering and byte budgets.
type FileChange struct {
	Source          string
	ItemID          string
	TurnID          string
	Owner           map[string]any
	OwnerKey        string
	AllowStatusOnly bool
}

func FirstString(owner map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, _ := owner[key].(string); value != "" {
			return value
		}
	}
	return ""
}
