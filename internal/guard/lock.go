package guard

import "os"

// LockPath acquires the same nonblocking flock used by the plugin worker.
func LockPath(path string) (*os.File, error) { return lockFile(path) }

// UnlockPath releases a lock acquired by LockPath.
func UnlockPath(f *os.File) { unlockFile(f) }
