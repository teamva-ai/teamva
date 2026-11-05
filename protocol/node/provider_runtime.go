package nodeprotocol

// ProviderRuntimeRecycleResult reports a synchronous idle-instance recycle.
// RemainingInstances includes active instances that were deliberately left
// untouched; callers may retry after those Runs finish.
type ProviderRuntimeRecycleResult struct {
	Provider           string `json:"provider"`
	RecycledInstances  int    `json:"recycled_instances"`
	ActiveInstances    int    `json:"active_instances"`
	RemainingInstances int    `json:"remaining_instances"`
}

// ProviderExecutable reports the executable currently used by one Provider.
// Source explains where Path came from: a user-configured path ("custom"), the
// TEAMVA_<ID>_PATH environment variable ("environment"), the app-bundled
// runtime ("bundled"), or the manifest command resolved through PATH ("path").
type ProviderExecutable struct {
	Provider string `json:"provider"`
	Path     string `json:"path"`
	Source   string `json:"source"`
}
