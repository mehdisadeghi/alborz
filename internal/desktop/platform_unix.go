//go:build !windows

package desktop

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"runtime"
)

func DefaultDataDir() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("desktop launcher requires Windows or macOS")
	}
	dir, err := os.UserConfigDir()
	return filepath.Join(dir, "Alborz"), err
}

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}
func acquireLock(path string) (*os.File, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return file, true, nil
}
func releaseLock(file *os.File) { unix.Flock(int(file.Fd()), unix.LOCK_UN); file.Close() }
