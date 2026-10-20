package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/haroldlabadie/cpa-quota-guard/internal/guard"
)

// sameNameConflictState models the critical recovery case: the transaction
// belongs to the old identity, while the conflict observed a replacement
// credential under the same account name.
func sameNameConflictState(t *testing.T, dir string) (guard.State, string) {
	t.Helper()
	old := guard.Snapshot{Identity: "identity-old", Protected: guard.Protected{Disabled: true, Excluded: []string{}}, MtimeNS: 10}
	old.ProtectedHash = guard.Digest(old.Protected)
	newer := guard.Snapshot{Identity: "identity-new", Protected: guard.Protected{Disabled: true, Excluded: []string{}}, MtimeNS: 20}
	newer.ProtectedHash = guard.Digest(newer.Protected)
	tx := &guard.Transaction{
		ID: "tx-a", CreatedMS: 1000, Kind: "restore", Account: "a.json",
		Identity: old.Identity, Old: old, Target: old.Protected, Phase: "conflict",
		Reason: "identity or same-name replacement changed",
	}
	tx.Target.MutationID = &tx.ID
	s := guard.NewState()
	s.Transactions["a.json"] = tx
	s.Conflicts["a.json"] = &guard.Conflict{
		ID: tx.ID, Account: "a.json", Identity: newer.Identity,
		Kind: tx.Kind, Reason: tx.Reason, Observed: newer.Protected,
		ObservedHash: newer.ProtectedHash, CreatedMS: 2000,
	}
	path := filepath.Join(dir, "state.json")
	if err := guard.AtomicJSON(path, s); err != nil {
		t.Fatal(err)
	}
	return s, path
}

func runCLI(t *testing.T, dir string, input []byte, apply bool) ([]byte, error) {
	t.Helper()
	oldArgs, oldStdin := os.Args, os.Stdin
	f, err := os.CreateTemp(t.TempDir(), "conflict-input")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(input); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	os.Stdin = f
	os.Args = []string{"guard-state", "acknowledge-conflict", "--data-dir", dir}
	if apply {
		os.Args = append(os.Args, "--apply")
	}
	defer func() {
		os.Args, os.Stdin = oldArgs, oldStdin
		f.Close()
	}()
	return capture(run)
}

