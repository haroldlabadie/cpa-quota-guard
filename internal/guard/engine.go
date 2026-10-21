package guard

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

type Engine struct {
	mu         sync.RWMutex
	mutationMu sync.RWMutex
	backend    Backend
	state      State
	settings   Settings
	statePath  string
	apply      bool
	LastTick   int64
	LastError  string
	Running    bool
}

func NewEngine(b Backend, state State, settings Settings, path string, apply bool) *Engine {
	return &Engine{backend: b, state: state, settings: settings, statePath: path, apply: apply}
}
func (e *Engine) View() (State, int64, string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return cloneState(e.state), e.LastTick, e.LastError, e.Running
}
func cloneState(s State) State {
	b, _ := json.Marshal(s)
	var out State
	_ = json.Unmarshal(b, &out)
	return out
}
func (e *Engine) SetSettings(s Settings) {
	e.mutationMu.Lock()
	defer e.mutationMu.Unlock()
	e.mu.Lock()
	e.settings = s
	e.mu.Unlock()
}
func (e *Engine) Run(ctx context.Context, check, probe time.Duration) {
	e.mu.Lock()
	e.Running = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.Running = false
		if recover() != nil {
			e.LastError = "worker panic; automation stopped"
		}
		e.mu.Unlock()
	}()
	ticker := time.NewTicker(check)
	defer ticker.Stop()
	lastProbe := int64(0)
	for {
		if ctx.Err() != nil {
			return
		}
		now := time.Now().UnixMilli()
		fresh := now-lastProbe >= probe.Milliseconds()
		if fresh {
			lastProbe = now
		}
		err := e.Tick(ctx, now, fresh)
		e.mu.Lock()
		e.LastTick = now
		if err != nil {
			e.LastError = err.Error()
		} else {
			e.LastError = ""
		}
		e.mu.Unlock()
		if err != nil && strings.Contains(err.Error(), "state persistence") {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick uses one working copy. Reconcile and rule mutations update that copy;
// only a successful final persistence publishes it to the engine. Observe mode
// runs the same pure planning decisions but never persists or publishes state.
func (e *Engine) Tick(ctx context.Context, now int64, fresh bool) error {
	tickStarted := time.Now()
	e.mu.RLock()
	settings := e.settings
	working := cloneState(e.state)
	e.mu.RUnlock()
	e.mutationMu.RLock()
	defer e.mutationMu.RUnlock()
	ctx = context.WithValue(ctx, settingsContextKey{}, Digest(settings))
	entries, err := e.backend.List(ctx)
	if err != nil {
		return errors.New("credential inventory unavailable")
	}
	var tickErr error
	seen := map[string]bool{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var rule *Rule
		for i := range settings.Rules {
			r := &settings.Rules[i]
			if r.Enabled && strings.EqualFold(entry.Email, r.Email) && entry.Provider == r.Provider {
				rule = r
				break
			}
		}
		if rule == nil {
			continue
		}
		seen[entry.Name] = true
		a, err := e.backend.Read(ctx, entry)
		if err != nil {
			obs := Observation{Name: entry.Name, Provider: entry.Provider, AuthIndex: entry.AuthIndex, Email: entry.Email, Reasons: []string{"credential metadata unavailable"}, Blocked: true, Action: "read-degraded; no credential change", UpdatedMS: now}
			if old, ok := previousObservations(e)[entry.Name]; ok && old.Identity != "" {
				obs.Identity = old.Identity
				obs.Sample = old.Sample
			}
			working.Observations[entry.Name] = obs
			tickErr = errors.New("credential metadata unavailable")
			continue
		}
		if !strings.EqualFold(a.Entry.Email, rule.Email) || a.Entry.Provider != rule.Provider {
			continue
		}
		obs := Observation{Name: entry.Name, Provider: entry.Provider, AuthIndex: entry.AuthIndex, Email: a.Entry.Email, Identity: a.Snapshot.Identity, Reasons: []string{}, UpdatedMS: now}
		if conflict := working.Conflicts[entry.Name]; conflict != nil {
			obs.Manual = true
			obs.Action = "conflict; explicit offline resolution required"
			obs.OrphanReason = conflict.Reason
			if conflict.Identity == a.Snapshot.Identity && conflict.ObservedHash == a.Snapshot.ProtectedHash {
				obs.TransactionID = conflict.ID
			}
			obs.Blocked = true
			working.Observations[entry.Name] = obs
			continue
		}
		if tx := working.Transactions[entry.Name]; tx != nil && tx.Phase == "conflict" {
			obs.Manual = true
			obs.Action = "conflict; explicit offline resolution required"
			obs.TransactionID, obs.TransactionPhase = tx.ID, tx.Phase
			obs.OrphanReason = tx.Reason
			obs.Blocked = true
			working.Observations[entry.Name] = obs
			continue
		}
		if tx := working.Transactions[entry.Name]; tx != nil {
			own := working.Ownership[entry.Name]
			if own == nil {
				own = &Ownership{}
			}
			resolved, recErr := e.reconcile(ctx, tx, own, a, &working, now)
			if recErr != nil {
				obs.Error = recErr.Error()
				obs.TransactionID = tx.ID
				obs.TransactionPhase = tx.Phase
				obs.Action = "transaction-pending"
				obs.Manual = tx.Phase == "conflict" || tx.Phase == "manual"
				tickErr = recErr
				working.Observations[entry.Name] = obs
				// A persistence failure inside reconcile is fatal. Reconcile
				// already persisted the new phase on the working state; do not
				// swallow it and continue with later accounts.
				if strings.Contains(recErr.Error(), "state persistence") {
					return recErr
				}
				continue
			}
			if resolved != nil {
				a = *resolved
				obs.Identity = a.Snapshot.Identity
			}
		}
		oldObs := working.Observations[entry.Name]
		recent := oldObs.Identity == a.Snapshot.Identity && oldObs.Sample != nil && now-oldObs.Sample.ObservedMS <= 90000 && oldObs.Sample.ObservedMS <= now+1000
		retained := false
		if !fresh && recent {
			retained = true
			obs.Sample = oldObs.Sample
		} else {
			s, probeErr := e.backend.Probe(ctx, a)
			if probeErr != nil {
				if recent {
					obs.Sample = oldObs.Sample
					obs.Error = "quota probe unavailable; retained recent sample"
					retained = true
				} else {
					obs.Error = "quota probe unavailable"
				}
			} else {
				obs.Sample = &s
			}
		}
		if e.apply && fresh && obs.Sample != nil && !retained {
			if publisher, ok := e.backend.(interface {
				Publish(context.Context, Account, Sample) error
			}); ok {
				if err := publisher.Publish(ctx, a, *obs.Sample); err != nil {
					obs.SnapshotError = "panel snapshot publication unavailable"
				}
			}
		}
		own := working.Ownership[entry.Name]
		if own == nil {
			own = &Ownership{}
		}
		decisionNow := now + time.Since(tickStarted).Milliseconds()
		if IsWholeCredential(entry.Provider) {
			obs.Reasons = Reasons(obs.Sample, entry.Provider, "all", *rule, decisionNow)
			obs.Blocked = len(obs.Reasons) > 0
			err = e.applyCodex(ctx, a, own, &obs, &working, now)
		} else {
			obs.FamilyReasons = map[string][]string{}
			for _, scope := range EffectiveScopes(entry.Provider, rule.Scopes) {
				r := Reasons(obs.Sample, entry.Provider, scope, *rule, decisionNow)
				obs.FamilyReasons[scope] = r
				obs.Reasons = append(obs.Reasons, r...)
			}
			obs.Blocked = len(obs.Reasons) > 0
			err = e.applyFamily(ctx, a, own, &obs, &working, now)
		}
		if err != nil {
			obs.Error = err.Error()
			tickErr = err
			if strings.Contains(err.Error(), "state persistence") {
				return err
			}
		}
		working.Ownership[entry.Name] = own
		working.Observations[entry.Name] = obs
	}
	// Keep a visible degraded state for whitelisted entries that disappeared or
	// became unreadable during this tick; a successful save must not erase it.
	// Reconcile previously observed whitelist entries that the inventory did not
	// return. Do not invent a credential name from an email; retain the last known
	// name and mark the evidence degraded.
	for name, obs := range previousObservations(e) {
		if seen[name] {
			continue
		}
		for _, r := range settings.Rules {
			if r.Enabled && strings.EqualFold(obs.Provider, r.Provider) && strings.EqualFold(obs.Email, r.Email) {
				obs.Reasons = []string{"credential unavailable or no longer listed"}
				obs.Blocked = true
				obs.Action = "inventory-degraded; no credential change"
				obs.UpdatedMS = now
				working.Observations[name] = obs
				break
			}
		}
	}
	if e.apply {
		if err := e.persist(working); err != nil {
			return err
		}
	} else {
		e.mu.Lock()
		e.state = cloneState(working)
		e.mu.Unlock()
	}
	return tickErr
}

func previousObservations(e *Engine) map[string]Observation {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]Observation, len(e.state.Observations))
	for k, v := range e.state.Observations {
		out[k] = v
	}
	return out
}

