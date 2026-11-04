package models

import (
	"path"
	"strings"
	"time"
)

// Artifact is a file a job's build produced.
type Artifact struct {
	ID    int64  `json:"-"`
	JobID int64  `json:"job_id"`
	Name  string `json:"name"` // its path relative to the workspace, e.g. dist/app.tar.gz
	Size  int64  `json:"size"`
	// SHA256 is the hex digest of the content.
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
}

// MaxArtifactName bounds an artifact's name.
const MaxArtifactName = 255

// ValidArtifactName reports whether name is a clean relative path that
// stays inside the workspace.
func ValidArtifactName(name string) bool {
	return name != "" && len(name) <= MaxArtifactName && !path.IsAbs(name) && path.Clean(name) == name &&
		name != "." && name != ".." && !strings.HasPrefix(name, "../") &&
		!strings.ContainsFunc(name, func(r rune) bool { return r < ' ' || r == 0x7f || r == '\\' })
}
