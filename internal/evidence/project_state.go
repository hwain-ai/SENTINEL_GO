package evidence

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const maxSafeInteger = uint64(9007199254740991)

type projectKeys struct {
	projectIdentifier []byte
	fingerprintKey    []byte
	cleanupLeaseKey   []byte
	keyEpoch          uint64
}

func (keys *projectKeys) clear() {
	clear(keys.projectIdentifier)
	clear(keys.fingerprintKey)
	clear(keys.cleanupLeaseKey)
}

// Store owns one project's private state-v1 directory.
type Store struct {
	projectRoot string
}

func NewStore(projectRoot string) *Store {
	return &Store{projectRoot: filepath.Clean(projectRoot)}
}

func (store *Store) Root() string {
	return filepath.Join(store.projectRoot, ".sentinel", stateVersion)
}

// RISK(security): project.json에는 외부로 내보내면 안 되는 프로젝트 전용 비밀 key가 들어 있다.
// Initialize creates the private project root of trust exactly once.
func (store *Store) Initialize() error {
	if err := store.ensureStateRoots(); err != nil {
		return err
	}
	return withProcessLock(store.lockPath(), lockExclusive, store.initializeLocked)
}

func (store *Store) initializeLocked() error {
	path := filepath.Join(store.Root(), "project.json")
	if _, err := os.Lstat(path); err == nil {
		return validateExistingProject(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect project state: %w", err)
	}
	state, err := newProjectState()
	if err != nil {
		return err
	}
	payload, err := canonicalJSON(state)
	if err != nil {
		return err
	}
	if err := publishNoReplace(path, payload); err != nil {
		return err
	}
	return syncDirectory(store.Root())
}

func validateExistingProject(path string) error {
	keys, err := readProjectKeys(path)
	if err == nil {
		keys.clear()
	}
	return err
}

func (store *Store) ensureStateRoots() error {
	for _, directory := range []string{
		filepath.Join(store.projectRoot, ".sentinel"),
		store.Root(),
		filepath.Join(store.Root(), "runs"),
	} {
		if err := ensureOwnerDirectory(directory); err != nil {
			return err
		}
	}
	lock, err := openLockFile(store.lockPath(), true)
	if err != nil {
		return err
	}
	if err := lock.Close(); err != nil {
		return fmt.Errorf("close commit lock: %w", err)
	}
	return syncDirectory(store.Root())
}

func (store *Store) lockPath() string {
	return filepath.Join(store.Root(), "commit.lock")
}

func newProjectState() (projectState, error) {
	identifier, err := entropyBytes(16)
	if err != nil {
		return projectState{}, err
	}
	defer clear(identifier)
	fingerprintKey, err := entropyBytes(32)
	if err != nil {
		return projectState{}, err
	}
	defer clear(fingerprintKey)
	cleanupKey, err := entropyBytes(32)
	if err != nil {
		return projectState{}, err
	}
	defer clear(cleanupKey)
	if bytes.Equal(fingerprintKey, cleanupKey) {
		return projectState{}, fmt.Errorf("CSPRNG returned duplicate project keys")
	}
	return projectState{
		CleanupLeaseKey:    base64.RawURLEncoding.EncodeToString(cleanupKey),
		FingerprintHMACKey: base64.RawURLEncoding.EncodeToString(fingerprintKey),
		KeyEpoch:           1,
		ProjectIdentifier:  base64.RawURLEncoding.EncodeToString(identifier),
		SchemaVersion:      projectSchemaVersion,
		StateVersion:       stateVersion,
	}, nil
}

func entropyBytes(size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		clear(value)
		return nil, fmt.Errorf("read project entropy: %w", err)
	}
	return value, nil
}

func readProjectKeys(path string) (projectKeys, error) {
	payload, err := readOwnerFile(path)
	if err != nil {
		return projectKeys{}, err
	}
	var state projectState
	if err := decodeCanonical(payload, &state); err != nil {
		return projectKeys{}, fmt.Errorf("invalid project state: %w", err)
	}
	if !validProjectMetadata(state) {
		return projectKeys{}, fmt.Errorf("invalid project state metadata")
	}
	return decodeProjectKeys(state)
}

func validProjectMetadata(state projectState) bool {
	return state.SchemaVersion == projectSchemaVersion && state.StateVersion == stateVersion && state.KeyEpoch > 0 && state.KeyEpoch <= maxSafeInteger
}

func decodeProjectKeys(state projectState) (projectKeys, error) {
	keys := projectKeys{keyEpoch: state.KeyEpoch}
	identifier, err := decodeExactBase64(state.ProjectIdentifier, 16)
	if err != nil {
		return projectKeys{}, fmt.Errorf("invalid project identifier")
	}
	keys.projectIdentifier = identifier
	fingerprintKey, err := decodeExactBase64(state.FingerprintHMACKey, 32)
	if err != nil {
		return invalidProjectKeys(keys, "invalid fingerprint key")
	}
	keys.fingerprintKey = fingerprintKey
	cleanupKey, err := decodeExactBase64(state.CleanupLeaseKey, 32)
	if err != nil {
		return invalidProjectKeys(keys, "invalid cleanup key")
	}
	keys.cleanupLeaseKey = cleanupKey
	if bytes.Equal(keys.fingerprintKey, keys.cleanupLeaseKey) {
		return invalidProjectKeys(keys, "duplicate project keys")
	}
	return keys, nil
}

func invalidProjectKeys(keys projectKeys, message string) (projectKeys, error) {
	keys.clear()
	return projectKeys{}, fmt.Errorf("%s", message)
}

func decodeExactBase64(value string, size int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != size || base64.RawURLEncoding.EncodeToString(decoded) != value {
		clear(decoded)
		return nil, fmt.Errorf("invalid base64url value")
	}
	return decoded, nil
}