func (e *Engine) applyCodex(ctx context.Context, a Account, own *Ownership, obs *Observation, s *State, now int64) error {
	p := a.Snapshot.Protected
	owned := p.Disabled && HasOwner(p.Owner, Owner) && Proven(own.Disable, a.Snapshot)
	if p.Disabled && !owned {
		obs.Manual = true
		if HasOwner(p.Owner, Owner) {
			obs.OrphanReason = "owner marker without matching prior proof"
		}
		obs.Action = "manual-disabled; retained"
		own.Disable = nil
		return nil
	}
	if owned {
		copy := a.Snapshot
		own.Disable = &copy
	}
	if obs.Blocked && !p.Disabled {
		obs.Action = "would-disable"
		if !e.apply {
			return nil
		}
		target := p
		target.Disabled = true
		target.Owner = strPtr(Owner)
		target.Reason = strPtr(strings.Join(obs.Reasons, "; "))
		tx, err := e.startTransaction(a.Entry.Name, "disable", a.Snapshot, *own, target, s, now)
		if err != nil {
			return err
		}
		after, err := e.mutate(ctx, a, mutationFields(map[string]any{"disabled": true, "quota_guard_owner": Owner, "quota_guard_reason": *target.Reason}, tx.ID), nil, s, tx, now)
		if err != nil {
			obs.TransactionID, obs.TransactionPhase = tx.ID, tx.Phase
			return err
		}
		own.Disable = &after.Snapshot
		obs.Action = "disabled"
	} else if !obs.Blocked && p.Disabled {
		obs.Action = "would-restore"
		if !e.apply {
			return nil
		}
		target := p
		target.Disabled = false
		target.Owner = nil
		target.Reason = nil
		tx, err := e.startTransaction(a.Entry.Name, "restore", a.Snapshot, *own, target, s, now)
		if err != nil {
			return err
		}
		_, err = e.mutate(ctx, a, mutationFields(map[string]any{"disabled": false, "quota_guard_owner": nil, "quota_guard_reason": nil}, tx.ID), own.Disable, s, tx, now)
		if err != nil {
			obs.TransactionID, obs.TransactionPhase = tx.ID, tx.Phase
			return err
		}
		own.Disable = nil
		obs.Action = "restored"
	}
	return nil
}

