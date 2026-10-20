package guard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"reflect"
)

// Durable transaction lifecycle. The engine persists a prepared intent before
// touching credentials, then reconciles an uncertain outcome from exact
// evidence. Credential files have no conditional write, so a simultaneous
// manual edit remains a host limitation and is surfaced as a sticky conflict.
func (e *Engine) persist(s State) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	PruneJournal(&s)
	if err := AtomicJSON(e.statePath, s); err != nil {
		return errors.New("state persistence failed; automation stopped")
	}
	e.state = cloneState(s)
	return nil
}

func (e *Engine) mutate(ctx context.Context, a Account, fields map[string]any, own *Snapshot, s *State, tx *Transaction, now int64) (*Account, error) {
	e.mu.RLock()
	revision := Digest(e.settings)
	e.mu.RUnlock()
	if expected, _ := ctx.Value(settingsContextKey{}).(string); expected != "" && expected != revision {
		return nil, errors.New("settings changed before patch")
	}
	if !e.apply {
		return &a, nil
	}
	current, err := e.backend.Read(ctx, a.Entry)
	if err != nil {
		return e.failTransaction(a.Entry.Name, tx, s, "unknown", "prewrite read failed; transaction retained")
	}
	if current.Snapshot.Identity != tx.Old.Identity || !reflect.DeepEqual(current.Snapshot.Protected, tx.Old.Protected) || !Proven(&tx.Old, current.Snapshot) {
		return nil, e.recordConflict(a.Entry.Name, tx, current, s, "credential changed before patch; manual control retained", now)
	}
	if own != nil && !Proven(own, current.Snapshot) {
		return nil, e.recordConflict(a.Entry.Name, tx, current, s, "prior ownership changed before patch", now)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// The transaction was persisted by startTransaction before this call. Do
	// not write e.state between intent persistence and PATCH: a local publish
	// could make a failed PATCH look durable while the caller still holds its
	// working copy.
	if err := e.backend.Patch(ctx, current, fields); err != nil {
		// A transport error may follow a successful server-side write. Keep
		// intent and classify on reconcile; never resend in this tick.
		return e.failTransaction(a.Entry.Name, tx, s, "unknown", "credential patch result unknown; transaction retained")
	}
	after, err := e.backend.Read(ctx, current.Entry)
	if err != nil {
		return e.failTransaction(a.Entry.Name, tx, s, "unknown", "credential readback unavailable; transaction retained")
	}
	return e.acceptWrite(a.Entry.Name, tx, after, s, now)
}

// failTransaction persists only the transaction phase on the caller's working
// state and returns a failure that halts the worker. It never swallows a state
// persistence error.
func (e *Engine) failTransaction(account string, tx *Transaction, s *State, phase, reason string) (*Account, error) {
	tx.Phase, tx.Reason = phase, reason
	if s.Transactions == nil {
		s.Transactions = map[string]*Transaction{}
	}
	s.Transactions[account] = tx
	if e.apply {
		if err := e.persist(*s); err != nil {
			return nil, err
		}
	}
	return nil, errors.New(reason)
}

func (e *Engine) recordConflict(account string, tx *Transaction, current Account, s *State, reason string, now int64) error {
	tx.Phase, tx.Reason = "conflict", reason
	s.Transactions[account] = tx
	s.Conflicts[account] = &Conflict{ID: tx.ID, Account: account, Identity: current.Snapshot.Identity, Kind: tx.Kind, Reason: reason, Observed: current.Snapshot.Protected, ObservedHash: current.Snapshot.ProtectedHash, CreatedMS: now}
	s.Journal = append(s.Journal, Transition{ID: tx.ID, Account: account, Kind: tx.Kind, Phase: "conflict", Reason: reason, CreatedMS: tx.CreatedMS, CompletedMS: now, OldHash: Digest(tx.Old.Protected), TargetHash: Digest(tx.Target)})
	if e.apply {
		if err := e.persist(*s); err != nil {
			return err
		}
	}
	return errors.New("transaction conflict; explicit offline resolution required")
}

func (e *Engine) acceptWrite(account string, tx *Transaction, after Account, s *State, now int64) (*Account, error) {
	if after.Snapshot.Identity != tx.Identity {
		return nil, e.recordConflict(account, tx, after, s, "identity changed on readback", now)
	}
	if !ProtectedEqual(after.Snapshot.Protected, tx.Target) {
		return nil, e.recordConflict(account, tx, after, s, "full protected readback mismatch", now)
	}
	if after.Entry.Disabled != after.Snapshot.Protected.Disabled {
		return nil, e.recordConflict(account, tx, after, s, "runtime disabled mismatch", now)
	}
	proof := after.Snapshot
	own := s.Ownership[account]
	if own == nil {
		own = &Ownership{}
	}
	switch tx.Kind {
	case "disable":
		own.Disable = &proof
	case "restore":
		own.Disable = nil
		if tx.Target.Family == nil {
			own.Family = nil
		}
	case "family":
		if tx.Target.Family != nil {
			own.Family = &proof
		} else {
			own.Family = nil
		}
		if tx.Prior.Disable != nil && !tx.Target.Disabled {
			own.Disable = nil
		}
	}
	s.Ownership[account] = own
	delete(s.Transactions, account)
	s.Journal = append(s.Journal, Transition{ID: tx.ID, Account: account, Kind: tx.Kind, Phase: "applied", CreatedMS: tx.CreatedMS, CompletedMS: now, OldHash: Digest(tx.Old.Protected), TargetHash: Digest(tx.Target)})
	if e.apply {
		if err := e.persist(*s); err != nil {
			return nil, err
		}
	}
	return &after, nil
}

// reconcile checks durable intent evidence before any ordinary rule decision.
// It never create or updates credential restrictions; a conflict is sticky.
func (e *Engine) reconcile(ctx context.Context, tx *Transaction, own *Ownership, current Account, s *State, now int64) (*Account, error) {
	// A conflict is terminal until an explicit offline acknowledgement. It must
	// never be re-adopted merely because fields later happen to match the target.
	if tx.Phase == "conflict" || s.Conflicts[tx.Account] != nil {
		// Conflicts are terminal until an explicit acknowledgement. Do not
		// rewrite state on every tick merely to display the same condition.
		return nil, errors.New("transaction conflict; explicit offline resolution required")
	}
	if current.Snapshot.Identity != tx.Identity {
		s.Conflicts[tx.Account] = &Conflict{ID: tx.ID, Account: tx.Account, Identity: current.Snapshot.Identity, Kind: tx.Kind, Reason: "identity or same-name replacement changed", Observed: current.Snapshot.Protected, ObservedHash: current.Snapshot.ProtectedHash, CreatedMS: now}
		tx.Phase, tx.Reason = "conflict", "identity or same-name replacement changed"
		s.Transactions[tx.Account] = tx
		s.Journal = append(s.Journal, Transition{ID: tx.ID, Account: tx.Account, Kind: tx.Kind, Phase: "conflict", Reason: tx.Reason, CreatedMS: tx.CreatedMS, CompletedMS: now, OldHash: Digest(tx.Old.Protected), TargetHash: Digest(tx.Target)})
		if e.apply {
			if err := e.persist(*s); err != nil {
				return nil, err
			}
		}
		return nil, errors.New(tx.Reason)
	}
	if reflect.DeepEqual(current.Snapshot.Protected, tx.Old.Protected) && Proven(&tx.Old, current.Snapshot) {
		delete(s.Transactions, tx.Account)
		s.Journal = append(s.Journal, Transition{ID: tx.ID, Account: tx.Account, Kind: tx.Kind, Phase: "not-applied", Reason: "old snapshot unchanged", CreatedMS: tx.CreatedMS, CompletedMS: now, OldHash: Digest(tx.Old.Protected), TargetHash: Digest(tx.Target)})
		if e.apply {
			if err := e.persist(*s); err != nil {
				return nil, err
			}
		}
		return &current, nil
	}
	if !reflect.DeepEqual(current.Snapshot.Protected, tx.Target) {
		return nil, e.recordConflict(tx.Account, tx, current, s, "protected fields differ from both intent snapshots", now)
	}
	if tx.Target.MutationID == nil || current.Snapshot.Protected.MutationID == nil || *current.Snapshot.Protected.MutationID != *tx.Target.MutationID {
		return nil, e.recordConflict(tx.Account, tx, current, s, "mutation evidence missing", now)
	}
	if tx.Prior.Disable != nil && (tx.Prior.Disable.Identity != tx.Old.Identity || !Proven(tx.Prior.Disable, tx.Old)) {
		return nil, e.recordConflict(tx.Account, tx, current, s, "prior disable ownership inconsistent", now)
	}
	if tx.Prior.Family != nil && (tx.Prior.Family.Identity != tx.Old.Identity || !Proven(tx.Prior.Family, tx.Old)) {
		return nil, e.recordConflict(tx.Account, tx, current, s, "prior family ownership inconsistent", now)
	}
	tx.Phase = "patched"
	return e.acceptWrite(tx.Account, tx, current, s, now)
}

func newMutationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

type settingsContextKey struct{}

func (e *Engine) startTransaction(account string, kind string, old Snapshot, prior Ownership, target Protected, s *State, now int64) (*Transaction, error) {
	id, err := newMutationID()
	if err != nil {
		return nil, err
	}
	target.MutationID = &id
	tx := &Transaction{ID: id, CreatedMS: now, Kind: kind, Account: account, Identity: old.Identity, Old: old, Prior: cloneOwnership(prior), Target: target, Phase: "prepared"}
	if s.Transactions == nil {
		s.Transactions = map[string]*Transaction{}
	}
	s.Transactions[account] = tx
	if e.apply {
		if err := e.persist(*s); err != nil {
			delete(s.Transactions, account)
			return nil, err
		}
	}
	return tx, nil
}
func cloneOwnership(o Ownership) Ownership {
	out := Ownership{}
	if o.Disable != nil {
		c := *o.Disable
		out.Disable = &c
	}
	if o.Family != nil {
		c := *o.Family
		out.Family = &c
	}
	return out
}
func strPtr(s string) *string { return &s }
func mutationFields(fields map[string]any, id string) map[string]any {
	out := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		out[k] = v
	}
	out["quota_guard_mutation_id"] = id
	return out
}
