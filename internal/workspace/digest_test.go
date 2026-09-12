package workspace

import (
	"path/filepath"
	"testing"
)

func TestSnapshotContentDigestIsLocationIndependentAndBindsEveryCopiedFile(t *testing.T) {
	root := t.TempDir()
	writeSnapshotTestFile(t, filepath.Join(root, "go.mod"), "module p\n")
	writeSnapshotTestFile(t, filepath.Join(root, "value.go"), "package p\n")
	writeSnapshotTestFile(t, filepath.Join(root, "value_test.go"), "package p\n")
	first, err := Create(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Remove()
	copy, err := Create(first.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Remove()
	if len(first.ContentSHA256()) != 64 || first.ContentSHA256() != copy.ContentSHA256() {
		t.Fatal("copy changes project content identity")
	}
	for _, file := range []string{"go.mod", "value.go", "value_test.go", "data/input.txt"} {
		one, err := Create(first.Root)
		if err != nil {
			t.Fatal(err)
		}
		defer one.Remove()
		writeSnapshotTestFile(t, filepath.Join(one.Root, file), "changed")
		changed, err := Create(one.Root)
		if err != nil {
			t.Fatal(err)
		}
		if changed.ContentSHA256() == first.ContentSHA256() {
			t.Fatalf("file not bound: %s", file)
		}
		if err := changed.Remove(); err != nil {
			t.Fatal(err)
		}
	}
}
