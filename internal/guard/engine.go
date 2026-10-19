package guard

import (
	"context"
	"errors"
	"fmt"
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
func cloneState(s State) State { // JSON roundtrip deliberately bounds snapshot ownership and avoids shared maps.
	b, _ := jsonMarshal(s)
	var out State
	_ = jsonUnmarshal(b, &out)
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
func (e *Engine) Tick(ctx context.Context, now int64, fresh bool) error {
	tickStarted := time.Now()
	e.mu.RLock()
	settings := e.settings
	previous := cloneState(e.state)
	e.mu.RUnlock()
	ctx = context.WithValue(ctx, settingsContextKey{}, Digest(settings))
	entries, err := e.backend.List(ctx)
	if err != nil {
		return errors.New("credential inventory unavailable")
	}
	var tickErr error
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
		a, err := e.backend.Read(ctx, entry)
		if err != nil {
			tickErr = errors.New("credential metadata unavailable")
			continue
		}
		// Bind email and runtime state on every read, not only the inventory snapshot.
		if !strings.EqualFold(a.Entry.Email, rule.Email) || a.Entry.Provider != rule.Provider {
			continue
		}
		obs := Observation{Name: entry.Name, Provider: entry.Provider, AuthIndex: entry.AuthIndex, Identity: a.Snapshot.Identity, Reasons: []string{}, UpdatedMS: now}
		oldObs := previous.Observations[entry.Name]
		recent := oldObs.Identity == a.Snapshot.Identity && oldObs.Sample != nil && now-oldObs.Sample.ObservedMS <= 90000 && oldObs.Sample.ObservedMS <= now+1000
		retained := false
		if !fresh && recent {
			retained = true
			obs.Sample = oldObs.Sample
		} else {
			s, err := e.backend.Probe(ctx, a)
			if err != nil {
				if recent {
					// A transient management API failure must not discard an identity-bound
					// sample that is still within the same freshness bound used for decisions.
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
		// CPAMP panel snapshots are a best-effort display adapter. They never supply
		// the evidence used by protection and are disabled in observe mode.
		if e.apply && fresh && obs.Sample != nil && !retained {
			if publisher, ok := e.backend.(interface {
				Publish(context.Context, Account, Sample) error
			}); ok {
				if err := publisher.Publish(ctx, a, *obs.Sample); err != nil {
					obs.SnapshotError = "panel snapshot publication unavailable"
				}
			}
		}
		own := previous.Ownership[entry.Name]
		if own == nil {
			own = &Ownership{}
		}
		decisionNow := now + time.Since(tickStarted).Milliseconds()
		if entry.Provider == "codex" {
			obs.Reasons = Reasons(obs.Sample, entry.Provider, "all", *rule, decisionNow)
			obs.Blocked = len(obs.Reasons) > 0
			err = e.applyCodex(ctx, a, own, &obs)
		} else {
			obs.FamilyReasons = map[string][]string{}
			for _, scope := range []string{"gemini", "third_party"} {
				r := Reasons(obs.Sample, entry.Provider, scope, *rule, decisionNow)
				obs.FamilyReasons[scope] = r
				obs.Reasons = append(obs.Reasons, r...)
			}
			obs.Blocked = len(obs.Reasons) > 0
			err = e.applyFamily(ctx, a, own, &obs)
		}
		if err != nil {
			obs.Error = err.Error()
			tickErr = err
		}
		previous.Ownership[entry.Name] = own
		previous.Observations[entry.Name] = obs
		// A failed fsync after a credential write stops the worker: losing ownership
		// must not permit further changes in this process.
		if e.apply {
			if err = AtomicJSON(e.statePath, previous); err != nil {
				return errors.New("state persistence failed; automation stopped")
			}
		}
		e.mu.Lock()
		e.state = cloneState(previous)
		e.mu.Unlock()
	}
	return tickErr
}
func (e *Engine) mutate(ctx context.Context, a Account, fields map[string]any, own *Snapshot) (*Account, error) {
	e.mutationMu.RLock()
	defer e.mutationMu.RUnlock()
	e.mu.RLock()
	revision := Digest(e.settings)
	e.mu.RUnlock()
	if expected, _ := ctx.Value(settingsContextKey{}).(string); expected != "" && expected != revision {
		return nil, errors.New("settings changed before patch")
	}
	current, err := e.backend.Read(ctx, a.Entry)
	if err != nil {
		return nil, errors.New("prewrite read failed")
	}
	if !Proven(&a.Snapshot, current.Snapshot) {
		return nil, errors.New("credential changed before patch")
	}
	if own != nil && !Proven(own, current.Snapshot) {
		return nil, errors.New("ownership changed before patch")
	}
	if !e.apply {
		return &current, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err = e.backend.Patch(ctx, current, fields); err != nil {
		return nil, errors.New("credential patch failed")
	}
	after, err := e.backend.Read(ctx, current.Entry)
	if err != nil {
		return nil, errors.New("credential readback failed")
	}
	if after.Snapshot.Identity != a.Snapshot.Identity {
		return nil, errors.New("credential identity changed on readback")
	}
	p := after.Snapshot.Protected
	for k, v := range fields {
		match := false
		switch k {
		case "disabled":
			match = p.Disabled == v
		case "quota_guard_owner":
			match = (v == nil && p.Owner == nil) || (p.Owner != nil && *p.Owner == v)
		case "quota_guard_reason":
			match = (v == nil && p.Reason == nil) || (p.Reason != nil && *p.Reason == v)
		case "excluded_models":
			match = reflect.DeepEqual(p.Excluded, v)
		case "quota_guard_family_control":
			if v == nil {
				match = p.Family == nil
			} else {
				match = reflect.DeepEqual(p.Family, v)
			}
		}
		if !match {
			return nil, fmt.Errorf("credential readback mismatch: %s", k)
		}
	}
	if after.Entry.Disabled != after.Snapshot.Protected.Disabled {
		return nil, errors.New("credential runtime readback mismatch")
	}
	return &after, nil
}

type settingsContextKey struct{}

func (e *Engine) applyCodex(ctx context.Context, a Account, own *Ownership, obs *Observation) error {
	p := a.Snapshot.Protected
	if p.Disabled && (!HasOwner(p.Owner, Owner) || !Proven(own.Disable, a.Snapshot)) {
		obs.Manual = true
		obs.Action = "manual-disabled; retained"
		own.Disable = nil
		return nil
	}
	if p.Disabled {
		copy := a.Snapshot
		own.Disable = &copy
	}
	if obs.Blocked && !p.Disabled {
		obs.Action = "would-disable"
		if !e.apply {
			return nil
		}
		after, err := e.mutate(ctx, a, map[string]any{"disabled": true, "quota_guard_owner": Owner, "quota_guard_reason": strings.Join(obs.Reasons, "; ")}, nil)
		if err != nil {
			return err
		}
		own.Disable = &after.Snapshot
		obs.Action = "disabled"
	} else if !obs.Blocked && p.Disabled {
		obs.Action = "would-restore"
		if !e.apply {
			return nil
		}
		_, err := e.mutate(ctx, a, map[string]any{"disabled": false, "quota_guard_owner": nil, "quota_guard_reason": nil}, own.Disable)
		if err != nil {
			return err
		}
		own.Disable = nil
		obs.Action = "restored"
	}
	return nil
}
func (e *Engine) applyFamily(ctx context.Context, a Account, own *Ownership, obs *Observation) error {
	p := a.Snapshot.Protected
	wholeOwned := p.Disabled && HasOwner(p.Owner, Owner) && Proven(own.Disable, a.Snapshot)
	if p.Disabled && !wholeOwned {
		obs.Manual = true
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
		own.Family = nil
	}
	blocked := []string{}
	target := append([]string{}, base...)
	for _, scope := range []string{"gemini", "third_party"} {
		if len(obs.FamilyReasons[scope]) > 0 {
			blocked = append(blocked, scope)
			target = append(target, Patterns[scope]...)
		}
	}
	target = Normalize(target)
	sort.Strings(blocked)
	var control *FamilyControl
	if len(blocked) > 0 {
		control = &FamilyControl{Owner: FamilyOwner, Base: Normalize(base), Blocked: blocked}
	}
	// An unproven old control becomes manual baseline. No safe quota result may
	// remove those exclusions; newly unsafe families may only add exclusions.
	if !familyProven && p.Family != nil && len(blocked) == 0 && !wholeOwned {
		obs.Action = "manual-exclusions; retained"
		return nil
	}
	if reflect.DeepEqual(p.Excluded, target) && reflect.DeepEqual(p.Family, control) && !wholeOwned {
		return nil
	}
	obs.Action = "would-update-families"
	if !e.apply {
		return nil
	}
	fields := map[string]any{"excluded_models": target, "quota_guard_family_control": control}
	if wholeOwned {
		fields["disabled"] = false
		fields["quota_guard_owner"] = nil
		fields["quota_guard_reason"] = nil
	}
	var proof *Snapshot
	if familyProven {
		proof = own.Family
	}
	if wholeOwned {
		proof = own.Disable
	}
	after, err := e.mutate(ctx, a, fields, proof)
	if err != nil {
		return err
	}
	own.Disable = nil
	if control != nil {
		own.Family = &after.Snapshot
	} else {
		own.Family = nil
	}
	obs.Action = "families-updated"
	return nil
}
