package guard

import (
	"context"
	"encoding/json"
)

const Owner = "cpa-quota-guard-v2"
const FamilyOwner = "cpa-quota-guard-family-v1"
const Schema = 2
const MaxJournal = 1024
const FiveHourMS int64 = 18000000
const WeekMS int64 = 604800000

var Patterns = map[string][]string{"gemini": {"gemini-*"}, "third_party": {"claude-*", "gpt-*"}}

type Rule struct {
	Email           string  `json:"email"`
	Provider        string  `json:"provider"`
	Enabled         bool    `json:"enabled"`
	ReserveFiveHour float64 `json:"reserve_five_hour"`
	ReserveWeekly   float64 `json:"reserve_weekly"`
}
type Settings struct {
	Schema int    `json:"schema"`
	Rules  []Rule `json:"rules"`
}
type Window struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Scope      string  `json:"scope"`
	Remaining  float64 `json:"remaining"`
	EndMS      int64   `json:"end_ms"`
	DurationMS int64   `json:"duration_ms"`
}
type Sample struct {
	ObservedMS int64             `json:"observed_ms"`
	Windows    []Window          `json:"windows"`
	Plan       string            `json:"plan,omitempty"`
	Sources    map[string]string `json:"sources,omitempty"`
}
type Entry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AuthIndex   string `json:"auth_index"`
	Provider    string `json:"provider"`
	Type        string `json:"type"`
	Email       string `json:"email"`
	Disabled    bool   `json:"disabled"`
	RuntimeOnly bool   `json:"runtime_only"`
	Source      string `json:"source"`
}
type FamilyControl struct {
	Owner   string   `json:"owner"`
	Base    []string `json:"base_excluded_models"`
	Blocked []string `json:"blocked_scopes"`
}
type Protected struct {
	Disabled   bool           `json:"disabled"`
	Owner      *string        `json:"quota_guard_owner"`
	Reason     *string        `json:"quota_guard_reason"`
	Family     *FamilyControl `json:"quota_guard_family_control"`
	Excluded   []string       `json:"excluded_models"`
	MutationID *string        `json:"quota_guard_mutation_id,omitempty"`
}

// Snapshot contains digests and routing controls, never OAuth credentials.
type Snapshot struct {
	Identity      string    `json:"identity"`
	Protected     Protected `json:"protected"`
	ProtectedHash string    `json:"protected_hash"`
	TokenHash     string    `json:"token_hash"`
	OtherHash     string    `json:"other_hash"`
	MtimeNS       int64     `json:"mtime_ns"`
}
type Account struct {
	Entry     Entry
	Snapshot  Snapshot
	AccountID string
	ProjectID string
}
type Ownership struct {
	Disable *Snapshot `json:"disable,omitempty"`
	Family  *Snapshot `json:"family,omitempty"`
}
type Observation struct {
	Name             string              `json:"name"`
	Provider         string              `json:"provider"`
	AuthIndex        string              `json:"auth_index"`
	Email            string              `json:"email"`
	Identity         string              `json:"identity"`
	Sample           *Sample             `json:"sample,omitempty"`
	Error            string              `json:"error,omitempty"`
	SnapshotError    string              `json:"snapshot_error,omitempty"`
	Blocked          bool                `json:"blocked"`
	Reasons          []string            `json:"reasons"`
	FamilyReasons    map[string][]string `json:"family_reasons,omitempty"`
	Manual           bool                `json:"manual"`
	Action           string              `json:"action,omitempty"`
	TransactionID    string              `json:"transaction_id,omitempty"`
	TransactionPhase string              `json:"transaction_phase,omitempty"`
	OrphanReason     string              `json:"orphan_reason,omitempty"`
	UpdatedMS        int64               `json:"updated_ms"`
}
type Legacy struct {
	Version  int                        `json:"version"`
	Accounts map[string]json.RawMessage `json:"accounts"`
}

// Transaction is a persisted intent. It contains only sanitized snapshots and
// protected metadata; raw OAuth credentials are never stored.
type Transaction struct {
	ID        string    `json:"id"`
	CreatedMS int64     `json:"created_ms"`
	Kind      string    `json:"kind"`
	Account   string    `json:"account"`
	Identity  string    `json:"identity"`
	Old       Snapshot  `json:"old_snapshot"`
	Prior     Ownership `json:"prior_ownership"`
	Target    Protected `json:"target_protected"`
	Phase     string    `json:"phase"`
	Reason    string    `json:"reason,omitempty"`
}
type Conflict struct {
	ID           string    `json:"id"`
	Account      string    `json:"account"`
	Identity     string    `json:"identity"`
	Kind         string    `json:"kind"`
	Reason       string    `json:"reason"`
	Observed     Protected `json:"observed_protected"`
	ObservedHash string    `json:"observed_hash"`
	CreatedMS    int64     `json:"created_ms"`
}

// RetiredTransaction is a terminal audit record after an explicit manual
// decision. Transaction preserves the complete original intent, including its
// old identity and prior ownership. ObservedIdentity and Observed preserve the
// conflict-time identity and protected fields, which may differ after a
// same-name credential replacement. A retired record is never re-adopted by
// the worker; current restrictions remain outside plugin ownership.
type RetiredTransaction struct {
	ID               string       `json:"id"`
	Account          string       `json:"account"`
	Identity         string       `json:"identity"`
	ObservedIdentity string       `json:"observed_identity"`
	Kind             string       `json:"kind"`
	Reason           string       `json:"reason"`
	Observed         Protected    `json:"observed_protected"`
	ObservedHash     string       `json:"observed_hash"`
	Transaction      *Transaction `json:"transaction"`
	RetiredMS        int64        `json:"retired_ms"`
}
type Transition struct {
	ID          string `json:"id"`
	Account     string `json:"account"`
	Kind        string `json:"kind"`
	Phase       string `json:"phase"`
	Reason      string `json:"reason,omitempty"`
	CreatedMS   int64  `json:"created_ms"`
	CompletedMS int64  `json:"completed_ms"`
	OldHash     string `json:"old_hash"`
	TargetHash  string `json:"target_hash"`
}
type State struct {
	Initialization *Initialization         `json:"initialization,omitempty"`
	Schema         int                     `json:"schema"`
	Plugin         string                  `json:"plugin"`
	Migration      json.RawMessage         `json:"migration,omitempty"`
	Legacy         Legacy                  `json:"legacy"`
	Ownership      map[string]*Ownership   `json:"ownership_proofs"`
	Transactions   map[string]*Transaction `json:"transactions"`
	Conflicts      map[string]*Conflict    `json:"conflicts"`
	Retired        []RetiredTransaction    `json:"retired"`
	Journal        []Transition            `json:"journal"`
	Observations   map[string]Observation  `json:"observations"`
}
type Initialization struct {
	Kind      string `json:"kind"`
	CreatedMS int64  `json:"created_ms"`
}

func NewState() State {
	return State{Schema: Schema, Plugin: "cpa-quota-guard", Legacy: Legacy{Version: 2, Accounts: map[string]json.RawMessage{}}, Ownership: map[string]*Ownership{}, Transactions: map[string]*Transaction{}, Conflicts: map[string]*Conflict{}, Retired: []RetiredTransaction{}, Journal: []Transition{}, Observations: map[string]Observation{}}
}

type Backend interface {
	List(context.Context) ([]Entry, error)
	Read(context.Context, Entry) (Account, error)
	Patch(context.Context, Account, map[string]any) error
	Probe(context.Context, Account) (Sample, error)
}
