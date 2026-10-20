package guard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const now int64 = 1900000000000

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func quota(remaining float64) Sample {
	return Sample{ObservedMS: now, Windows: []Window{{"5h", "five_hour", "all", remaining, now + FiveHourMS, FiveHourMS}, {"week", "weekly", "all", 90, now + WeekMS, WeekMS}}}
}
func settings(provider string) Settings {
	return Settings{Schema: 1, Rules: []Rule{{Email: "test@example.invalid", Provider: provider, Enabled: true, ReserveFiveHour: 8, ReserveWeekly: 3}}}
}
func rawAuth(provider string) map[string]any {
	return map[string]any{"type": provider, "email": "test@example.invalid", "account_id": "account", "project_id": "project", "access_token": "FAKE_TOKEN", "disabled": false}
}

type fakeBackend struct {
	raw          map[string]any
	mtime        int64
	sample       Sample
	probeErr     bool
	patchErr     bool
	badReadback  bool
	patches      int
	tokenRefresh bool
	beforeRead   func()
	reads        int
	published    int
	publishErr   bool
}

func (f *fakeBackend) entry() Entry {
	return Entry{Name: "test.json", AuthIndex: "index", Provider: text(f.raw["type"]), Email: text(f.raw["email"]), Disabled: f.raw["disabled"] == true}
}
func (f *fakeBackend) List(context.Context) ([]Entry, error) { return []Entry{f.entry()}, nil }
func (f *fakeBackend) Read(_ context.Context, _ Entry) (Account, error) {
	f.reads++
	if f.beforeRead != nil {
		f.beforeRead()
	}
	return MakeAccount(f.entry(), f.raw, f.mtime)
}
func (f *fakeBackend) Patch(_ context.Context, _ Account, fields map[string]any) error {
	f.patches++
	if f.patchErr {
		return errors.New("fake")
	}
	if !f.badReadback {
		for k, v := range fields {
			b, _ := json.Marshal(v)
			var typed any
			_ = json.Unmarshal(b, &typed)
			f.raw[k] = typed
		}
	}
	f.mtime++
	return nil
}
func (f *fakeBackend) Probe(context.Context, Account) (Sample, error) {
	if f.tokenRefresh {
		f.raw["access_token"] = "NEW_FAKE_TOKEN"
		f.mtime++
	}
	if f.probeErr {
		return Sample{}, errors.New("fake")
	}
	return f.sample, nil
}
func (f *fakeBackend) Publish(context.Context, Account, Sample) error {
	f.published++
	if f.publishErr {
		return errors.New("fake")
	}
	return nil
}
func engine(t *testing.T, f *fakeBackend) *Engine {
	t.Helper()
	return NewEngine(f, NewState(), settings(text(f.raw["type"])), filepath.Join(t.TempDir(), "state.json"), true)
}
func tick(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Tick(context.Background(), now, true); err != nil {
		t.Fatal(err)
	}
}
func TestCodexBoundaryRecoveryAndWeekly(t *testing.T) {
	f := &fakeBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(8)}
	e := engine(t, f)
	tick(t, e)
	if f.raw["disabled"] != true || f.patches != 1 {
		t.Fatal("8% must protect")
	}
	f.sample = quota(9)
	tick(t, e)
	if f.raw["disabled"] != false || f.patches != 2 {
		t.Fatal("9% must recover")
	}
	f.sample.Windows[1].Remaining = 3
	tick(t, e)
	if f.raw["disabled"] != true {
		t.Fatal("weekly independent")
	}
}
func TestTransientProbeFailureRetainsRecentSample(t *testing.T) {
	f := &fakeBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(9)}
	e := engine(t, f)
	tick(t, e)
	if f.patches != 0 {
		t.Fatal("healthy sample must not patch")
	}
	f.probeErr = true
	if err := e.Tick(context.Background(), now+30000, true); err != nil {
		t.Fatal(err)
	}
	if f.raw["disabled"] == true || f.patches != 0 {
		t.Fatal("transient probe failure discarded a recent safe sample")
	}
	s, _, _, _ := e.View()
	obs := s.Observations["test.json"]
	if obs.Sample == nil || obs.Sample.ObservedMS != now || obs.Error == "" {
		t.Fatalf("retained sample not visible: %#v", obs)
	}
	f.sample = quota(8)
	f.sample.ObservedMS = now + 91000
	f.probeErr = false
	if err := e.Tick(context.Background(), now+91000, true); err != nil {
		t.Fatal(err)
	}
	if f.raw["disabled"] != true || f.patches != 1 {
		t.Fatal("expired sample must not authorize recovery")
	}
}
func TestTransientProbeFailureNeverRestores(t *testing.T) {
	f := &fakeBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(8)}
	e := engine(t, f)
	tick(t, e)
	if f.raw["disabled"] != true {
		t.Fatal("unsafe sample must protect")
	}
	f.probeErr = true
	if err := e.Tick(context.Background(), now+30000, true); err != nil {
		t.Fatal(err)
	}
	if f.raw["disabled"] != true || f.patches != 1 {
		t.Fatal("probe failure must retain protection")
	}
}
func TestUnknownAndExpiredNeverRecover(t *testing.T) {
	for _, kind := range []string{"probe", "expired", "missing", "stale", "future"} {
		t.Run(kind, func(t *testing.T) {
			f := &fakeBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(9)}
			switch kind {
			case "probe":
				f.probeErr = true
			case "expired":
				f.sample.Windows[0].EndMS = now
			case "missing":
				f.sample.Windows = f.sample.Windows[1:]
			case "stale":
				f.sample.ObservedMS = now - 90001
			case "future":
				f.sample.ObservedMS = now + 5000
			}
			e := engine(t, f)
			tick(t, e)
			if f.raw["disabled"] != true {
				t.Fatal("unknown quota must protect")
			}
		})
	}
}
func TestManualBaselineNeverRecovered(t *testing.T) {
	f := &fakeBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(99)}
	f.raw["disabled"] = true
	e := engine(t, f)
	tick(t, e)
	if f.patches != 0 {
		t.Fatal("manual disable mutated")
	}
	s, _, _, _ := e.View()
	if !s.Observations["test.json"].Manual {
		t.Fatal("manual not shown")
	}
}
func TestOwnershipChanges(t *testing.T) {
	for _, kind := range []string{"token", "unrelated", "same-content", "owner", "identity"} {
		t.Run(kind, func(t *testing.T) {
			f := &fakeBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(8)}
			e := engine(t, f)
			tick(t, e)
			switch kind {
			case "token":
				f.raw["access_token"] = "other"
			case "unrelated":
				f.raw["prefix"] = "configured-route"
			case "owner":
				f.raw["quota_guard_owner"] = nil
			case "identity":
				f.raw["account_id"] = "replacement"
			}
			f.mtime++
			f.sample = quota(20)
			tick(t, e)
			want := kind == "token" || kind == "unrelated"
			if (f.raw["disabled"] == false) != want {
				t.Fatalf("recovery=%v wanted %v", f.raw["disabled"] == false, want)
			}
		})
	}
}
func TestProbeTokenRefreshPreservesOwner(t *testing.T) {
	f := &fakeBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(8)}
	e := engine(t, f)
	tick(t, e)
	f.sample = quota(20)
	f.tokenRefresh = true
	tick(t, e)
	if f.raw["disabled"] != false {
		t.Fatal("probe refresh prevented proven recovery")
	}
}
func TestPatchFailureDoesNotOwn(t *testing.T) {
	for _, bad := range []bool{false, true} {
		f := &fakeBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(8), patchErr: !bad, badReadback: bad}
		e := engine(t, f)
		if e.Tick(context.Background(), now, true) == nil {
			t.Fatal("failure hidden")
		}
		s, _, _, _ := e.View()
		if s.Ownership["test.json"].Disable != nil {
			t.Fatal("failed patch registered ownership")
		}
	}
}
func familySample(gemini, third float64) Sample {
	s := quota(gemini)
	for i := range s.Windows {
		s.Windows[i].Scope = "gemini"
	}
	t := quota(third)
	for i := range t.Windows {
		t.Windows[i].Scope = "third_party"
	}
	s.Windows = append(s.Windows, t.Windows...)
	return s
}
func TestFamilyIsolationAndBaseline(t *testing.T) {
	f := &fakeBackend{raw: rawAuth("antigravity"), mtime: 10, sample: familySample(8, 90)}
	f.raw["excluded_models"] = []any{"manual-model"}
	e := engine(t, f)
	tick(t, e)
	a, _ := f.Read(context.Background(), f.entry())
	if strings.Join(a.Snapshot.Protected.Excluded, ",") != "gemini-*,manual-model" || a.Snapshot.Protected.Disabled {
		t.Fatal("family isolation")
	}
	f.sample = familySample(90, 8)
	tick(t, e)
	a, _ = f.Read(context.Background(), f.entry())
	if strings.Join(a.Snapshot.Protected.Excluded, ",") != "claude-*,gpt-*,manual-model" {
		t.Fatal(a.Snapshot.Protected.Excluded)
	}
	f.sample = familySample(90, 90)
	tick(t, e)
	a, _ = f.Read(context.Background(), f.entry())
	if strings.Join(a.Snapshot.Protected.Excluded, ",") != "manual-model" || a.Snapshot.Protected.Family != nil {
		t.Fatal("baseline lost")
	}
}
func TestFamilyManualOverrideConvertsToBaseline(t *testing.T) {
	f := &fakeBackend{raw: rawAuth("antigravity"), mtime: 10, sample: familySample(8, 90)}
	e := engine(t, f)
	tick(t, e)
	f.mtime++
	f.sample = familySample(90, 8)
	tick(t, e)
	a, _ := f.Read(context.Background(), f.entry())
	if strings.Join(a.Snapshot.Protected.Excluded, ",") != "claude-*,gemini-*,gpt-*" {
		t.Fatal("manual retained family removed")
	}
	f.sample = familySample(90, 90)
	tick(t, e)
	a, _ = f.Read(context.Background(), f.entry())
	if strings.Join(a.Snapshot.Protected.Excluded, ",") != "gemini-*" {
		t.Fatal("manual baseline must remain")
	}
}
func TestIdentityReplacementNoOwnership(t *testing.T) {
	f := &fakeBackend{raw: rawAuth("antigravity"), mtime: 10, sample: familySample(8, 90)}
	e := engine(t, f)
	tick(t, e)
	f.raw["project_id"] = "replacement"
	f.mtime++
	f.sample = familySample(90, 90)
	tick(t, e)
	a, _ := f.Read(context.Background(), f.entry())
	if len(a.Snapshot.Protected.Excluded) == 0 {
		t.Fatal("same-name replacement inherited ownership")
	}
}
func TestObserveAndWhitelist(t *testing.T) {
	f := &fakeBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(0)}
	e := engine(t, f)
	e.apply = false
	tick(t, e)
	if f.patches != 0 || f.published != 0 {
		t.Fatal("observe wrote")
	}
	e.apply = true
	s := settings("codex")
	s.Rules[0].Email = "other@example.invalid"
	e.SetSettings(s)
	tick(t, e)
	if f.patches != 0 {
		t.Fatal("unlisted account mutated")
	}
}
func TestPublicationFailureDoesNotSkipProtection(t *testing.T) {
	f := &fakeBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(0), publishErr: true}
	e := engine(t, f)
	tick(t, e)
	if f.raw["disabled"] != true {
		t.Fatal("display failure bypassed protection")
	}
}
func TestStateCorruptionAndPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := NewState()
	s.Legacy.Accounts["unknown"] = json.RawMessage(`{"custom":"保留"}`)
	if err := AtomicJSON(path, s); err != nil {
		t.Fatal(err)
	}
	out, err := LoadState(path)
	var preserved map[string]any
	_ = json.Unmarshal(out.Legacy.Accounts["unknown"], &preserved)
	if err != nil || preserved["custom"] != "保留" {
		t.Fatal("legacy lost", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("state permissions")
	}
	if err = os.WriteFile(path, []byte(`{"schema":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadState(path); err == nil {
		t.Fatal("corrupt state accepted")
	}
}
func TestStateCorruptionRejectsNonObjectLegacyEvidence(t *testing.T) {
	s := NewState()
	bad, _ := json.Marshal(s)
	var obj map[string]any
	_ = json.Unmarshal(bad, &obj)
	obj["legacy"] = map[string]any{"version": 2, "accounts": map[string]any{"a.json": []any{1}}}
	bad, _ = json.Marshal(obj)
	if _, err := ParseState(bad); err == nil {
		t.Fatal("non-object legacy evidence accepted")
	}
}
func TestStateDigestCoversFullProvenance(t *testing.T) {
	s := NewState()
	s.Legacy.Accounts["unknown"] = json.RawMessage(`{"custom":"first"}`)
	first := StateDigest(s)
	s.Legacy.Accounts["unknown"] = json.RawMessage(`{"custom":"second"}`)
	if StateDigest(s) == first {
		t.Fatal("state digest ignored legacy source evidence")
	}
	s.Migration = json.RawMessage(`{"id":"migration-one"}`)
	second := StateDigest(s)
	s.Migration = json.RawMessage(`{"id":"migration-two"}`)
	if StateDigest(s) == second {
		t.Fatal("state digest ignored migration provenance")
	}
}
func TestQuotaParsers(t *testing.T) {
	good := `{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":92,"reset_at":1900001000},"secondary_window":{"limit_window_seconds":604800,"used_percent":2,"reset_at":1900100000}},"additional_rate_limits":[{"limit_name":"extra","rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":99,"reset_at":1900001000}}}]}`
	windows, err := ParseCodex(decode(t, good))
	if err != nil || len(windows) != 3 || windows[2].Scope != "additional:extra" {
		t.Fatal(windows, err)
	}
	for _, bad := range []string{strings.Replace(good, `"used_percent":92`, `"used_percent":true`, 1), strings.Replace(good, `"used_percent":92`, `"used_percent":-1`, 1), strings.Replace(good, `"used_percent":92`, `"used_percent":"NaN"`, 1), `{"rate_limit":{}}`} {
		if _, err = ParseCodex(decode(t, bad)); err == nil {
			t.Fatal("invalid accepted")
		}
	}
	free, _ := ParseCodex(decode(t, `{"rate_limit":{"primary_window":{"limit_window_seconds":604800,"used_percent":2,"reset_at":1900100000}}}`))
	sample := Sample{ObservedMS: now, Windows: free}
	if len(Reasons(&sample, "codex", "all", settings("codex").Rules[0], now)) == 0 {
		t.Fatal("free must not assume 5h")
	}
	ag := `{"groups":[{"displayName":"Gemini","buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.08,"resetTime":"2030-03-17T20:00:00Z"},{"bucketId":"gemini-weekly","window":"weekly","remainingFraction":0.9,"resetTime":"2030-03-18T20:00:00Z"}]}]}`
	if w, err := ParseAntigravity(decode(t, ag), true); err != nil || len(w) != 2 {
		t.Fatal(w, err)
	}
	missing := strings.Replace(ag, `"remainingFraction":0.08,`, "", 1)
	w, err := ParseAntigravity(decode(t, missing), true)
	if err != nil || w[0].Remaining != 0 {
		t.Fatal("missing fraction must fail-safe zero")
	}
	broken := strings.Replace(ag, `"remainingFraction":0.08`, `"remainingFraction":true`, 1)
	if _, err := ParseAntigravity(decode(t, broken), true); err == nil {
		t.Fatal("partial invalid family accepted")
	}
}
func TestStrictSettings(t *testing.T) {
	for _, raw := range []string{`{"schema":1,"rules":[],"secret":"x"}`, `{"schema":1,"rules":[]} {}`, `{"schema":1,"rules":null}`} {
		var s Settings
		err := StrictJSON([]byte(raw), &s)
		if err == nil {
			err = ValidateSettings(s)
		}
		if err == nil {
			t.Fatal("invalid settings accepted")
		}
	}
}
func TestRunCancellation(t *testing.T) {
	f := &fakeBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(0)}
	e := engine(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { e.Run(ctx, time.Hour, time.Hour); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not join")
	}
}
func TestProofContainsNoRawTokens(t *testing.T) {
	a, err := MakeAccount(Entry{Name: "test.json", Provider: "codex", AuthIndex: "i"}, rawAuth("codex"), 10)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(a.Snapshot)
	if strings.Contains(string(raw), "FAKE_TOKEN") {
		t.Fatal("secret escaped")
	}
}
