package main

import (
	"encoding/json"
	"github.com/haroldlabadie/cpa-quota-guard/internal/guard"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupInstance(t *testing.T) *instance {
	t.Helper()
	stopCurrent()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "key")
	if err := os.WriteFile(secret, []byte("TEST_KEY"), 0600); err != nil {
		t.Fatal(err)
	}
	yaml := "enabled: false\npriority: 0\nmode: observe\ndata_dir: " + dir + "\nsecret_file: " + secret + "\ncpamp_url: http://127.0.0.1:18317\n"
	raw, _ := json.Marshal(map[string]any{"schema_version": 6, "config_yaml": []byte(yaml)})
	if err := configure(raw); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopCurrent)
	return currentInstance()
}
func unpack(t *testing.T, b []byte) managementResponse {
	t.Helper()
	var env struct {
		OK     bool               `json:"ok"`
		Result managementResponse `json:"result"`
	}
	if err := json.Unmarshal(b, &env); err != nil || !env.OK {
		t.Fatal(string(b), err)
	}
	return env.Result
}
func manage(t *testing.T, method, path string, body []byte, etag string) managementResponse {
	t.Helper()
	raw, _ := json.Marshal(managementRequest{Method: method, Path: path, Body: body, Headers: http.Header{"If-Match": {etag}}})
	b, err := handleManagement(raw)
	if err != nil {
		t.Fatal(err)
	}
	return unpack(t, b)
}
func TestLifecycleQuiesceReleasesLock(t *testing.T) {
	i := setupInstance(t)
	if _, err := lockFile(filepath.Join(i.cfg.DataDir, "worker.lock")); err == nil {
		t.Fatal("concurrent lock acquired")
	}
	stopCurrent()
	f, err := lockFile(filepath.Join(i.cfg.DataDir, "worker.lock"))
	if err != nil {
		t.Fatal("quiesce did not release", err)
	}
	unlockFile(f)
	stopCurrent()
}
func TestManagementSettingsRevisionAndUnknownFields(t *testing.T) {
	setupInstance(t)
	r := manage(t, "GET", managementBase+"/settings", nil, "")
	etag := r.Headers.Get("ETag")
	if etag == "" {
		t.Fatal("missing etag")
	}
	body := []byte(`{"schema":1,"rules":[{"email":"test@example.invalid","provider":"codex","enabled":true,"reserve_five_hour":8,"reserve_weekly":3}]}`)
	if r = manage(t, "PUT", managementBase+"/settings", body, ""); r.StatusCode != 428 {
		t.Fatal("missing If-Match")
	}
	if r = manage(t, "PUT", managementBase+"/settings", body, `"stale"`); r.StatusCode != 412 {
		t.Fatal("stale If-Match")
	}
	bad := []byte(`{"schema":1,"rules":[],"secret_file":"not-allowed"}`)
	if r = manage(t, "PUT", managementBase+"/settings", bad, etag); r.StatusCode != 400 {
		t.Fatal("unknown fields")
	}
	if r = manage(t, "PUT", managementBase+"/settings", body, etag); r.StatusCode != 200 {
		t.Fatal(string(r.Body))
	}
	if r = manage(t, "PUT", managementBase+"/settings", body, etag); r.StatusCode != 412 {
		t.Fatal("stale save accepted")
	}
	if r = manage(t, "GET", managementBase+"/status", nil, ""); strings.Contains(string(r.Body), "TEST_KEY") {
		t.Fatal("secret leaked")
	}
}
func TestPublicPageOnlyStaticShell(t *testing.T) {
	i := setupInstance(t)
	body, err := settingsPage(false)
	if err != nil {
		t.Fatal(err)
	}
	r := unpack(t, body)
	if r.Headers.Get("Content-Security-Policy") == "" {
		t.Fatal("CSP missing")
	}
	if strings.Contains(string(r.Body), "TEST_KEY") || strings.Contains(string(r.Body), i.cfg.DataDir) {
		t.Fatal("private state in public page")
	}
	if strings.Contains(string(r.Body), "innerHTML") {
		t.Fatal("unsafe rendering")
	}
}
func TestCorruptStateStopsNewInstance(t *testing.T) {
	i := setupInstance(t)
	if err := os.WriteFile(filepath.Join(i.cfg.DataDir, "state.json"), []byte(`{"schema":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	yaml := "enabled: false\nmode: observe\ndata_dir: " + i.cfg.DataDir + "\nsecret_file: " + i.cfg.SecretFile + "\ncpamp_url: http://127.0.0.1:18317\n"
	raw, _ := json.Marshal(map[string]any{"schema_version": 6, "config_yaml": []byte(yaml)})
	if configure(raw) == nil {
		t.Fatal("corrupt state accepted")
	}
	if _, _, _, running := i.engine.View(); running {
		t.Fatal("old worker running")
	}
}
func TestURLRestriction(t *testing.T) {
	for _, u := range []string{"https://public.example.com", "http://user:pass@cpamp:18317", "http://cpamp:18317/v0", "http://cpamp:18317?key=x", "http://8.8.8.8"} {
		if validateBase(u) == nil {
			t.Fatal("unsafe base", u)
		}
	}
	for _, u := range []string{"http://cpamp:18317", "http://127.0.0.1:18317", "http://10.1.2.3:18317"} {
		if validateBase(u) != nil {
			t.Fatal("valid base rejected", u)
		}
	}
}
func TestPanicStopDoesNotJoinInCallback(t *testing.T) {
	stopCurrent()
	done := make(chan struct{})
	i := &instance{cancel: func() {}, done: done}
	current.Lock()
	current.inst = i
	current.Unlock()
	returned := make(chan struct{})
	go func() { panicStop(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		close(done)
		t.Fatal("panic callback blocked")
	}
	close(done)
	stopCurrent()
}
func TestSettingsValidateKnownDefaults(t *testing.T) {
	s := guard.Settings{Schema: 1, Rules: []guard.Rule{{Email: "x@example.invalid", Provider: "codex", Enabled: true, ReserveFiveHour: 8, ReserveWeekly: 3}}}
	if err := guard.ValidateSettings(s); err != nil {
		t.Fatal(err)
	}
}
func TestApplyRequiresExplicitState(t *testing.T) {
	i := setupInstance(t)
	yaml := "enabled: false\nmode: apply\ndata_dir: " + i.cfg.DataDir + "\nsecret_file: " + i.cfg.SecretFile + "\ncpamp_url: http://127.0.0.1:18317\n"
	raw, _ := json.Marshal(map[string]any{"schema_version": 6, "config_yaml": []byte(yaml)})
	if err := configure(raw); err == nil || !strings.Contains(err.Error(), "state.json") {
		t.Fatal("missing state did not stop apply", err)
	}
	f, err := lockFile(filepath.Join(i.cfg.DataDir, "worker.lock"))
	if err != nil {
		t.Fatal("failed startup retained lock", err)
	}
	unlockFile(f)
	s := guard.NewState()
	s.Initialization = &guard.Initialization{Kind: "fresh-install", CreatedMS: time.Now().UnixMilli()}
	if err = guard.AtomicJSON(filepath.Join(i.cfg.DataDir, "state.json"), s); err != nil {
		t.Fatal(err)
	}
	if err = configure(raw); err != nil {
		t.Fatal("explicit fresh state rejected", err)
	}
}

func TestSchemaOneObserveLoadsInMemoryAndApplyPersists(t *testing.T) {
	i := setupInstance(t)
	legacy := `{"schema":1,"plugin":"cpa-quota-guard","legacy":{"version":2,"accounts":{}},"ownership_proofs":{},"observations":{}}`
	statePath := filepath.Join(i.cfg.DataDir, "state.json")
	if err := os.WriteFile(statePath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	yaml := "enabled: false\nmode: observe\ndata_dir: " + i.cfg.DataDir + "\nsecret_file: " + i.cfg.SecretFile + "\ncpamp_url: http://127.0.0.1:18317\n"
	raw, _ := json.Marshal(map[string]any{"schema_version": 6, "config_yaml": []byte(yaml)})
	if err := configure(raw); err != nil {
		t.Fatalf("observe rejected valid schema one: %v", err)
	}
	if b, _ := os.ReadFile(statePath); !strings.Contains(string(b), `"schema":1`) {
		t.Fatal("observe persisted schema upgrade")
	}
	state, _, _, _ := i.engine.View()
	if state.Schema != 2 || state.Transactions == nil || state.Conflicts == nil {
		t.Fatal("observe did not upgrade in-memory state")
	}
	yaml = "enabled: false\nmode: apply\ndata_dir: " + i.cfg.DataDir + "\nsecret_file: " + i.cfg.SecretFile + "\ncpamp_url: http://127.0.0.1:18317\n"
	raw, _ = json.Marshal(map[string]any{"schema_version": 6, "config_yaml": []byte(yaml)})
	if err := configure(raw); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(statePath)
	if !strings.Contains(string(b), `"schema": 2`) {
		t.Fatal("apply did not persist schema upgrade")
	}
	stopCurrent()
	if err := configure(raw); err != nil {
		t.Fatal("upgraded state rejected on reopen")
	}
}

func TestStateSchemaTwoRejectsFuture(t *testing.T) {
	for _, raw := range []string{`{"schema":3,"plugin":"cpa-quota-guard","legacy":{"version":2,"accounts":{}},"ownership_proofs":{},"observations":{}}`, `{"schema":2,"plugin":"cpa-quota-guard","legacy":{"version":2,"accounts":{}},"ownership_proofs":{},"observations":{},"transactions":{},"conflicts":{},"journal":[],"unknown":true}`} {
		if _, err := guard.ParseState([]byte(raw)); err == nil {
			t.Fatal("invalid state accepted")
		}
	}
}
