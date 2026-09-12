package evidence

import (
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"
)

type fingerprintInput struct {
	Component        string `json:"component"`
	Kind             string `json:"kind"`
	SemanticIdentity string `json:"semanticIdentity"`
	Version          string `json:"version"`
}

// Fingerprint returns a project-specific HMAC without persisting raw identity.
func (store *Store) Fingerprint(component, kind, semanticIdentity string) (string, error) {
	if component == "" || kind == "" || semanticIdentity == "" ||
		!utf8.ValidString(component) || !utf8.ValidString(kind) || !utf8.ValidString(semanticIdentity) {
		return "", fmt.Errorf("invalid fingerprint input")
	}
	var fingerprint string
	err := store.withExistingState(lockShared, func(keys projectKeys) error {
		body, err := canonicalBody(fingerprintInput{
			Component:        component,
			Kind:             kind,
			SemanticIdentity: semanticIdentity,
			Version:          "sentinel-fingerprint-json-v1",
		})
		if err != nil {
			return err
		}
		fingerprint = namespacedHMAC(keys.fingerprintKey, "SENTINEL\x00finding-occurrence\x00v1\x00", body)
		return nil
	})
	return fingerprint, err
}

func (store *Store) withExistingState(mode lockMode, action func(projectKeys) error) error {
	if err := validateStateParents(store); err != nil {
		return err
	}
	if err := validateOwnerDirectory(store.Root()); err != nil {
		return err
	}
	return withProcessLock(store.lockPath(), mode, func() error {
		keys, err := readProjectKeys(store.projectPath())
		if err != nil {
			return err
		}
		defer keys.clear()
		return action(keys)
	})
}

func validateStateParents(store *Store) error {
	projectInfo, err := os.Lstat(store.projectRoot)
	if err != nil || !projectInfo.IsDir() || projectInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("project root is not a real directory")
	}
	return validateOwnerDirectory(filepath.Join(store.projectRoot, ".sentinel"))
}

func (store *Store) projectPath() string {
	return filepath.Join(store.Root(), "project.json")
}
