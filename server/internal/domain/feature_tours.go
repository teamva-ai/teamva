package domain

// FeatureTours is a user's global, grow-only set of displayed feature tours.
// It is independent of onboarding, projects, devices and preference versions.
type FeatureTours struct {
	Seen []string `json:"seen"`
}

// FeatureTourIDs returns the fixed presentation order, not alphabetical order.
func FeatureTourIDs() []string {
	return []string{"overview", "issues", "conversations", "resources", "memories", "schedules", "changes"}
}

func IsFeatureTourID(id string) bool {
	for _, known := range FeatureTourIDs() {
		if id == known {
			return true
		}
	}
	return false
}
