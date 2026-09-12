package workspace

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"sort"
)

// ContentSHA256 binds copied relative paths and contents, not temporary locations
// or inode numbers. The existing guard separately verifies identity and modes.
func (snapshot *Snapshot) ContentSHA256() string {
	paths := make([]string, 0, len(snapshot.manifest))
	for path := range snapshot.manifest {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	hash := sha256.New()
	_, _ = io.WriteString(hash, "sentinel-project-content-v1\x00")
	for _, path := range paths {
		_ = binary.Write(hash, binary.BigEndian, uint64(len(path)))
		_, _ = io.WriteString(hash, path)
		_, _ = io.WriteString(hash, snapshot.manifest[path].digest)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
