// Package gotoolchain defines the closed environment used for every pinned Go
// child process started by the compiled SENTINEL command.
package gotoolchain

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const pinnedInstallName = "go-1.27.1"

// OfflineEnvironment derives only repository-local state paths from the
// already selected pinned Go executable. Ambient Go, proxy, credential, and
// workspace settings are intentionally not inherited.
func OfflineEnvironment(goBinary string) ([]string, error) {
	canonicalBinary, err := canonicalPath(goBinary)
	if err != nil {
		return nil, fmt.Errorf("pinned Go path: %w", err)
	}
	installRoot := filepath.Dir(filepath.Dir(canonicalBinary))
	if filepath.Base(installRoot) != pinnedInstallName || canonicalBinary != filepath.Join(installRoot, "bin", "go") {
		return nil, fmt.Errorf("pinned Go layout is invalid")
	}
	toolchainRoot := filepath.Dir(installRoot)
	stateRoot := filepath.Join(toolchainRoot, "state")
	directories := []string{
		stateRoot,
		filepath.Join(stateRoot, "home"),
		filepath.Join(stateRoot, "config"),
		filepath.Join(stateRoot, "tmp"),
		filepath.Join(stateRoot, "gopath"),
		filepath.Join(stateRoot, "build-cache"),
	}
	for _, directory := range directories {
		if err := requirePrivateDirectory(directory); err != nil {
			return nil, err
		}
	}
	if err := requireModuleCacheDirectory(filepath.Join(stateRoot, "module-cache")); err != nil {
		return nil, err
	}
	return []string{
		"LC_ALL=C",
		"TZ=UTC",
		"HOME=" + filepath.Join(stateRoot, "home"),
		"XDG_CONFIG_HOME=" + filepath.Join(stateRoot, "config"),
		"PATH=" + filepath.Join(installRoot, "bin") + ":/usr/bin:/bin",
		"TMPDIR=" + filepath.Join(stateRoot, "tmp"),
		"GOROOT=" + installRoot,
		"GOPATH=" + filepath.Join(stateRoot, "gopath"),
		"GOCACHE=" + filepath.Join(stateRoot, "build-cache"),
		"GOMODCACHE=" + filepath.Join(stateRoot, "module-cache"),
		"GOENV=off",
		"GOWORK=off",
		"GOFLAGS=",
		"GOPRIVATE=",
		"GONOPROXY=",
		"GONOSUMDB=",
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
		"GOVCS=*:off",
	}, nil
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil || canonical != filepath.Clean(absolute) {
		return "", fmt.Errorf("path is not canonical")
	}
	return canonical, nil
}

func requirePrivateDirectory(path string) error {
	if err := requireCanonicalDirectoryPath(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect toolchain state directory: %w", err)
	}
	return requirePrivateDirectoryMetadata(info)
}

func requireModuleCacheDirectory(path string) error {
	if err := requireCanonicalDirectoryPath(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect toolchain module cache: %w", err)
	}
	// RISK(packaging): an immutable dependency root is mode 0500 and mounted
	// read-only by the unified executor. Writable local caches remain mode 0700.
	if !info.IsDir() || (info.Mode().Perm() != 0o500 && info.Mode().Perm() != 0o700) {
		return fmt.Errorf("toolchain module cache is unsafe")
	}
	metadata, ok := info.Sys().(*syscall.Stat_t)
	if !ok || metadata.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("toolchain module cache owner is unsafe")
	}
	return nil
}

func requireCanonicalDirectoryPath(path string) error {
	canonical, err := canonicalPath(path)
	if err != nil {
		return fmt.Errorf("toolchain state directory is not canonical")
	}
	if canonical != path {
		return fmt.Errorf("toolchain state directory is not canonical")
	}
	return nil
}

func requirePrivateDirectoryMetadata(info os.FileInfo) error {
	if !privateDirectoryMetadata(info) {
		return fmt.Errorf("toolchain state directory is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("toolchain state directory owner is unsafe")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("toolchain state directory owner is unsafe")
	}
	return nil
}

func privateDirectoryMetadata(info os.FileInfo) bool {
	return info.IsDir() && info.Mode().Perm() == 0o700
}
