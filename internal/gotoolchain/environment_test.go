package gotoolchain

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOfflineEnvironmentIgnoresAmbientGoAndProxySettings(t *testing.T) {
	t.Setenv("HOME", "/attacker/home")
	t.Setenv("GOROOT", "/attacker/go")
	t.Setenv("GOPROXY", "https://attacker.invalid")
	t.Setenv("GOFLAGS", "-mod=mod")
	goBinary := fixtureToolchain(t)

	got, err := OfflineEnvironment(goBinary)
	if err != nil {
		t.Fatalf("OfflineEnvironment() error = %v", err)
	}
	toolchainRoot := filepath.Dir(filepath.Dir(filepath.Dir(goBinary)))
	installRoot := filepath.Dir(filepath.Dir(goBinary))
	stateRoot := filepath.Join(toolchainRoot, "state")
	want := []string{
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
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestOfflineEnvironmentRejectsMissingOrUnsafeState(t *testing.T) {
	goBinary := fixtureToolchain(t)
	stateRoot := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(goBinary))), "state")
	if err := os.Chmod(filepath.Join(stateRoot, "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OfflineEnvironment(goBinary); err == nil {
		t.Fatal("unsafe state directory was accepted")
	}
}

func TestOfflineEnvironmentAcceptsReadOnlyPrivateModuleCache(t *testing.T) {
	goBinary := fixtureToolchain(t)
	stateRoot := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(goBinary))), "state")
	cache := filepath.Join(stateRoot, "module-cache")
	if err := os.Chmod(cache, 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := OfflineEnvironment(goBinary); err != nil {
		t.Fatalf("read-only private module cache was rejected: %v", err)
	}
	if err := os.Chmod(filepath.Join(stateRoot, "build-cache"), 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := OfflineEnvironment(goBinary); err == nil {
		t.Fatal("read-only writable-state directory was accepted")
	}
}

func TestOfflineEnvironmentRejectsExposedModuleCache(t *testing.T) {
	for _, mode := range []os.FileMode{0o550, 0o555, 0o755, 0o702} {
		t.Run(mode.String(), func(t *testing.T) {
			goBinary := fixtureToolchain(t)
			stateRoot := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(goBinary))), "state")
			if err := os.Chmod(filepath.Join(stateRoot, "module-cache"), mode); err != nil {
				t.Fatal(err)
			}
			if _, err := OfflineEnvironment(goBinary); err == nil {
				t.Fatal("exposed module cache was accepted")
			}
		})
	}
}

func fixtureToolchain(t *testing.T) string {
	t.Helper()
	toolchainRoot := filepath.Join(t.TempDir(), ".toolchain")
	installRoot := filepath.Join(toolchainRoot, "go-1.27.1")
	goBinary := filepath.Join(installRoot, "bin", "go")
	if err := os.MkdirAll(filepath.Dir(goBinary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goBinary, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home", "config", "tmp", "gopath", "build-cache", "module-cache"} {
		if err := os.MkdirAll(filepath.Join(toolchainRoot, "state", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return goBinary
}