func (e *Engine) applyFamily(ctx context.Context, a Account, own *Ownership, obs *Observation, s *State, now int64) error {
	p := a.Snapshot.Protected
	wholeOwned := p.Disabled && HasOwner(p.Owner, Owner) && Proven(own.Disable, a.Snapshot)
	if p.Disabled && !wholeOwned {
		obs.Manual = true
		if HasOwner(p.Owner, Owner) {
			obs.OrphanReason = "whole disable owner without matching prior proof"
		}
		obs.Action = "manual-disabled; retained"
		own.Disable = nil
		return nil
	}
	base := append([]string{}, p.Excluded...)
	familyProven := p.Family != nil && p.Family.Owner == FamilyOwner && Proven(own.Family, a.Snapshot)
	if familyProven {
		base = append([]string{}, p.Family.Base...)
		copy := a.Snapshot
		own.Family = &copy
	} else if p.Family != nil {
		obs.Manual = true
		obs.OrphanReason = "family control without matching prior proof"
		own.Family = nil
	}
	blocked := []string{}
	target := append([]string{}, base...)
	for _, scope := range sortedKeys(obs.FamilyReasons) {
		if len(obs.FamilyReasons[scope]) > 0 {
			blocked = append(blocked, scope)
			target = append(target, ScopePatterns(a.Entry.Provider, scope)...)
		}
	}
	target = Normalize(target)
	sort.Strings(blocked)
	var control *FamilyControl
	if len(blocked) > 0 {
		control = &FamilyControl{Owner: FamilyOwner, Base: Normalize(base), Blocked: blocked}
	}
	if !familyProven && p.Family != nil && len(blocked) == 0 && !wholeOwned {
		obs.Action = "manual-exclusions; retained"
		return nil
	}
	targetP := p
	targetP.Excluded = target
	targetP.Family = control
	if wholeOwned {
		targetP.Disabled = false
		targetP.Owner = nil
		targetP.Reason = nil
	}
	if reflect.DeepEqual(p.Excluded, targetP.Excluded) && reflect.DeepEqual(p.Family, targetP.Family) && !wholeOwned {
		return nil
	}
	obs.Action = "would-update-families"
	if !e.apply {
		return nil
	}
	var proof *Snapshot
	if familyProven {
		proof = own.Family
	}
	if wholeOwned {
		proof = own.Disable
	}
	tx, err := e.startTransaction(a.Entry.Name, "family", a.Snapshot, *own, targetP, s, now)
	if err != nil {
		return err
	}
	fields := map[string]any{"excluded_models": target, "quota_guard_family_control": control}
	if wholeOwned {
		fields["disabled"] = false
		fields["quota_guard_owner"] = nil
		fields["quota_guard_reason"] = nil
	}
	after, err := e.mutate(ctx, a, mutationFields(fields, tx.ID), proof, s, tx, now)
	if err != nil {
		obs.TransactionID, obs.TransactionPhase = tx.ID, tx.Phase
		return err
	}
	if wholeOwned {
		own.Disable = nil
	}
	if control != nil {
		own.Family = &after.Snapshot
	} else {
		own.Family = nil
	}
	obs.Action = "families-updated"
	return nil
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
