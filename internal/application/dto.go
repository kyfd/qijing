// Package application provides desktop- and transport-independent application services.
package application

import (
	"github.com/kyfd/qijing/internal/agent"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/privacy"
)

// RootDTO is one explicitly authorized filesystem root.
type RootDTO struct {
	Path string `json:"path"`
}

type RootsDTO struct {
	Roots []RootDTO `json:"roots"`
}

type BatchRootsRequestDTO struct {
	Paths     []string `json:"paths"`
	StartScan bool     `json:"start_scan"`
}

type RootAuthorizationStatus string

const (
	RootAuthorizationValidated         RootAuthorizationStatus = "validated"
	RootAuthorizationAuthorized        RootAuthorizationStatus = "authorized"
	RootAuthorizationAlreadyAuthorized RootAuthorizationStatus = "already_authorized"
	RootAuthorizationDuplicate         RootAuthorizationStatus = "duplicate"
	RootAuthorizationCovered           RootAuthorizationStatus = "covered_by_parent"
	RootAuthorizationInvalid           RootAuthorizationStatus = "invalid"
)

type RootAuthorizationResultDTO struct {
	RequestedPath string                  `json:"requested_path"`
	CanonicalPath string                  `json:"canonical_path,omitempty"`
	Status        RootAuthorizationStatus `json:"status"`
	Error         string                  `json:"error,omitempty"`
}

type BatchRootsDTO struct {
	AuthorizationSucceeded bool                         `json:"authorization_succeeded"`
	Results                []RootAuthorizationResultDTO `json:"results"`
	Roots                  []RootDTO                    `json:"roots"`
	Scan                   *StartScanDTO                `json:"scan,omitempty"`
	ScanError              string                       `json:"scan_error,omitempty"`
}

type StatsDTO struct {
	Files           int64 `json:"files"`
	Bytes           int64 `json:"bytes"`
	Recommendations int   `json:"recommendations"`
}

type ScanState string

const (
	ScanIdle       ScanState = "idle"
	ScanRunning    ScanState = "running"
	ScanCancelling ScanState = "cancelling"
)

type ProgressBudgetDTO struct {
	Limit int64 `json:"limit"`
	Used  int64 `json:"used"`
}

type ScanProgressDTO struct {
	Phase            string            `json:"phase"`
	ObservedEntries  int64             `json:"observed_entries"`
	Files            int64             `json:"files"`
	Directories      int64             `json:"directories"`
	Bytes            int64             `json:"bytes"`
	RootsStarted     int               `json:"roots_started"`
	RootsCompleted   int               `json:"roots_completed"`
	RootsTotal       int               `json:"roots_total"`
	CurrentRootIndex int               `json:"current_root_index,omitempty"`
	CurrentRootLabel string            `json:"current_root_label,omitempty"`
	ElapsedMS        int64             `json:"elapsed_ms"`
	EntryBudget      ProgressBudgetDTO `json:"entry_budget"`
	ErrorBudget      ProgressBudgetDTO `json:"error_budget"`
	DurationBudget   ProgressBudgetDTO `json:"duration_budget_ms"`
	Cancelling       bool              `json:"cancelling"`
	BudgetTruncated  bool              `json:"budget_truncated"`
	TruncationReason string            `json:"truncation_reason,omitempty"`
}

type StatusDTO struct {
	Scanning        bool             `json:"scanning"`
	State           ScanState        `json:"state"`
	ScanID          string           `json:"scan_id,omitempty"`
	TaskResult      string           `json:"task_result,omitempty"`
	LastScan        string           `json:"last_scan"`
	LastError       string           `json:"last_error,omitempty"`
	Stats           StatsDTO         `json:"stats"`
	// ScanReadOnly reports the scan pipeline's guarantee only. Recycling runs
	// on a separate, individually confirmed endpoint.
	ScanReadOnly    bool             `json:"scan_readonly"`
	Network         bool             `json:"network"`
	Partial         bool             `json:"partial"`
	Truncated       bool             `json:"truncated"`
	TruncationCause string           `json:"truncation_reason,omitempty"`
	ErrorCount      int              `json:"error_count"`
	Progress        *ScanProgressDTO `json:"progress,omitempty"`
}

type StartScanDTO struct {
	Accepted bool   `json:"accepted"`
	ScanID   string `json:"scan_id,omitempty"`
}

type NodeDTO struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Path     string  `json:"path,omitempty"`
	Size     int64   `json:"size"`
	Zone     string  `json:"zone"`
	Health   int     `json:"health"`
	Modified string  `json:"modified"`
	Kind     string  `json:"kind"`
	Insight  string  `json:"insight"`
	X        float64 `json:"x,omitempty"`
	Y        float64 `json:"y,omitempty"`
}

type RecommendationDTO struct {
	ID     string `json:"id"`
	NodeID string `json:"node_id"`
}

type MapDTO struct {
	Nodes           []NodeDTO           `json:"nodes"`
	Stats           StatsDTO            `json:"stats"`
	Recommendations []RecommendationDTO `json:"recommendations"`
	// Stats cover the whole scan but Nodes are capped at the largest entries.
	// These fields let the UI say so instead of presenting a partial canvas as
	// the complete picture.
	NodesTruncated bool `json:"nodes_truncated,omitempty"`
	NodesOmitted   int  `json:"nodes_omitted,omitempty"`
	NodesTotal     int  `json:"nodes_total,omitempty"`
}

type RevealDTO struct {
	OK bool `json:"ok"`
}

// CapabilitiesDTO is rendered verbatim in the privacy audit panel. A false
// value renders as "禁用"; every capability the application actually has must
// carry an explicit, non-false description instead of relying on a zero value.
type CapabilitiesDTO struct {
	FileContentRewrite  bool   `json:"文件内容改写"`
	PermanentDelete     bool   `json:"永久删除"`
	MoveOrRename        bool   `json:"移动或重命名"`
	ArbitraryShell      bool   `json:"任意 Shell"`
	NetworkUpload       bool   `json:"网络上传"`
	FileContentRead     bool   `json:"文件内容读取"`
	RecycleBin          string `json:"移入回收站"`
	LocalHash           bool   `json:"本地哈希"`
	AuthorizedRootCount int    `json:"授权根目录数"`
	RecycledItemCount   int    `json:"已移入回收站条目"`
}

type PrivacyDTO struct {
	Capabilities  CapabilitiesDTO   `json:"capabilities"`
	AgentPayload  privacy.AgentScan `json:"agent_payload"`
	ExcludedNames []string          `json:"excluded_names"`
}

type DemoDTO struct {
	Nodes []NodeDTO `json:"nodes"`
}

type ScanResultDTO struct {
	ScanID string       `json:"scan_id"`
	Stats  StatsDTO     `json:"stats"`
	Report agent.Report `json:"report"`
}

func stats(scan model.Scan) StatsDTO {
	var out StatsDTO
	for _, entry := range scan.Entries {
		if entry.Kind == model.KindFile {
			out.Files++
			out.Bytes += entry.Size
		}
	}
	out.Recommendations = len(recommendations(scan))
	return out
}
