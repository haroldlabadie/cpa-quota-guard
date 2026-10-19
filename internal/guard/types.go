package guard

import (
	"context"
	"encoding/json"
)

const Owner = "cpa-quota-guard-v2"
const FamilyOwner = "cpa-quota-guard-family-v1"
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
	Disabled bool           `json:"disabled"`
	Owner    *string        `json:"quota_guard_owner"`
	Reason   *string        `json:"quota_guard_reason"`
	Family   *FamilyControl `json:"quota_guard_family_control"`
	Excluded []string       `json:"excluded_models"`
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
	Name          string              `json:"name"`
	Provider      string              `json:"provider"`
	AuthIndex     string              `json:"auth_index"`
	Identity      string              `json:"identity"`
	Sample        *Sample             `json:"sample,omitempty"`
	Error         string              `json:"error,omitempty"`
	SnapshotError string              `json:"snapshot_error,omitempty"`
	Blocked       bool                `json:"blocked"`
	Reasons       []string            `json:"reasons"`
	FamilyReasons map[string][]string `json:"family_reasons,omitempty"`
	Manual        bool                `json:"manual"`
	Action        string              `json:"action,omitempty"`
	UpdatedMS     int64               `json:"updated_ms"`
}
type Legacy struct {
	Version  int                        `json:"version"`
	Accounts map[string]json.RawMessage `json:"accounts"`
}
type State struct {
	Initialization *Initialization        `json:"initialization,omitempty"`
	Schema         int                    `json:"schema"`
	Plugin         string                 `json:"plugin"`
	Migration      json.RawMessage        `json:"migration,omitempty"`
	Legacy         Legacy                 `json:"legacy"`
	Ownership      map[string]*Ownership  `json:"ownership_proofs"`
	Observations   map[string]Observation `json:"observations"`
}
type Initialization struct {
	Kind      string `json:"kind"`
	CreatedMS int64  `json:"created_ms"`
}

func NewState() State {
	return State{Schema: 1, Plugin: "cpa-quota-guard", Legacy: Legacy{Version: 2, Accounts: map[string]json.RawMessage{}}, Ownership: map[string]*Ownership{}, Observations: map[string]Observation{}}
}

type Backend interface {
	List(context.Context) ([]Entry, error)
	Read(context.Context, Entry) (Account, error)
	Patch(context.Context, Account, map[string]any) error
	Probe(context.Context, Account) (Sample, error)
}
