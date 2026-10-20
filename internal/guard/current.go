package guard

import (
	"errors"
	"path/filepath"
)

// CurrentCredential is a sanitized view supplied by the offline operator after
// reading a credential through the existing helper. No raw OAuth value is
// stored in or emitted from this package.
type CurrentCredential struct {
	Name     string   `json:"name"`
	Identity string   `json:"identity"`
	Snapshot Snapshot `json:"snapshot"`
}

// Account builds the sanitized current record used by PlanOrphanRepair.
func (c CurrentCredential) Account() (Account, error) {
	if c.Name == "" || filepath.Base(c.Name) != c.Name || c.Identity == "" || c.Snapshot.Identity != c.Identity || c.Snapshot.ProtectedHash != Digest(c.Snapshot.Protected) || c.Snapshot.MtimeNS <= 0 {
		return Account{}, errors.New("invalid current credential record")
	}
	return Account{Entry: Entry{Name: c.Name}, Snapshot: c.Snapshot}, nil
}
