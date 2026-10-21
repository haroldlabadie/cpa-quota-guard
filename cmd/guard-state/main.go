// guard-state exchanges only sanitized ownership proofs and state evidence. Raw
// OAuth data is read from stdin and is never echoed, logged or accepted as a
// command-line argument.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/haroldlabadie/cpa-quota-guard/internal/guard"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "guard-state: invalid input or proof")
		os.Exit(1)
	}
}
func usage() error {
	return errors.New("usage: guard-state fingerprint|verify|validate-state|initialize-state | guard-state fingerprint|verify|validate-state|state-digest | acknowledge-conflict|diagnose-orphans|verify-native-proof --data-dir DIR [--current FILE] [--apply]")
}
func run() error {
	if len(os.Args) < 2 {
		return usage()
	}
	switch os.Args[1] {
	case "initialize-state":
		s := guard.NewState()
		s.Initialization = &guard.Initialization{Kind: "fresh-install", CreatedMS: time.Now().UnixMilli()}
		return json.NewEncoder(os.Stdout).Encode(s)
	case "acknowledge-conflict":
		return acknowledge(os.Args[2:])
	case "diagnose-orphans":
		return diagnose(os.Args[2:])
	case "verify-native-proof":
		return verifyNative(os.Args[2:])
	case "fingerprint", "verify", "validate-state", "state-digest":
	default:
		return usage()
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, guard.MaxFile+1))
	if err != nil {
		return err
	}
	if os.Args[1] == "validate-state" {
		if _, err = guard.ParseState(b); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]bool{"valid": true})
	}
	if os.Args[1] == "state-digest" {
		s, err := guard.ParseState(b)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"digest": guard.StateDigest(s)})
	}
	var req struct {
		Auth      map[string]any  `json:"auth"`
		MtimeNS   int64           `json:"mtime_ns"`
		Name      string          `json:"name"`
		AuthIndex string          `json:"auth_index"`
		Provider  string          `json:"provider"`
		Email     string          `json:"email"`
		Proof     *guard.Snapshot `json:"proof,omitempty"`
	}
	if err = guard.StrictJSON(b, &req); err != nil {
		return err
	}
	if req.Auth == nil || req.Name == "" || req.AuthIndex == "" || req.MtimeNS <= 0 || !guard.SupportedProvider(req.Provider) {
		return errors.New("input")
	}
	a, err := guard.MakeAccount(guard.Entry{Name: req.Name, AuthIndex: req.AuthIndex, Provider: req.Provider, Email: req.Email}, req.Auth, req.MtimeNS)
	if err != nil {
		return err
	}
	var out any = a.Snapshot
	if os.Args[1] == "verify" {
		out = map[string]any{"valid": guard.Proven(req.Proof, a.Snapshot), "snapshot": a.Snapshot}
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

// acknowledge is an offline, explicit conflict acknowledgement. It never
// fabricates ownership and never removes a pending transaction.
func acknowledge(args []string) error {
	dataDir, apply := "", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--data-dir":
			i++
			if i >= len(args) {
				return usage()
			}
			dataDir = args[i]
		case "--apply":
			apply = true
		default:
			return usage()
		}
	}
	if !filepath.IsAbs(dataDir) {
		return errors.New("absolute data_dir required")
	}
	statePath := filepath.Join(dataDir, "state.json")
	b, err := os.ReadFile(statePath)
	if err != nil {
		return errors.New("state unavailable")
	}
	s, err := guard.ParseState(b)
	if err != nil {
		return err
	}
	var input struct {
		Account     string `json:"account"`
		Identity    string `json:"identity"`
		StateDigest string `json:"state_digest"`
		Reason      string `json:"reason"`
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, guard.MaxFile+1))
	if err != nil || guard.StrictJSON(raw, &input) != nil || input.Account == "" || input.Identity == "" || input.StateDigest == "" || input.Reason == "" {
		return errors.New("invalid conflict input")
	}
	out, conflict, err := matchConflict(s, input.Account, input.Identity, input.StateDigest)
	if err != nil {
		return err
	}
	if !apply {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	f, err := guard.LockPath(filepath.Join(dataDir, "worker.lock"))
	if err != nil {
		return errors.New("worker is running; quiesce first")
	}
	defer guard.UnlockPath(f)
	// Re-read after acquiring the lock and apply the exact same decision rule
	// as dry-run. Same-name replacement deliberately leaves tx.Identity at the
	// original identity while conflict.Identity records the current identity.
	b, err = os.ReadFile(statePath)
	if err != nil {
		return errors.New("state unavailable")
	}
	fresh, err := guard.ParseState(b)
	if err != nil {
		return errors.New("state changed since review; no action")
	}
	out, conflict, err = matchConflict(fresh, input.Account, input.Identity, input.StateDigest)
	if err != nil {
		return errors.New("state changed since review; no action")
	}
	tx := fresh.Transactions[input.Account]
	now := time.Now().UnixMilli()
	retiredTransaction := *tx
	fresh.Retired = append(fresh.Retired, guard.RetiredTransaction{
		ID: tx.ID, Account: input.Account, Identity: tx.Identity,
		ObservedIdentity: conflict.Identity, Kind: conflict.Kind,
		Reason: input.Reason, Observed: conflict.Observed,
		ObservedHash: conflict.ObservedHash, Transaction: &retiredTransaction,
		RetiredMS: now,
	})
	delete(fresh.Transactions, input.Account)
	delete(fresh.Conflicts, input.Account)
	if fresh.Ownership[input.Account] != nil {
		// Clear automatic ownership only after the matching transaction has been
		// confirmed retired; the operator has taken over.
		delete(fresh.Ownership, input.Account)
	}
	fresh.Journal = append(fresh.Journal, guard.Transition{ID: conflict.ID, Account: input.Account, Kind: "conflict", Phase: "manual-retired", Reason: input.Reason, CreatedMS: now, CompletedMS: now, OldHash: conflict.ObservedHash, TargetHash: conflict.ObservedHash})
	guard.PruneJournal(&fresh)
	if err = validateCandidate(fresh); err != nil {
		return errors.New("state validation failed; no action")
	}
	if err = guard.AtomicJSON(statePath, fresh); err != nil {
		return errors.New("state persistence failed")
	}
	out["applied"] = true
	return json.NewEncoder(os.Stdout).Encode(out)
}

