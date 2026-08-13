package model

import "time"

// Kind describes a scanned filesystem object.
type Kind string

const (
	KindFile      Kind = "file"
	KindDirectory Kind = "directory"
)

// Class is a derived lifecycle or size classification.
type Class string

const (
	ClassOrphan    Class = "orphan"
	ClassSeedling  Class = "seedling"
	ClassDormant   Class = "dormant"
	ClassActive    Class = "active"
	ClassGiant     Class = "giant"
	ClassRotten    Class = "rotten"
	ClassGitZombie Class = "git_zombie"
)

// Entry is the internal, privileged representation of a scanned object.
type Entry struct {
	ID         string
	RootID     string
	Path       string
	Relative   string
	Name       string
	Extension  string
	Kind       Kind
	Size       int64
	ModTime    time.Time
	SHA256     string
	Classes    []Class
	GitProject bool
	Error      string
}

// Relation links entries without imposing filesystem-specific semantics.
type Relation struct {
	FromID string
	ToID   string
	Type   string
}

const (
	RelationDuplicate  = "duplicate"
	RelationZIPExtract = "zip_extract"
)

const (
	ScanStatusComplete  = "complete"
	ScanStatusPartial   = "partial"
	ScanStatusCancelled = "cancelled"
)

// Scan contains one immutable scan result.
type Scan struct {
	ID               string
	StartedAt        time.Time
	EndedAt          time.Time
	Status           string
	Partial          bool
	Truncated        bool
	TruncationReason string
	Roots            []string
	Entries          []Entry
	Relations        []Relation
	Errors           []string
	ErrorCount       int
}

// MapCell is an aggregate suitable for filesystem-map rendering.
type MapCell struct {
	RootID     string
	Extension  string
	Class      Class
	Count      int64
	TotalBytes int64
}

// AuditEvent records a security- or policy-relevant event.
type AuditEvent struct {
	ID      int64
	ScanID  string
	At      time.Time
	Level   string
	Code    string
	Message string
	EntryID string
}

// Suggestion is an actionable, persisted observation.
type Suggestion struct {
	ID        int64
	ScanID    string
	CreatedAt time.Time
	Kind      string
	EntryID   string
	Summary   string
	Details   string
	Resolved  bool
}
