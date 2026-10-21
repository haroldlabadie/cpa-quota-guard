package guard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const MaxFile = 8 << 20

func StrictJSON(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > MaxFile {
		return errors.New("JSON size invalid")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(target); err != nil {
		return errors.New("invalid JSON document")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}
func ValidateSettings(s Settings) error {
	if s.Schema != 1 || s.Rules == nil || len(s.Rules) > 1000 {
		return errors.New("unsupported settings schema/rules")
	}
	seen := map[string]bool{}
	for _, r := range s.Rules {
		email := strings.TrimSpace(strings.ToLower(r.Email))
		if email == "" || !strings.Contains(email, "@") || email != r.Email || len(email) > 320 {
			return errors.New("invalid normalized email")
		}
		if !SupportedProvider(r.Provider) {
			return errors.New("invalid provider")
		}
		if len(r.Scopes) > 0 {
			supported := map[string]bool{}
			for _, scope := range SupportedScopes(r.Provider) {
				supported[scope] = true
			}
			seenScopes := map[string]bool{}
			for _, scope := range r.Scopes {
				if !supported[scope] || seenScopes[scope] {
					return errors.New("invalid or duplicate model scope")
				}
				seenScopes[scope] = true
			}
		}
		if r.ReserveFiveHour < 0 || r.ReserveFiveHour >= 100 || r.ReserveWeekly < 0 || r.ReserveWeekly >= 100 {
			return errors.New("reserve must be within [0,100)")
		}
		key := email + "/" + r.Provider
		if seen[key] {
			return errors.New("duplicate account rule")
		}
		seen[key] = true
	}
	return nil
}
func AtomicJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errors.New("encode state failed")
	}
	b = append(b, '\n')
	if len(b) > MaxFile {
		return errors.New("state exceeds size limit")
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing symlink state")
	}
	f, err := os.CreateTemp(dir, ".guard-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func LoadState(path string) (State, error) {
	s, _, err := LoadStateUpgrade(path)
	return s, err
}

// LoadStateUpgrade accepts schema 1 and schema 2. The caller persists the
// upgraded representation only after acquiring worker.lock and only in apply
// mode; observe never writes state.
func LoadStateUpgrade(path string) (State, bool, error) {
	s := NewState()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	s, original, err := parseState(b)
	if err == nil {
		PruneJournal(&s)
	}
	return s, original != Schema && err == nil, err
}
func ParseState(b []byte) (State, error) {
	s, _, err := parseState(b)
	return s, err
}
func parseState(b []byte) (State, int, error) {
	var s State
	if err := StrictJSON(b, &s); err != nil {
		return s, 0, err
	}
	original := s.Schema
	if s.Schema != 1 && s.Schema != Schema {
		return s, original, errors.New("unsupported/corrupt guard state")
	}
	if s.Plugin != "cpa-quota-guard" || s.Legacy.Version != 2 || s.Legacy.Accounts == nil || s.Ownership == nil || s.Observations == nil {
		return s, original, errors.New("unsupported/corrupt guard state")
	}
	if s.Migration != nil && !validRawObject(s.Migration) {
		return s, original, errors.New("invalid migration provenance")
	}
	for name, raw := range s.Legacy.Accounts {
		if name == "" || !validRawObject(raw) {
			return s, original, errors.New("invalid legacy account evidence")
		}
	}
	if s.Initialization != nil && (s.Initialization.Kind != "fresh-install" || s.Initialization.CreatedMS <= 0) {
		return s, original, errors.New("invalid initialization marker")
	}
	if s.Transactions == nil {
		s.Transactions = map[string]*Transaction{}
	}
	if s.Conflicts == nil {
		s.Conflicts = map[string]*Conflict{}
	}
	if s.Retired == nil {
		s.Retired = []RetiredTransaction{}
	}
	if s.Journal == nil {
		s.Journal = []Transition{}
	}
	if len(s.Journal) > MaxJournal {
		return s, original, errors.New("transition journal exceeds capacity")
	}
	for name, o := range s.Ownership {
		if name == "" || o == nil {
			return s, original, errors.New("invalid ownership")
		}
		for _, p := range []*Snapshot{o.Disable, o.Family} {
			if p != nil && (p.Identity == "" || p.MtimeNS <= 0 || p.ProtectedHash != Digest(p.Protected)) {
				return s, original, errors.New("invalid ownership proof")
			}
		}
	}
	for name, tx := range s.Transactions {
		if !validTransaction(name, tx) {
			return s, original, errors.New("invalid pending transaction")
		}
	}
	for name, c := range s.Conflicts {
		if name == "" || c == nil || c.Account != name || c.ID == "" || c.Identity == "" || c.Reason == "" || c.CreatedMS <= 0 || c.ObservedHash != Digest(c.Observed) {
			return s, original, errors.New("invalid sticky conflict")
		}
	}
	for i, r := range s.Retired {
		if r.ID == "" || r.Account == "" || r.Identity == "" || r.Kind == "" || r.Reason == "" || r.RetiredMS <= 0 || r.ObservedHash != Digest(r.Observed) {
			return s, original, fmt.Errorf("invalid retired transaction %d", i)
		}
		if r.Transaction == nil {
			// Retired summaries written before the complete-transaction format
			// remain loadable because they are terminal audit evidence and cannot
			// grant ownership. They cannot carry a new observed-identity claim
			// without a complete transaction.
			if r.ObservedIdentity != "" {
				return s, original, fmt.Errorf("invalid retired transaction %d", i)
			}
			continue
		}
		if r.ObservedIdentity == "" {
			// Older schema-two records did not retain the observed identity.
			// The transaction identity is the only known identity in that form,
			// so fill it in memory without claiming a same-name replacement was
			// observed when it was not recorded.
			r.ObservedIdentity = r.Identity
			s.Retired[i] = r
		}
		if !validTransaction(r.Account, r.Transaction) || r.Transaction.ID != r.ID || r.Transaction.Account != r.Account || r.Transaction.Identity != r.Identity || r.Transaction.Kind != r.Kind || r.Transaction.Phase != "conflict" {
			return s, original, fmt.Errorf("invalid retired transaction %d", i)
		}
	}
	for i, t := range s.Journal {
		if t.ID == "" || t.Account == "" || t.Kind == "" || t.OldHash == "" || t.TargetHash == "" || t.CreatedMS <= 0 || t.CompletedMS < t.CreatedMS {
			return s, original, fmt.Errorf("invalid transition journal entry %d", i)
		}
	}
	s.Schema = Schema
	return s, original, nil
}
func validTransaction(name string, tx *Transaction) bool {
	if name == "" || tx == nil || tx.Account != name || tx.ID == "" || tx.CreatedMS <= 0 || tx.Identity == "" || tx.Old.Identity != tx.Identity || tx.Old.ProtectedHash != Digest(tx.Old.Protected) || tx.Target.MutationID == nil || *tx.Target.MutationID != tx.ID {
		return false
	}
	switch tx.Kind {
	case "disable", "restore", "family":
	default:
		return false
	}
	switch tx.Phase {
	case "prepared", "patched", "unknown", "conflict":
	default:
		return false
	}
	for _, p := range []*Snapshot{tx.Prior.Disable, tx.Prior.Family} {
		if p != nil && (p.Identity == "" || p.MtimeNS <= 0 || p.ProtectedHash != Digest(p.Protected)) {
			return false
		}
	}
	return true
}

func validRawObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var obj map[string]json.RawMessage
	return json.Unmarshal(raw, &obj) == nil && obj != nil
}

