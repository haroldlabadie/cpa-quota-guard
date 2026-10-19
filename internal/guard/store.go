package guard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const MaxFile = 8 << 20

func StrictJSON(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > MaxFile {
		return errors.New("JSON size invalid")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(target); err != nil {
		return errors.New("invalid JSON document")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}
func ValidateSettings(s Settings) error {
	if s.Schema != 1 || s.Rules == nil || len(s.Rules) > 1000 {
		return errors.New("unsupported settings schema/rules")
	}
	seen := map[string]bool{}
	for _, r := range s.Rules {
		email := strings.TrimSpace(strings.ToLower(r.Email))
		if email == "" || !strings.Contains(email, "@") || email != r.Email || len(email) > 320 {
			return errors.New("invalid normalized email")
		}
		if r.Provider != "codex" && r.Provider != "antigravity" {
			return errors.New("invalid provider")
		}
		if r.ReserveFiveHour < 0 || r.ReserveFiveHour >= 100 || r.ReserveWeekly < 0 || r.ReserveWeekly >= 100 {
			return errors.New("reserve must be within [0,100)")
		}
		key := email + "/" + r.Provider
		if seen[key] {
			return errors.New("duplicate account rule")
		}
		seen[key] = true
	}
	return nil
}
func AtomicJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errors.New("encode state failed")
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing symlink state")
	}
	f, err := os.CreateTemp(dir, ".guard-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func LoadState(path string) (State, error) {
	s := NewState()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return ParseState(b)
}
func ParseState(b []byte) (State, error) {
	var s State
	if err := StrictJSON(b, &s); err != nil {
		return s, err
	}
	if s.Schema != 1 || s.Plugin != "cpa-quota-guard" || s.Legacy.Version != 2 || s.Legacy.Accounts == nil || s.Ownership == nil || s.Observations == nil {
		return s, errors.New("unsupported/corrupt guard state")
	}
	if s.Initialization != nil && (s.Initialization.Kind != "fresh-install" || s.Initialization.CreatedMS <= 0) {
		return s, errors.New("invalid initialization marker")
	}
	for name, o := range s.Ownership {
		if name == "" || o == nil {
			return s, errors.New("invalid ownership")
		}
		for _, p := range []*Snapshot{o.Disable, o.Family} {
			if p != nil && (p.Identity == "" || p.MtimeNS <= 0 || p.ProtectedHash != Digest(p.Protected)) {
				return s, fmt.Errorf("invalid ownership proof")
			}
		}
	}
	return s, nil
}
