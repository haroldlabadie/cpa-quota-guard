package guard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// durableBackend models a management API where a transport error can happen
// after the credential mutation reached disk. State is a real state.json.
type durableBackend struct {
	raw          map[string]any
	mtime        int64
	sample       Sample
	patchApplied bool
	patchReturns error
	afterPatch   func()
	failReads    map[int]bool
	entryName    string
	patches      int
	reads        int
	publishError error
	published    int
}

func (f *durableBackend) entry() Entry {
	name := f.entryName
	if name == "" {
		name = "test.json"
	}
	return Entry{Name: name, AuthIndex: "index", Provider: text(f.raw["type"]), Email: text(f.raw["email"]), Disabled: f.raw["disabled"] == true}
}
func (f *durableBackend) List(context.Context) ([]Entry, error) { return []Entry{f.entry()}, nil }
func (f *durableBackend) Read(context.Context, Entry) (Account, error) {
	f.reads++
	if f.failReads[f.reads] {
		return Account{}, errors.New("readback down")
	}
	return MakeAccount(f.entry(), f.raw, f.mtime)
}
func (f *durableBackend) Patch(_ context.Context, _ Account, fields map[string]any) error {
	f.patches++
	if f.patchApplied {
		for k, v := range fields {
			b, _ := json.Marshal(v)
			var typed any
			_ = json.Unmarshal(b, &typed)
			f.raw[k] = typed
		}
		f.mtime++
	}
	if f.afterPatch != nil {
		hook := f.afterPatch
		f.afterPatch = nil
		hook()
	}
	if f.patchReturns != nil {
		err := f.patchReturns
		f.patchReturns = nil
		return err
	}
	return nil
}

func (f *durableBackend) Probe(context.Context, Account) (Sample, error) { return f.sample, nil }
func (f *durableBackend) Publish(context.Context, Account, Sample) error {
	f.published++
	return f.publishError
}

func durableEngine(t *testing.T, f *durableBackend) (*Engine, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	s := NewState()
	if err := AtomicJSON(path, s); err != nil {
		t.Fatal(err)
	}
	return NewEngine(f, s, settings("codex"), path, true), path
}

func TestIntentPersistedBeforePatchAndPatchUnknownIsDurable(t *testing.T) {
	f := &durableBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(7), patchApplied: true, patchReturns: errors.New("timeout after write")}
	e, path := durableEngine(t, f)
	if err := e.Tick(context.Background(), now, true); err == nil {
		t.Fatal("unknown write hidden")
	}
	disk, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	tx := disk.Transactions["test.json"]
	if tx == nil || tx.Phase != "unknown" || f.patches != 1 || f.raw["disabled"] != true {
		t.Fatalf("intent/uncertain write not retained: tx=%#v patches=%d raw=%#v", tx, f.patches, f.raw)
	}
	// The next process sees a complete target with exact mutation evidence and
	// converges without a second PATCH.
	e2 := NewEngine(f, disk, settings("codex"), path, true)
	if err := e2.Tick(context.Background(), now+1000, true); err != nil {
		t.Fatal(err)
	}
	if f.patches != 1 {
		t.Fatalf("reconcile resent PATCH: %d", f.patches)
	}
	disk, _ = LoadState(path)
	if disk.Transactions["test.json"] != nil || disk.Ownership["test.json"] == nil || disk.Ownership["test.json"].Disable == nil {
		t.Fatalf("reconcile did not converge ownership: %#v", disk)
	}
}

