package guard

import (
	"encoding/json"
	"errors"
	"os"
)

// ReadCurrentFile verifies a private JSON file of sanitized current snapshots.
func ReadCurrentFile(path string) (map[string]Account, error) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 {
		return nil, errors.New("current credential file must be a private regular 0600 file")
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) > MaxFile {
		return nil, errors.New("current credential file unavailable")
	}
	var rows []CurrentCredential
	if err := StrictJSON(b, &rows); err != nil {
		return nil, err
	}
	out := map[string]Account{}
	for _, row := range rows {
		a, err := row.Account()
		if err != nil {
			return nil, err
		}
		out[row.Name] = a
	}
	return out, nil
}

// MarshalRepair keeps output free of credential data.
func MarshalRepair(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
