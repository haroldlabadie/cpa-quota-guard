//go:build !linux

package main

import (
	"errors"
	"os"
)

func lockFile(path string) (*os.File, error) {
	return nil, errors.New("production plugin requires Linux flock")
}
func unlockFile(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}
