package runner

import (
	"os"
	"testing"
)

func TestCreateEventFileUsesOwnerOnlyNamedPipe(t *testing.T) {
	path, cleanup, err := createEventFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("event channel mode = %v, want named pipe", info.Mode())
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("event channel permissions = %o", info.Mode().Perm())
	}
}
