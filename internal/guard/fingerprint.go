package guard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

func Digest(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

var tokenFields = map[string]bool{"access_token": true, "refresh_token": true, "id_token": true, "last_refresh": true, "expired": true, "expiry": true, "expires_at": true, "expires_in": true, "token": true, "timestamp": true}
var protectedFields = map[string]bool{"disabled": true, "quota_guard_owner": true, "quota_guard_reason": true, "quota_guard_family_control": true, "excluded_models": true, "excluded-models": true, "quota_guard_mutation_id": true}

func Exclusions(v any) ([]string, error) {
	out := []string{}
	if v == nil {
		return out, nil
	}
	rows, ok := v.([]any)
	if !ok {
		return nil, errors.New("invalid excluded_models")
	}
	seen := map[string]bool{}
	for _, row := range rows {
		s, ok := row.(string)
		if !ok {
			return nil, errors.New("invalid excluded_models entry")
		}
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}
func optionalString(raw map[string]any, key string) (*string, error) {
	v := raw[key]
	if v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("invalid %s", key)
	}
	return &s, nil
}
func MakeAccount(entry Entry, raw map[string]any, mtime int64) (Account, error) {
	a := Account{Entry: entry}
	var err error
	a.AccountID, _ = raw["account_id"].(string)
	a.ProjectID, _ = raw["project_id"].(string)
	if entry.Email == "" {
		entry.Email, _ = raw["email"].(string)
		a.Entry = entry
	}
	if provider, ok := raw["type"].(string); ok && provider != "" && provider != entry.Provider {
		return a, errors.New("credential provider mismatch")
	}
	p := Protected{}
	if v, exists := raw["disabled"]; exists {
		var ok bool
		p.Disabled, ok = v.(bool)
		if !ok {
			return a, errors.New("invalid disabled")
		}
	}
	if p.Owner, err = optionalString(raw, "quota_guard_owner"); err != nil {
		return a, err
	}
	if p.Reason, err = optionalString(raw, "quota_guard_reason"); err != nil {
		return a, err
	}
	if p.MutationID, err = optionalString(raw, "quota_guard_mutation_id"); err != nil {
		return a, err
	}
	if p.MutationID != nil && (len(*p.MutationID) == 0 || len(*p.MutationID) > 128) {
		return a, errors.New("invalid mutation id")
	}
	excluded := raw["excluded_models"]
	if excluded == nil {
		excluded = raw["excluded-models"]
	}
	if p.Excluded, err = Exclusions(excluded); err != nil {
		return a, err
	}
	if v := raw["quota_guard_family_control"]; v != nil {
		b, _ := json.Marshal(v)
		var c FamilyControl
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return a, errors.New("invalid family control")
		}
		if c.Owner == "" || c.Base == nil || c.Blocked == nil {
			return a, errors.New("incomplete family control")
		}
		for _, scope := range c.Blocked {
			if _, ok := Patterns[scope]; !ok {
				return a, errors.New("unknown controlled family")
			}
		}
		c.Base = Normalize(c.Base)
		c.Blocked = Normalize(c.Blocked)
		p.Family = &c
	}
	tokens, other := map[string]any{}, map[string]any{}
	for k, v := range raw {
		if tokenFields[k] {
			tokens[k] = v
		} else if !protectedFields[k] {
			other[k] = v
		}
	}
	a.Snapshot = Snapshot{Identity: Digest([]string{entry.Name, entry.Provider, entry.AuthIndex, strings.ToLower(strings.TrimSpace(entry.Email)), a.AccountID, a.ProjectID}), Protected: p, ProtectedHash: Digest(p), TokenHash: Digest(tokens), OtherHash: Digest(other), MtimeNS: mtime}
	return a, nil
}
func Normalize(rows []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range rows {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	sort.Strings(out)
	return out
}
func HasOwner(s *string, want string) bool { return s != nil && *s == want }

func ProtectedEqual(a, b Protected) bool {
	return reflect.DeepEqual(a, b)
}

// Proven rejects protected/identity changes and same-content rewrites. Token-only
// refresh and unrelated metadata edits retain ownership. CPA has no field CAS;
// simultaneous manual writes during refresh remain an explicit host limitation.

func Proven(old *Snapshot, current Snapshot) bool {
	if old == nil || old.Identity != current.Identity || old.ProtectedHash != current.ProtectedHash || Digest(old.Protected) != old.ProtectedHash {
		return false
	}
	if old.MtimeNS == current.MtimeNS {
		return old.TokenHash == current.TokenHash && old.OtherHash == current.OtherHash
	}
	return old.TokenHash != current.TokenHash || old.OtherHash != current.OtherHash
}
