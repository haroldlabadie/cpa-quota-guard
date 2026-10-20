//go:build !linux

package guard

import (
	"errors"
	"os"
)

func lockFile(path string) (*os.File, error) {
	return nil, errors.New("offline recovery requires Linux flock")
}
func unlockFile(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}
