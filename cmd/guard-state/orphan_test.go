package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/haroldlabadie/cpa-quota-guard/internal/guard"
)

func TestOrphanDiagnoseAndNativeProofDryRun(t *testing.T) {
	dir := t.TempDir()
	s := guard.NewState()
	s.Legacy.Accounts["a.json"] = json.RawMessage(`{"provider":"codex","owned_revision":{"mtime_ns":1}}`)
	a, _ := guard.MakeAccount(guard.Entry{Name: "a.json", Provider: "codex", AuthIndex: "i", Email: "a@example.invalid"}, map[string]any{"type": "codex", "email": "a@example.invalid", "disabled": false, "access_token": "fake"}, 10)
	rows, _ := json.Marshal([]map[string]any{{"name": "a.json", "identity": a.Snapshot.Identity, "snapshot": a.Snapshot}})
	currentPath := filepath.Join(dir, "current.json")
	if err := os.WriteFile(currentPath, rows, 0600); err != nil {
		t.Fatal(err)
	}
	if err := guard.AtomicJSON(filepath.Join(dir, "state.json"), s); err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error { return diagnose([]string{"--data-dir", dir, "--current", currentPath}) })
	if err != nil || !bytes.Contains(out, []byte(`"orphan"`)) {
		t.Fatalf("diagnosis failed: %v %s", err, out)
	}
	in, _ := json.Marshal(map[string]string{"account": "a.json"})
	if err = verifyNativeWithInput(dir, currentPath, in, false); err == nil {
		t.Fatal("missing native proof accepted")
	}
	disk, _ := guard.LoadState(filepath.Join(dir, "state.json"))
	if len(disk.Ownership) != 0 {
		t.Fatal("dry-run mutated state")
	}
}

func verifyNativeWithInput(dir, currentPath string, input []byte, apply bool) error {
	old := os.Stdin
	f, _ := os.CreateTemp("", "native-proof")
	defer os.Remove(f.Name())
	_, _ = f.Write(input)
	_, _ = f.Seek(0, 0)
	os.Stdin = f
	defer func() { os.Stdin = old }()
	args := []string{"--data-dir", dir, "--current", currentPath}
	if apply {
		args = append(args, "--apply")
	}
	return verifyNative(args)
}
