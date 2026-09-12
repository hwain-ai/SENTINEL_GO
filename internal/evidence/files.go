package evidence

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func ensureOwnerDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create state directory: %w", err)
	}
	return validateOwnerDirectory(path)
}

func validateOwnerDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect state directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("state directory is not owner-only: %s", path)
	}
	return nil
}

func createOwnerFile(path string, payload []byte) error {
	file, err := openOwnerFile(path, syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := writeSyncClose(file, payload); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func readOwnerFile(path string) ([]byte, error) {
	file, err := openOwnerFile(path, syscall.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read owner file: %w", err)
	}
	if len(payload) >= 1<<20 {
		return nil, fmt.Errorf("owner file exceeds size limit")
	}
	return payload, nil
}

func openOwnerFile(path string, flags int, mode uint32) (*os.File, error) {
	descriptor, err := syscall.Open(path, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return nil, fmt.Errorf("open owner file: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), path)
	if err := validateOwnerFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validateOwnerFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect owner file: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("state file is not owner-only: %s", file.Name())
	}
	return nil
}

func writeSyncClose(file *os.File, payload []byte) error {
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return fmt.Errorf("write state file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync state file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close state file: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open state directory: %w", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}

// RISK(data-loss): sequence high-water 교체는 fsync와 같은 directory의 rename 순서를 바꾸면 안 된다.
func atomicReplace(path string, payload []byte) error {
	directory := filepath.Dir(path)
	temporary := filepath.Join(directory, "."+filepath.Base(path)+".tmp")
	if err := createOwnerFile(temporary, payload); err != nil {
		return fmt.Errorf("create replacement: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace state file: %w", err)
	}
	return syncDirectory(directory)
}

func publishNoReplace(path string, payload []byte) error {
	directory := filepath.Dir(path)
	temporary := filepath.Join(directory, "."+filepath.Base(path)+".tmp")
	if err := createOwnerFile(temporary, payload); err != nil {
		return fmt.Errorf("create publish file: %w", err)
	}
	if err := os.Link(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish immutable file: %w", err)
	}
	if err := os.Remove(temporary); err != nil {
		return fmt.Errorf("remove publish link: %w", err)
	}
	return syncDirectory(directory)
}
