package crap

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

func validateModulePath(modulePath string) error {
	switch {
	case modulePath == "":
		return fmt.Errorf("module path must not be empty")
	case !validPathText(modulePath):
		return fmt.Errorf("module path must be valid UTF-8 without NUL")
	case strings.Contains(modulePath, `\`):
		return fmt.Errorf("module path must use POSIX separators")
	case !isCanonicalProjectPath(modulePath):
		return fmt.Errorf("module path must be canonical and project-relative")
	}
	return nil
}

func validPathText(modulePath string) bool {
	return utf8.ValidString(modulePath) && !strings.ContainsRune(modulePath, '\x00')
}

func isCanonicalProjectPath(modulePath string) bool {
	if path.IsAbs(modulePath) || modulePath == "." || path.Clean(modulePath) != modulePath {
		return false
	}
	if hasForbiddenPathSegment(modulePath) {
		return false
	}
	return true
}

func hasForbiddenPathSegment(modulePath string) bool {
	for _, segment := range strings.Split(modulePath, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return true
		}
	}
	return false
}
