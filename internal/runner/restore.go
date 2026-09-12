package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func (plan *instrumentation) verifyInstalled() error {
	for _, file := range plan.files {
		if err := installedTestMatches(file.path, file.transformed); err != nil {
			return err
		}
	}
	for _, entry := range plan.packages {
		if err := installedTestMatches(entry.helperPath, []byte(plan.helperSource(entry))); err != nil {
			return err
		}
	}
	return nil
}

func installedTestMatches(path string, expected []byte) error {
	file, err := openRestoreFile(path)
	if err != nil {
		return err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
	if err != nil || !bytes.Equal(payload, expected) {
		return fmt.Errorf("runner test source changed")
	}
	return nil
}

// RISK(data-loss): never follow a test-replaced symlink, hardlink or parent alias
// while restoring. This check is not a sandbox against live detached attackers.
func openRestoreFile(path string) (*os.File, error) {
	if err := canonicalTestParent(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !safeRestoreInfo(info) {
		_ = file.Close()
		return nil, fmt.Errorf("runner restore target is unsafe")
	}
	return file, nil
}

func canonicalTestParent(path string) error {
	parent := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return fmt.Errorf("runner restore parent is unsafe")
	}
	return nil
}

func safeRestoreInfo(info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1 && stat.Uid == uint32(os.Geteuid())
}

func restoreTestFile(source instrumentedFile) error {
	file, err := openRestoreFile(source.path)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := unchangedRestoreContent(file, source); err != nil {
		return err
	}
	if _, err := file.WriteAt(source.original, 0); err != nil {
		return err
	}
	return file.Truncate(int64(len(source.original)))
}

func unchangedRestoreContent(file *os.File, source instrumentedFile) error {
	limit := int64(max(len(source.original), len(source.transformed))) + 1
	payload, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil || (!bytes.Equal(payload, source.original) && !bytes.Equal(payload, source.transformed)) {
		return fmt.Errorf("runner restore refuses unexpected edits")
	}
	return nil
}

func removeTestHelper(path string) error {
	if err := canonicalTestParent(path); err != nil {
		return err
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