func TestReadbackFailureRetainsIntentAndRecoversAfterRestart(t *testing.T) {
	f := &durableBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(7), patchApplied: true}
	e, path := durableEngine(t, f)
	// Fail the readback that follows a successful PATCH (read 2), after the
	// pre-PATCH read has already been served normally.
	f.failReads = map[int]bool{3: true} // read 1 prewrite, read 2 post-PATCH
	if err := e.Tick(context.Background(), now, true); err == nil {
		t.Fatal("readback failure hidden")
	}
	disk, _ := LoadState(path)
	if disk.Transactions["test.json"] == nil {
		t.Fatalf("post-PATCH intent missing: %#v", disk.Transactions)
	}
	if disk.Transactions["test.json"].Phase != "unknown" && disk.Transactions["test.json"].Phase != "patched" {
		t.Fatalf("post-PATCH phase: %#v", disk.Transactions["test.json"])
	}
	e2 := NewEngine(f, disk, settings("codex"), path, true)
	if err := e2.Tick(context.Background(), now+1000, true); err != nil {
		t.Fatal(err)
	}
	if f.patches != 1 {
		t.Fatal("reconcile repeated PATCH")
	}
	disk, _ = LoadState(path)
	if disk.Transactions["test.json"] != nil || disk.Ownership["test.json"].Disable == nil {
		t.Fatal("readback recovery did not adopt exact target")
	}
}
func TestPatchSuccessThenFinalStateFailureStopsAndReconciles(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.Mkdir(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, "state.json")
	first := &durableBackend{raw: namedRawAuth("codex", "a@example.invalid"), mtime: 10, sample: quota(7), patchApplied: true}
	second := &durableBackend{raw: namedRawAuth("codex", "b@example.invalid"), mtime: 10, sample: quota(50)}
	for name, f := range map[string]*durableBackend{"a.json": first, "b.json": second} {
		f.entryName = name
	}
	moved := filepath.Join(root, "prepared")
	first.afterPatch = func() {
		if err := os.Rename(dataDir, moved); err != nil {
			t.Errorf("move state directory after successful PATCH: %v", err)
			return
		}
		if err := os.WriteFile(dataDir, []byte("blocked state path"), 0600); err != nil {
			t.Errorf("block final state persistence: %v", err)
		}
	}
	s := settings("codex")
	s.Rules = []Rule{
		{Email: "a@example.invalid", Provider: "codex", Enabled: true, ReserveFiveHour: 8, ReserveWeekly: 3},
		{Email: "b@example.invalid", Provider: "codex", Enabled: true, ReserveFiveHour: 8, ReserveWeekly: 3},
	}
	if err := AtomicJSON(path, NewState()); err != nil {
		t.Fatal(err)
	}
	backend := &orderedBackend{files: map[string]*durableBackend{"a.json": first, "b.json": second}, order: []string{"a.json", "b.json"}}
	e := NewEngine(backend, NewState(), s, path, true)
	err := e.Tick(context.Background(), now, true)
	if err == nil || !strings.Contains(err.Error(), "state persistence") {
		t.Fatalf("final state failure not fatal: %v", err)
	}
	if first.patches != 1 || first.raw["disabled"] != true {
		t.Fatalf("successful PATCH not completed before final persistence: %#v patches=%d", first.raw, first.patches)
	}
	if second.reads != 0 || second.patches != 0 {
		t.Fatalf("later account continued after final state persistence failure: reads=%d patches=%d", second.reads, second.patches)
	}
	prepared, err := LoadState(filepath.Join(moved, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	tx := prepared.Transactions["a.json"]
	if tx == nil || tx.Phase != "prepared" {
		t.Fatalf("prepared transaction was not retained on disk: %#v", tx)
	}
	if err := os.Remove(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, dataDir); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	e2 := NewEngine(backend, loaded, s, path, true)
	if err := e2.Tick(context.Background(), now+1000, true); err != nil {
		t.Fatalf("restart reconciliation failed: %v", err)
	}
	if first.patches != 1 {
		t.Fatalf("restart repeated PATCH: %d", first.patches)
	}
	disk, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Transactions["a.json"] != nil || disk.Ownership["a.json"] == nil || disk.Ownership["a.json"].Disable == nil {
		t.Fatalf("restart did not adopt exact target: %#v", disk)
	}
}

func TestFullProtectedMismatchIsStickyConflict(t *testing.T) {
	f := &durableBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(7), patchApplied: true}
	e, path := durableEngine(t, f)
	if err := e.Tick(context.Background(), now, true); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	old := e.state.Ownership["test.json"].Disable
	tx := &Transaction{ID: "review-conflict", CreatedMS: now, Kind: "restore", Account: "test.json", Identity: old.Identity, Old: *old, Target: old.Protected, Phase: "unknown"}
	e.state.Transactions["test.json"] = tx
	e.mu.Unlock()
	f.raw["excluded_models"] = []any{"manual-model"}
	f.mtime++
	if err := e.Tick(context.Background(), now+1000, true); err == nil {
		t.Fatal("concurrent protected change was not surfaced")
	}
	disk, _ := LoadState(path)
	if disk.Conflicts["test.json"] == nil {
		t.Fatalf("conflict missing: %#v", disk)
	}
	// Conflict is sticky: returning fields to a coincidentally matching target
	// does not make it safe to adopt automatically.
	f.raw["excluded_models"] = []any{}
	f.mtime++
	_ = e.Tick(context.Background(), now+2000, true)
	disk, _ = LoadState(path)
	if disk.Conflicts["test.json"] == nil || f.patches != 1 {
		t.Fatal("conflict was auto-adopted or PATCHed")
	}
}
func TestObserveUsesMemoryWithoutStateWrites(t *testing.T) {
	f := &durableBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(1)}
	e, path := durableEngine(t, f)
	before, _ := os.ReadFile(path)
	e.apply = false
	if err := e.Tick(context.Background(), now, true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || f.patches != 0 || f.published != 0 {
		t.Fatal("observe mutated disk or credential")
	}
	s, _, _, _ := e.View()
	if s.Observations["test.json"].Sample == nil {
		t.Fatal("observe did not expose in-memory observation")
	}
}

func TestAdditionalPoolCannotMaskCodexMain(t *testing.T) {
	body := decode(t, `{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":90,"reset_at":1900001000},"secondary_window":{"limit_window_seconds":604800,"used_percent":10,"reset_at":1900100000}},"additional_rate_limits":[{"limit_name":"extra","rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":"broken"}}}]}`)
	windows, err := ParseCodex(body)
	if err != nil || len(windows) != 2 || windows[0].Remaining != 10 {
		t.Fatalf("malformed addon obscured main: %#v %v", windows, err)
	}
	bad := decode(t, `{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":92,"reset_at":1900001000},"secondary_window":{"limit_window_seconds":604800,"used_percent":"broken","reset_at":1900100000}}}`)
	if _, err = ParseCodex(bad); err == nil {
		t.Fatal("malformed main pool accepted")
	}
	sample := Sample{ObservedMS: now, Windows: windows}
	if len(Reasons(&sample, "codex", "all", settings("codex").Rules[0], now)) != 0 {
		t.Fatal("additional pool changed whole-credential decision")
	}
}

func TestMainPoolAddonExhaustionDoesNotProtect(t *testing.T) {
	body := decode(t, `{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":91,"reset_at":1900001000},"secondary_window":{"limit_window_seconds":604800,"used_percent":10,"reset_at":1900100000}},"additional_rate_limits":[{"limit_name":"extra","rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":100,"reset_at":1900001000}}}]}`)
	windows, err := ParseCodex(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) < 2 {
		t.Fatal("main windows missing")
	}
	sample := Sample{ObservedMS: now, Windows: windows}
	if got := Reasons(&sample, "codex", "all", settings("codex").Rules[0], now); len(got) != 0 {
		t.Fatalf("additional pool triggered protection: %v", got)
	}
}

type orderedBackend struct {
	files map[string]*durableBackend
	order []string
	reads int
}

func (m *orderedBackend) List(context.Context) ([]Entry, error) {
	out := []Entry{}
	for _, name := range m.order {
		out = append(out, m.files[name].entry())
	}
	return out, nil
}
func (m *orderedBackend) Read(ctx context.Context, e Entry) (Account, error) {
	m.reads++
	return m.files[e.Name].Read(ctx, e)
}
func (m *orderedBackend) Patch(ctx context.Context, a Account, fields map[string]any) error {
	return m.files[a.Entry.Name].Patch(ctx, a, fields)
}
func (m *orderedBackend) Probe(ctx context.Context, a Account) (Sample, error) {
	return m.files[a.Entry.Name].Probe(ctx, a)
}

func namedRawAuth(provider, email string) map[string]any {
	raw := rawAuth(provider)
	raw["email"] = email
	return raw
}

func TestIntentPersistenceFailureMeansZeroPatchAndStopsTick(t *testing.T) {
	first := &durableBackend{raw: namedRawAuth("codex", "a@example.invalid"), mtime: 10, sample: quota(7)}
	second := &durableBackend{raw: namedRawAuth("codex", "b@example.invalid"), mtime: 10, sample: quota(7)}
	for name, f := range map[string]*durableBackend{"a.json": first, "b.json": second} {
		f.entryName = name
	}
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "state.json")
	s := settings("codex")
	s.Rules = []Rule{{Email: "a@example.invalid", Provider: "codex", Enabled: true, ReserveFiveHour: 8, ReserveWeekly: 3}, {Email: "b@example.invalid", Provider: "codex", Enabled: true, ReserveFiveHour: 8, ReserveWeekly: 3}}
	e := NewEngine(&orderedBackend{files: map[string]*durableBackend{"a.json": first, "b.json": second}, order: []string{"a.json", "b.json"}}, NewState(), s, path, true)
	if err := e.Tick(context.Background(), now, true); err == nil || !strings.Contains(err.Error(), "state persistence") {
		t.Fatalf("state failure not fatal: %v", err)
	}
	if first.patches != 0 || second.patches != 0 {
		t.Fatalf("PATCH occurred without a durable intent: first=%d second=%d", first.patches, second.patches)
	}
	if second.reads != 0 {
		t.Fatal("later account continued after fatal persistence failure")
	}
}

func TestAdditionalPoolLabelAllCannotJoinMainScope(t *testing.T) {
	body := decode(t, `{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":10,"reset_at":1900001000},"secondary_window":{"limit_window_seconds":604800,"used_percent":10,"reset_at":1900100000}},"additional_rate_limits":[{"limit_name":"all","rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":100,"reset_at":1900001000}}}]}`)
	windows, err := ParseCodex(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range windows {
		if strings.Contains(w.ID, "additional") && w.Scope == "all" {
			t.Fatal("additional pool escaped its namespace")
		}
	}
	sample := Sample{ObservedMS: now, Windows: windows}
	if got := Reasons(&sample, "codex", "all", settings("codex").Rules[0], now); len(got) != 0 {
		t.Fatalf("label=all additional pool protected main: %v", got)
	}
}
