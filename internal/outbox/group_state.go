package outbox

import "sync"

// GroupStatus is a message group's processing state in the operational state
// machine. Mirrors Rust message_group_processor's ProcessorState.
type GroupStatus string

const (
	GroupRunning GroupStatus = "RUNNING"
	GroupPaused  GroupStatus = "PAUSED"
	GroupBlocked GroupStatus = "BLOCKED"
)

// GroupInfo is a snapshot of one group's state (for the status/admin surface).
type GroupInfo struct {
	Group         string      `json:"group"`
	Status        GroupStatus `json:"status"`
	BlockedItemID string      `json:"blockedItemId,omitempty"`
	Error         string      `json:"error,omitempty"`
}

type groupState struct {
	status        GroupStatus
	blockedItemID string
	err           string
}

// GroupStateManager holds per-message-group processing state — the operational
// state machine (Running / Paused / Blocked). It lives in memory on the
// leader-gated processor (single active poller), mirroring Rust's per-group
// MessageGroupProcessor state + pause/resume/unblock/skip controls. Absent
// groups default to Running. Safe for concurrent use.
type GroupStateManager struct {
	mu     sync.RWMutex
	groups map[string]*groupState
}

// NewGroupStateManager builds an empty manager (all groups default Running).
func NewGroupStateManager() *GroupStateManager {
	return &GroupStateManager{groups: make(map[string]*groupState)}
}

// IsActive reports whether a group may dispatch right now (Running — not Paused
// or Blocked).
func (m *GroupStateManager) IsActive(group string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	g, ok := m.groups[group]
	return !ok || g.status == GroupRunning
}

// Block marks a group Blocked on a poison item (a permanent dispatch failure).
// The group stays blocked until Unblock/Skip.
func (m *GroupStateManager) Block(group, itemID, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.groups[group] = &groupState{status: GroupBlocked, blockedItemID: itemID, err: errMsg}
}

// Pause transitions Running → Paused (no-op when Blocked or already Paused).
func (m *GroupStateManager) Pause(group string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.groups[group]
	if g == nil {
		m.groups[group] = &groupState{status: GroupPaused}
		return
	}
	if g.status == GroupRunning {
		g.status = GroupPaused
	}
}

// Resume transitions Paused → Running (no-op otherwise).
func (m *GroupStateManager) Resume(group string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.groups[group]; ok && g.status == GroupPaused {
		delete(m.groups, group) // back to default Running
	}
}

// ClearBlock transitions Blocked → Running and returns the blocked item id (so
// the caller can decide whether to re-queue it). Returns ok=false if the group
// wasn't Blocked. Backs both Unblock (re-queue the poison) and Skip (abandon it).
func (m *GroupStateManager) ClearBlock(group string) (itemID string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.groups[group]
	if g == nil || g.status != GroupBlocked {
		return "", false
	}
	itemID = g.blockedItemID
	delete(m.groups, group)
	return itemID, true
}

// Snapshot returns every group with a non-default (non-Running) state.
func (m *GroupStateManager) Snapshot() []GroupInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]GroupInfo, 0, len(m.groups))
	for g, s := range m.groups {
		out = append(out, GroupInfo{Group: g, Status: s.status, BlockedItemID: s.blockedItemID, Error: s.err})
	}
	return out
}

// Blocked returns only the Blocked groups.
func (m *GroupStateManager) Blocked() []GroupInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []GroupInfo
	for g, s := range m.groups {
		if s.status == GroupBlocked {
			out = append(out, GroupInfo{Group: g, Status: s.status, BlockedItemID: s.blockedItemID, Error: s.err})
		}
	}
	return out
}