func TestAcknowledgeSameNameReplacementThroughCLIDryRunApplyLoadTick(t *testing.T) {
	dir := t.TempDir()
	s, path := sameNameConflictState(t, dir)
	input, _ := json.Marshal(map[string]string{
		"account": "a.json", "identity": "identity-new",
		"state_digest": guard.StateDigest(s), "reason": "reviewed replacement",
	})
	out, err := runCLI(t, dir, input, false)
	if err != nil || !bytes.Contains(out, []byte(`"applied":false`)) ||
		!bytes.Contains(out, []byte(`"identity":"identity-new"`)) ||
		!bytes.Contains(out, []byte(`"transaction_identity":"identity-old"`)) {
		t.Fatalf("dry run failed: %v %s", err, out)
	}
	dry, err := guard.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Conflicts["a.json"] == nil || dry.Transactions["a.json"] == nil || len(dry.Retired) != 0 {
		t.Fatalf("dry run mutated state: %#v", dry)
	}
	out, err = runCLI(t, dir, input, true)
	if err != nil || !bytes.Contains(out, []byte(`"applied":true`)) {
		t.Fatalf("apply failed: %v %s", err, out)
	}
	disk, err := guard.LoadState(path)
	if err != nil {
		t.Fatalf("retired state cannot be loaded: %v", err)
	}
	if disk.Conflicts["a.json"] != nil || disk.Transactions["a.json"] != nil || len(disk.Retired) != 1 {
		t.Fatalf("acknowledgement did not retire conflict: %#v", disk)
	}
	retired := disk.Retired[0]
	if retired.ID != "tx-a" || retired.Identity != "identity-old" || retired.ObservedIdentity != "identity-new" || retired.Transaction == nil {
		t.Fatalf("retired identity evidence incomplete: %#v", retired)
	}
	if retired.Transaction.Old.Identity != "identity-old" || retired.Transaction.Target.MutationID == nil ||
		*retired.Transaction.Target.MutationID != retired.ID || retired.Transaction.Phase != "conflict" {
		t.Fatalf("retired transaction evidence incomplete: %#v", retired.Transaction)
	}
	// A fresh Engine must load the terminal evidence and continue without
	// adopting ownership or reusing the replacement identity.
	f := &fakeTickBackend{
		raw: map[string]any{"type": "codex", "email": "a@example.invalid", "account_id": "replacement", "project_id": "project", "access_token": "fake", "disabled": true, "quota_guard_owner": guard.Owner},
		sample: guard.Sample{ObservedMS: 1900000000000, Windows: []guard.Window{
			{ID: "5h", Kind: "five_hour", Scope: "all", Remaining: 1, EndMS: 1900005000000, DurationMS: guard.FiveHourMS},
			{ID: "week", Kind: "weekly", Scope: "all", Remaining: 50, EndMS: 1900600000000, DurationMS: guard.WeekMS},
		}},
	}
	loaded, err := guard.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	e := guard.NewEngine(f, loaded, guard.Settings{Schema: 1, Rules: []guard.Rule{{Email: "a@example.invalid", Provider: "codex", Enabled: true, ReserveFiveHour: 8, ReserveWeekly: 3}}}, path, true)
	if err = e.Tick(context.Background(), 1900000000000, true); err != nil {
		t.Fatal(err)
	}
	if f.patches != 0 {
		t.Fatal("retired replacement was re-adopted")
	}
	disk, err = guard.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.Journal) == 0 || disk.Journal[len(disk.Journal)-1].Phase != "manual-retired" {
		t.Fatal("audit journal missing")
	}
	// Replay of an already used digest cannot clear or re-adopt anything.
	if err = func() error { _, err := runCLI(t, dir, input, true); return err }(); err == nil {
		t.Fatal("replayed acknowledgement accepted")
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Contains(raw, []byte(`"retired"`)) {
		t.Fatal("state overwritten on replay")
	}
	// The foreign evidence in a retired record is part of its integrity
	// boundary; a mismatched embedded transaction must fail loading.
	disk.Retired[0].Transaction.ID = "different-tx"
	if err := guard.AtomicJSON(path, disk); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.LoadState(path); err == nil {
		t.Fatal("mismatched retired transaction accepted")
	}
	// A retired summary written before complete-transaction preservation stays
	// loadable, while a new observed identity without the transaction is
	// rejected instead of silently erasing evidence.
	legacySummary := disk
	legacySummary.Retired[0].Transaction = nil
	legacySummary.Retired[0].ObservedIdentity = ""
	if err := guard.AtomicJSON(path, legacySummary); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.LoadState(path); err != nil {
		t.Fatalf("legacy retired summary rejected: %v", err)
	}
	legacySummary.Retired[0].ObservedIdentity = "identity-new"
	if err := guard.AtomicJSON(path, legacySummary); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.LoadState(path); err == nil {
		t.Fatal("observed identity without complete transaction accepted")
	}
}

func TestAcknowledgeFullJournalPrunesAndRemainsLoadable(t *testing.T) {
	dir := t.TempDir()
	s, path := sameNameConflictState(t, dir)
	for i := 0; i < guard.MaxJournal; i++ {
		s.Journal = append(s.Journal, guard.Transition{
			ID: "audit-" + strconv.Itoa(i), Account: "a.json", Kind: "restore",
			Phase: "applied", CreatedMS: 1, CompletedMS: 1,
			OldHash: "old", TargetHash: "target",
		})
	}
	if len(s.Journal) != guard.MaxJournal {
		t.Fatalf("journal setup length = %d", len(s.Journal))
	}
	if err := guard.AtomicJSON(path, s); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{
		"account": "a.json", "identity": "identity-new",
		"state_digest": guard.StateDigest(s), "reason": "reviewed full journal",
	})
	if out, err := runCLI(t, dir, input, true); err != nil || !bytes.Contains(out, []byte(`"applied":true`)) {
		t.Fatalf("full-journal apply failed: %v %s", err, out)
	}
	disk, err := guard.LoadState(path)
	if err != nil {
		t.Fatalf("state written with full journal cannot be loaded: %v", err)
	}
	if len(disk.Journal) != guard.MaxJournal {
		t.Fatalf("journal length after prune = %d, want %d", len(disk.Journal), guard.MaxJournal)
	}
	if disk.Journal[len(disk.Journal)-1].Phase != "manual-retired" {
		t.Fatal("new manual-retired audit entry was not retained")
	}
	if disk.Conflicts["a.json"] != nil || disk.Transactions["a.json"] != nil || len(disk.Retired) != 1 {
		t.Fatalf("full-journal acknowledgement did not retire conflict: %#v", disk)
	}
}

