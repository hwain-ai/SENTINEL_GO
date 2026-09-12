package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

var excludedRoots = map[string]struct{}{
	".git":       {},
	".sentinel":  {},
	".toolchain": {},
	"target":     {},
}

type fileIdentity struct {
	mode   fs.FileMode
	size   int64
	device uint64
	inode  uint64
	digest string
}

// Snapshot is a disposable project copy plus the immutable original manifest
// captured from the same bytes.
type Snapshot struct {
	Root         string
	originalRoot string
	manifest     map[string]fileIdentity
}

// Create copies a regular-file-only project into an owner-only temporary root.
func Create(projectRoot string) (_ *Snapshot, returnedError error) {
	canonicalRoot, err := trustedProjectRoot(projectRoot)
	if err != nil {
		return nil, err
	}
	manifest, err := captureManifest(canonicalRoot)
	if err != nil {
		return nil, err
	}
	temporaryRoot, err := createSnapshotRoot()
	if err != nil {
		return nil, err
	}
	snapshot := &Snapshot{Root: temporaryRoot, originalRoot: canonicalRoot, manifest: manifest}
	defer func() {
		if returnedError != nil {
			_ = snapshot.Remove()
		}
	}()
	if err := copyManifestFiles(canonicalRoot, temporaryRoot, manifest); err != nil {
		return nil, err
	}
	if err := snapshot.VerifyOriginal(); err != nil {
		return nil, fmt.Errorf("project changed while snapshotting: %w", err)
	}
	return snapshot, nil
}

func createSnapshotRoot() (string, error) {
	root, err := os.MkdirTemp("", "sentinel-go-snapshot-")
	if err != nil {
		return "", fmt.Errorf("create snapshot root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.Remove(root)
		return "", fmt.Errorf("protect snapshot root: %w", err)
	}
	return root, nil
}

func trustedProjectRoot(projectRoot string) (string, error) {
	clean, err := canonicalProjectRoot(projectRoot)
	if err != nil {
		return "", err
	}
	if err := validateProjectRootOwner(clean); err != nil {
		return "", err
	}
	return clean, nil
}

func canonicalProjectRoot(projectRoot string) (string, error) {
	absolute, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	clean := filepath.Clean(absolute)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("resolve project root links: %w", err)
	}
	if resolved != clean {
		return "", fmt.Errorf("project root must not contain symbolic links")
	}
	return clean, nil
}

func validateProjectRootOwner(clean string) error {
	info, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("inspect project root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("project root is not a directory")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("project root owner is not trusted")
	}
	return nil
}

func captureManifest(root string) (map[string]fileIdentity, error) {
	capture := &manifestCapture{root: root, files: make(map[string]fileIdentity)}
	err := filepath.WalkDir(root, capture.visit)
	return capture.files, err
}

type manifestCapture struct {
	root  string
	files map[string]fileIdentity
}

func (capture *manifestCapture) visit(path string, entry fs.DirEntry, walkError error) error {
	if walkError != nil {
		return walkError
	}
	relative, err := filepath.Rel(capture.root, path)
	if err != nil || relative == "." {
		return err
	}
	if excludedPath(relative) {
		return excludedEntryDecision(entry)
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("project contains symbolic link %q", relative)
	}
	if entry.IsDir() {
		return nil
	}
	return capture.addFile(path, relative)
}

func excludedEntryDecision(entry fs.DirEntry) error {
	if entry.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

func (capture *manifestCapture) addFile(path, relative string) error {
	identity, err := identifyRegularFile(path)
	if err != nil {
		return fmt.Errorf("inspect %q: %w", relative, err)
	}
	capture.files[filepath.ToSlash(relative)] = identity
	return nil
}

func excludedPath(relative string) bool {
	first := strings.SplitN(filepath.ToSlash(relative), "/", 2)[0]
	_, excluded := excludedRoots[first]
	return excluded
}

func identifyRegularFile(path string) (fileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileIdentity{}, err
	}
	if !info.Mode().IsRegular() {
		return fileIdentity{}, fmt.Errorf("unsupported file type")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		return fileIdentity{}, fmt.Errorf("file owner or link count is unsafe")
	}
	digest, err := digestFile(path)
	if err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{
		mode:   info.Mode().Perm(),
		size:   info.Size(),
		device: uint64(stat.Dev),
		inode:  stat.Ino,
		digest: digest,
	}, nil
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func copyManifestFiles(sourceRoot, destinationRoot string, manifest map[string]fileIdentity) error {
	paths := make([]string, 0, len(manifest))
	for relative := range manifest {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		if err := copyOneFile(sourceRoot, destinationRoot, relative, manifest[relative]); err != nil {
			return err
		}
	}
	return nil
}

func copyOneFile(sourceRoot, destinationRoot, relative string, expected fileIdentity) error {
	source := filepath.Join(sourceRoot, filepath.FromSlash(relative))
	destination := filepath.Join(destinationRoot, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open snapshot source: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, expected.mode)
	if err != nil {
		return fmt.Errorf("create snapshot file: %w", err)
	}
	if err := copyAndSync(output, input); err != nil {
		return err
	}
	return verifySnapshotCopy(destination, relative, expected)
}

func copyAndSync(output *os.File, input io.Reader) error {
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return fmt.Errorf("copy snapshot file: %w", err)
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return fmt.Errorf("sync snapshot file: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close snapshot file: %w", err)
	}
	return nil
}

func verifySnapshotCopy(destination, relative string, expected fileIdentity) error {
	actual, err := identifyRegularFile(destination)
	if err != nil || actual.digest != expected.digest || actual.size != expected.size {
		return fmt.Errorf("snapshot copy mismatch for %q", relative)
	}
	return nil
}

// VerifyOriginal fails when any captured project file changed, disappeared, or
// was replaced after the snapshot was created.
func (snapshot *Snapshot) VerifyOriginal() error {
	current, err := captureManifest(snapshot.originalRoot)
	if err != nil {
		return err
	}
	if len(current) != len(snapshot.manifest) {
		return fmt.Errorf("project file set changed")
	}
	for path, expected := range snapshot.manifest {
		actual, exists := current[path]
		if !exists || actual != expected {
			return fmt.Errorf("project file changed: %s", path)
		}
	}
	return nil
}

// Remove deletes only the unique temporary directory created by Create.
func (snapshot *Snapshot) Remove() error {
	if snapshot == nil || snapshot.Root == "" {
		return nil
	}
	root := filepath.Clean(snapshot.Root)
	prefix := filepath.Join(os.TempDir(), "sentinel-go-snapshot-")
	if !strings.HasPrefix(root, prefix) || filepath.Dir(root) != filepath.Clean(os.TempDir()) {
		return fmt.Errorf("refusing to remove unexpected snapshot root")
	}
	err := os.RemoveAll(root)
	if err == nil {
		snapshot.Root = ""
	}
	return err
}
