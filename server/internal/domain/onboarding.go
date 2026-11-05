package domain

// Onboarding is the one-time first-run guide state. Needed is derived from the
// current Agent state (no user Agent and no configured Lumi) and is
// never persisted; Dismissed is persisted and only a manual rerun reopens it.
type Onboarding struct {
	Dismissed bool `json:"dismissed"`
	Needed    bool `json:"needed"`
}