func TestAtomicJSONRejectsOversizeWithoutChangingOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := guard.AtomicJSON(path, guard.NewState()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oversized := string(make([]byte, guard.MaxFile))
	state := guard.NewState()
	state.Legacy.Accounts["oversized"] = json.RawMessage(strconv.Quote(oversized))
	if err := guard.AtomicJSON(path, state); err == nil {
		t.Fatal("oversized state accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("oversized candidate changed the original state bytes")
	}
}

func TestAcknowledgeOversizeCandidatePreservesOriginalState(t *testing.T) {
	dir := t.TempDir()
	s, path := sameNameConflictState(t, dir)
	s.Legacy.Accounts["oversized"] = json.RawMessage(strconv.Quote(strings.Repeat("x", guard.MaxFile)))
	encoded, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if len(encoded)+1 <= guard.MaxFile {
		t.Fatal("oversized fixture did not exceed the state limit")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{
		"account": "a.json", "identity": "identity-new",
		"state_digest": guard.StateDigest(s), "reason": "reviewed oversized",
	})
	if _, err := runCLI(t, dir, input, true); err == nil {
		t.Fatal("oversized acknowledged state was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("oversized acknowledgement changed the original state bytes")
	}
}

func capture(fn func() error) ([]byte, error) {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	os.Stdout = w
	runErr := fn()
	closeErr := w.Close()
	os.Stdout = oldStdout
	out, readErr := io.ReadAll(r)
	if runErr != nil {
		return out, runErr
	}
	if closeErr != nil {
		return out, closeErr
	}
	return out, readErr
}

func TestAcknowledgeRejectsWrongIdentityDigestBusyAndSymlink(t *testing.T) {
	dir := t.TempDir()
	s, _ := sameNameConflictState(t, dir)
	good := guard.StateDigest(s)
	cases := []map[string]string{
		{"account": "a.json", "identity": "identity-old", "state_digest": good, "reason": "x"},
		{"account": "a.json", "identity": "identity-new", "state_digest": "stale", "reason": "x"},
		{"account": "missing.json", "identity": "identity-new", "state_digest": good, "reason": "x"},
	}
	for _, apply := range []bool{false, true} {
		for _, c := range cases {
			raw, _ := json.Marshal(c)
			if _, err := runCLI(t, dir, raw, apply); err == nil {
				t.Fatalf("invalid acknowledgement accepted apply=%v: %v", apply, c)
			}
		}
	}
	lock, err := guard.LockPath(filepath.Join(dir, "worker.lock"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"account": "a.json", "identity": "identity-new", "state_digest": good, "reason": "reviewed"})
	if _, err := runCLI(t, dir, raw, true); err == nil {
		t.Fatal("busy worker was not refused")
	}
	guard.UnlockPath(lock)
	if _, err := runCLI(t, dir, raw, true); err != nil {
		t.Fatalf("valid acknowledgement failed after unlock: %v", err)
	}
}

// An orphaned conflict without its matching transaction is evidence
// corruption and must never delete ownership.
func TestAcknowledgeRejectsOrphanedConflictMap(t *testing.T) {
	dir := t.TempDir()
	s, path := sameNameConflictState(t, dir)
	s.Transactions = map[string]*guard.Transaction{}
	if err := guard.AtomicJSON(path, s); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"account": "a.json", "identity": "identity-new", "state_digest": guard.StateDigest(s), "reason": "reviewed"})
	if _, err := runCLI(t, dir, input, true); err == nil {
		t.Fatal("orphaned conflict acknowledgement accepted")
	}
	loaded, err := guard.LoadState(path)
	if err != nil || loaded.Conflicts["a.json"] == nil {
		t.Fatal("orphaned conflict state was modified")
	}
}

// fakeTickBackend suffices for the post-acknowledgement load/tick assertion.
type fakeTickBackend struct {
	raw     map[string]any
	sample  guard.Sample
	patches int
}

func (f *fakeTickBackend) entry() guard.Entry {
	return guard.Entry{Name: "a.json", AuthIndex: "index", Provider: "codex", Email: "a@example.invalid", Disabled: f.raw["disabled"] == true}
}
func (f *fakeTickBackend) List(context.Context) ([]guard.Entry, error) {
	return []guard.Entry{f.entry()}, nil
}
func (f *fakeTickBackend) Read(context.Context, guard.Entry) (guard.Account, error) {
	return guard.MakeAccount(f.entry(), f.raw, 10)
}
func (f *fakeTickBackend) Patch(context.Context, guard.Account, map[string]any) error {
	f.patches++
	return nil
}
func (f *fakeTickBackend) Probe(context.Context, guard.Account) (guard.Sample, error) {
	return f.sample, nil
}
