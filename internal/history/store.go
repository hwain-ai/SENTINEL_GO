package history

import "github.com/hwain-hwang/sentinel-go/internal/evidence"

// Store is the evidence-backed project-local history store.
type Store = evidence.Store

func NewStore(projectRoot string) *Store {
	return evidence.NewStore(projectRoot)
}