// validateCandidate serializes exactly as AtomicJSON will and runs the plugin
// parser before publication. This prevents an offline command from writing a
// state that the next LoadState would reject.
func validateCandidate(s guard.State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(b) > guard.MaxFile {
		return errors.New("state exceeds size limit")
	}
	_, err = guard.ParseState(b)
	return err
}

// matchConflict is shared by dry-run and apply. The input identity must match
// the current conflict observation; it is intentionally not required to match
// the transaction's original identity because a same-name credential
// replacement is one of the conflicts an operator must be able to retire.
func matchConflict(s guard.State, account, identity, stateDigest string) (map[string]any, *guard.Conflict, error) {
	conflict := s.Conflicts[account]
	tx := s.Transactions[account]
	if conflict == nil || tx == nil || conflict.ID == "" || conflict.Account != account || tx.ID != conflict.ID || tx.Account != account || tx.ID == "" || tx.Kind != conflict.Kind || tx.Phase != "conflict" || conflict.Identity != identity || stateDigest != guard.StateDigest(s) {
		return nil, nil, errors.New("state digest, account, identity, or transaction changed; no action")
	}
	out := map[string]any{
		"account": account, "conflict_id": conflict.ID,
		"identity": conflict.Identity, "transaction_identity": tx.Identity,
		"applied": false,
	}
	return out, conflict, nil
}

func parseDirCurrent(args []string) (string, string, bool, error) {
	dataDir, currentFile, apply := "", "", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--data-dir":
			i++
			if i >= len(args) {
				return "", "", false, usage()
			}
			dataDir = args[i]
		case "--current":
			i++
			if i >= len(args) {
				return "", "", false, usage()
			}
			currentFile = args[i]
		case "--apply":
			apply = true
		default:
			return "", "", false, usage()
		}
	}
	if !filepath.IsAbs(dataDir) {
		return "", "", false, errors.New("absolute data_dir required")
	}
	return dataDir, currentFile, apply, nil
}

func loadDir(dataDir, currentFile string) (guard.State, string, map[string]guard.Account, error) {
	statePath := filepath.Join(dataDir, "state.json")
	b, err := os.ReadFile(statePath)
	if err != nil {
		return guard.State{}, statePath, nil, errors.New("state unavailable")
	}
	s, err := guard.ParseState(b)
	if err != nil {
		return s, statePath, nil, err
	}
	if currentFile == "" {
		return guard.State{}, statePath, nil, errors.New("--current required")
	}
	current, err := guard.ReadCurrentFile(currentFile)
	return s, statePath, current, err
}

func diagnose(args []string) error {
	dataDir, currentFile, _, err := parseDirCurrent(args)
	if err != nil {
		return err
	}
	s, _, current, err := loadDir(dataDir, currentFile)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"state_digest": guard.StateDigest(s), "orphans": guard.DiagnoseOrphans(s, current), "applied": false})
}

func verifyNative(args []string) error {
	dataDir, currentFile, apply, err := parseDirCurrent(args)
	if err != nil {
		return err
	}
	s, statePath, current, err := loadDir(dataDir, currentFile)
	if err != nil {
		return err
	}
	var in struct {
		Account string `json:"account"`
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, guard.MaxFile+1))
	if err != nil || guard.StrictJSON(raw, &in) != nil || in.Account == "" {
		return errors.New("invalid proof input")
	}
	a, ok := current[in.Account]
	if !ok {
		return errors.New("account not in current evidence")
	}
	if err = guard.VerifyNativeProof(s, in.Account, a); err != nil {
		return err
	}
	report := map[string]any{"account": in.Account, "verified": true, "applied": false}
	if !apply {
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	f, err := guard.LockPath(filepath.Join(dataDir, "worker.lock"))
	if err != nil {
		return errors.New("worker is running; quiesce first")
	}
	defer guard.UnlockPath(f)
	fresh, err := guard.LoadState(statePath)
	if err != nil {
		return errors.New("state changed; no action")
	}
	if err = guard.VerifyNativeProof(fresh, in.Account, a); err != nil {
		return err
	}
	report["applied"] = true
	return json.NewEncoder(os.Stdout).Encode(report)
}
