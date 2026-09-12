package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreCannotOverwriteLinkedOrReplacedParentTargets(t *testing.T) {
	for _, kind := range []string{"symbolic", "hard", "parent"} {
		t.Run(kind, func(t *testing.T) {
			outside := filepath.Join(t.TempDir(), "value_test.go")
			if err := os.WriteFile(outside, []byte("private canary"), 0o600); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			path := filepath.Join(root, "value_test.go")
			switch kind {
			case "symbolic":
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "hard":
				if err := os.Link(outside, path); err != nil {
					t.Fatal(err)
				}
			case "parent":
				parent := filepath.Join(root, "sub")
				if err := os.Symlink(filepath.Dir(outside), parent); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(parent, "value_test.go")
			}
			plan := &instrumentation{files: []instrumentedFile{{path: path, original: []byte("restore"), mode: 0o600}}}
			if err := plan.restore(); err == nil {
				t.Error("unsafe restoration accepted")
			}
			after, err := os.ReadFile(outside)
			if err != nil || string(after) != "private canary" {
				t.Fatal("restoration overwrote a different file")
			}
		})
	}
}

func TestRestorePreservesUnexpectedRegularFileEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value_test.go")
	if err := os.WriteFile(path, []byte("concurrent edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := instrumentedFile{path: path, original: []byte("original"), transformed: []byte("installed"), mode: 0o600}
	if err := restoreTestFile(source); err == nil {
		t.Error("unexpected edit accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != "concurrent edit" {
		t.Fatal("unexpected edit overwritten")
	}
}
