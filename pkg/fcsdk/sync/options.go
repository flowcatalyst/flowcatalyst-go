package sync

// Options controls a Synchronizer.Sync call.
//
// RemoveUnlisted toggles the ?removeUnlisted=true flag on every
// per-category endpoint: when true, the platform archives/removes any
// SDK-sourced rows that aren't in the submitted list. UI-sourced rows
// are never touched. Default false.
//
// SyncX flags are per-category skip switches. Empty categories on the
// DefinitionSet are also implicitly skipped regardless of these flags.
type Options struct {
	RemoveUnlisted    bool
	SyncRoles         bool
	SyncEventTypes    bool
	SyncSubscriptions bool
	SyncDispatchPools bool
	SyncPrincipals    bool
	SyncProcesses     bool
}

// DefaultOptions enables every category, RemoveUnlisted=false.
func DefaultOptions() Options {
	return Options{
		SyncRoles:         true,
		SyncEventTypes:    true,
		SyncSubscriptions: true,
		SyncDispatchPools: true,
		SyncPrincipals:    true,
		SyncProcesses:     true,
	}
}

// WithRemoveUnlisted returns DefaultOptions with RemoveUnlisted=true.
func WithRemoveUnlisted() Options {
	o := DefaultOptions()
	o.RemoveUnlisted = true
	return o
}

// NoneOptions disables every category. Use as a starting point for
// selectively enabling one or two.
func NoneOptions() Options { return Options{} }

// RolesOnly returns Options enabling only the roles category.
func RolesOnly() Options { return Options{SyncRoles: true} }

// EventTypesOnly returns Options enabling only the event types category.
func EventTypesOnly() Options { return Options{SyncEventTypes: true} }

// SubscriptionsOnly returns Options enabling only the subscriptions category.
func SubscriptionsOnly() Options { return Options{SyncSubscriptions: true} }

// DispatchPoolsOnly returns Options enabling only the dispatch pools category.
func DispatchPoolsOnly() Options { return Options{SyncDispatchPools: true} }

// PrincipalsOnly returns Options enabling only the principals category.
func PrincipalsOnly() Options { return Options{SyncPrincipals: true} }

// ProcessesOnly returns Options enabling only the processes category.
func ProcessesOnly() Options { return Options{SyncProcesses: true} }
