package guard

import (
	"encoding/json"
	"testing"
)

func TestDiagnoseOrphansRequiresNativeProvenProof(t *testing.T) {
	s := NewState()
	s.Legacy.Accounts["a.json"] = json.RawMessage(`{"provider":"codex","owned_revision":{"mtime_ns":1}}`)
	a, _ := MakeAccount(Entry{Name: "a.json", Provider: "codex", AuthIndex: "i", Email: "a@example.invalid"}, rawAuth("codex"), 10)
	rows := DiagnoseOrphans(s, map[string]Account{"a.json": a})
	if len(rows) != 1 || rows[0]["status"] != "orphan" || rows[0]["reason"] != "legacy revision alone is not ownership evidence" {
		t.Fatalf("legacy revision guessed ownership: %#v", rows)
	}
}

func TestVerifyNativeProofRejectsLegacyOnlyAndMismatch(t *testing.T) {
	s := NewState()
	s.Legacy.Accounts["a.json"] = json.RawMessage(`{"provider":"codex","owned_revision":{"mtime_ns":1}}`)
	a, _ := MakeAccount(Entry{Name: "a.json", Provider: "codex", AuthIndex: "i", Email: "a@example.invalid"}, rawAuth("codex"), 10)
	if err := VerifyNativeProof(s, "a.json", a); err == nil {
		t.Fatal("missing native proof accepted")
	}
	proof := a.Snapshot
	s.Ownership["a.json"] = &Ownership{Disable: &proof}
	if err := VerifyNativeProof(s, "a.json", a); err == nil {
		t.Fatal("unowned disabled proof accepted")
	}
	a.Snapshot.Protected.Disabled = true
	a.Snapshot.Protected.Owner = strPtr(Owner)
	a.Snapshot.ProtectedHash = Digest(a.Snapshot.Protected)
	if err := VerifyNativeProof(s, "a.json", a); err == nil {
		t.Fatal("stale proof accepted")
	}
	proof = a.Snapshot
	s.Ownership["a.json"].Disable = &proof
	if err := VerifyNativeProof(s, "a.json", a); err != nil {
		t.Fatal(err)
	}
	s.Transactions["a.json"] = &Transaction{ID: "tx"}
	if err := VerifyNativeProof(s, "a.json", a); err == nil {
		t.Fatal("pending transaction accepted")
	}
}
