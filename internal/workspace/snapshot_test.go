package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotCopiesRegularProjectAndVerifiesOriginal(t *testing.T) {
	project := t.TempDir()
	writeSnapshotTestFile(t, filepath.Join(project, "go.mod"), "module example.test/sample\n\ngo 1.27.0\n")
	writeSnapshotTestFile(t, filepath.Join(project, "value.go"), "package sample\nfunc Value() int { return 1 }\n")
	writeSnapshotTestFile(t, filepath.Join(project, ".git", "ignored"), "private")

	snapshot, err := Create(project)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Remove()

	if _, err := os.Stat(filepath.Join(snapshot.Root, "value.go")); err != nil {
		t.Fatalf("snapshot source missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snapshot.Root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("snapshot copied .git: %v", err)
	}
	if err := snapshot.VerifyOriginal(); err != nil {
		t.Fatalf("unchanged original rejected: %v", err)
	}

	writeSnapshotTestFile(t, filepath.Join(project, "value.go"), "package sample\nfunc Value() int { return 2 }\n")
	if err := snapshot.VerifyOriginal(); err == nil {
		t.Fatal("modified original was accepted")
	}
}

func TestSnapshotRejectsSymlinkAndHardlink(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{
			name: "symlink",
			setup: func(t *testing.T, root string) {
				writeSnapshotTestFile(t, filepath.Join(root, "real.go"), "package p\n")
				if err := os.Symlink("real.go", filepath.Join(root, "link.go")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hardlink",
			setup: func(t *testing.T, root string) {
				path := filepath.Join(root, "real.go")
				writeSnapshotTestFile(t, path, "package p\n")
				if err := os.Link(path, filepath.Join(root, "hard.go")); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			project := t.TempDir()
			testCase.setup(t, project)
			if snapshot, err := Create(project); err == nil {
				snapshot.Remove()
				t.Fatal("unsafe project was accepted")
			}
		})
	}
}

func writeSnapshotTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
