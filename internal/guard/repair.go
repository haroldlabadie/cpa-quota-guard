package guard

import (
	"encoding/json"
	"errors"
)

// DiagnoseOrphans is a read-only report over legacy ownership evidence. A
// native proof is the only machine evidence that can restore ownership; a
// legacy revision alone is always reported as orphan unless a native proof has
// already been transferred by the Python migrator.
func DiagnoseOrphans(s State, current map[string]Account) []map[string]any {
	out := []map[string]any{}
	for name, raw := range s.Legacy.Accounts {
		var rec map[string]any
		if json.Unmarshal(raw, &rec) != nil || rec == nil {
			continue
		}
		for _, item := range []struct{ kind, key string }{{"disable", "owned_revision"}, {"family", "family_revision"}} {
			proof := snapshotFor(s, name, item.kind)
			if rec[item.key] == nil && proof == nil {
				continue
			}
			row := map[string]any{"account": name, "kind": item.kind, "record_digest": recordDigest(raw), "status": "orphan"}
			a, ok := current[name]
			if !ok {
				row["reason"] = "current credential missing"
				out = append(out, row)
				continue
			}
			row["identity"] = a.Snapshot.Identity
			if proof == nil {
				row["reason"] = "legacy revision alone is not ownership evidence"
			} else if proof.Identity != a.Snapshot.Identity {
				row["reason"] = "native proof identity mismatch"
			} else if !Proven(proof, a.Snapshot) {
				row["reason"] = "native proof no longer matches protected state"
			} else if item.kind == "disable" && (!a.Snapshot.Protected.Disabled || !HasOwner(a.Snapshot.Protected.Owner, Owner)) {
				row["reason"] = "disable proof lacks plugin owner or disabled state"
			} else if item.kind == "family" && (a.Snapshot.Protected.Family == nil || a.Snapshot.Protected.Family.Owner != FamilyOwner) {
				row["reason"] = "family proof lacks family owner control"
			} else {
				row["status"] = "verified"
				delete(row, "reason")
			}
			out = append(out, row)
		}
	}
	return out
}
func snapshotFor(s State, name, kind string) *Snapshot {
	if s.Ownership[name] == nil {
		return nil
	}
	if kind == "disable" {
		return s.Ownership[name].Disable
	}
	return s.Ownership[name].Family
}

// VerifyNativeProof accepts only an already existing native proof that exactly
// matches the current credential. A missing proof is not a successful
// verification: callers must not use this command to infer ownership from a
// legacy revision or a caller-supplied snapshot.
func VerifyNativeProof(s State, name string, current Account) error {
	if s.Transactions[name] != nil || s.Conflicts[name] != nil {
		return errors.New("account has pending transaction or conflict")
	}
	disable := snapshotFor(s, name, "disable")
	family := snapshotFor(s, name, "family")
	if disable == nil && family == nil {
		return errors.New("no native proof exists for account")
	}
	if disable != nil {
		if !HasOwner(current.Snapshot.Protected.Owner, Owner) || !current.Snapshot.Protected.Disabled || !Proven(disable, current.Snapshot) {
			return errors.New("native disable proof no longer matches current credential")
		}
	}
	if family != nil {
		if current.Snapshot.Protected.Family == nil || current.Snapshot.Protected.Family.Owner != FamilyOwner || !Proven(family, current.Snapshot) {
			return errors.New("native family proof no longer matches current credential")
		}
	}
	return nil
}

func recordDigest(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return Digest(v)
}