func PruneJournal(s *State) {
	if len(s.Journal) <= MaxJournal {
		return
	}
	// Pending transactions are separate from this bounded history and must never
	// be evicted. Oldest completed transitions are removed first.
	keep := append([]Transition{}, s.Journal[len(s.Journal)-MaxJournal:]...)
	s.Journal = keep
}

// StateDigest binds an offline decision to the complete normalized state,
// including legacy source evidence, migration provenance and observations.
// The JSON normalization removes insignificant whitespace while preserving
// numeric lexemes, so the same state gets the same digest across helpers.
func StateDigest(s State) string {
	b, err := json.Marshal(s)
	if err != nil {
		return Digest(map[string]string{"invalid": "state"})
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var normalized any
	if err := dec.Decode(&normalized); err != nil {
		return Digest(map[string]string{"invalid": "state"})
	}
	return Digest(normalized)
}

// JournalDigest is retained for callers that only need the transaction view.
// Offline repair and conflict decisions bind to StateDigest so no source or
// migration evidence can change outside the reviewed digest.
func JournalDigest(s State) string {
	return Digest(struct {
		Ownership    map[string]*Ownership   `json:"ownership"`
		Transactions map[string]*Transaction `json:"transactions"`
		Conflicts    map[string]*Conflict    `json:"conflicts"`
		Retired      []RetiredTransaction    `json:"retired"`
		Journal      []Transition            `json:"journal"`
	}{s.Ownership, s.Transactions, s.Conflicts, s.Retired, s.Journal})
}

func SortedObservationNames(m map[string]Observation) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
