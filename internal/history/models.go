package history

import (
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/evidence"
)

const RunSchemaVersion = evidence.RunSchemaVersion

type Finding = evidence.Finding
type RunRecord = evidence.RunRecord
type Components = evidence.Components
type CrapComponent = evidence.CrapComponent
type MutationComponent = evidence.MutationComponent

// RepeatedDefect summarizes one project-keyed fingerprint across distinct runs.
type RepeatedDefect struct {
	Fingerprint      string    `json:"fingerprint"`
	Component        string    `json:"component"`
	Kind             string    `json:"kind"`
	ObservationCount int       `json:"observationCount"`
	FirstSeenUTC     time.Time `json:"firstSeenUtc"`
	LastSeenUTC      time.Time `json:"lastSeenUtc"`
	Repeated         bool      `json:"repeated"`
}
