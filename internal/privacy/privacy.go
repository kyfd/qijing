// Package privacy defines path-free views safe to cross the agent boundary.
package privacy

import (
	"path/filepath"
	"sort"

	"github.com/kyfd/qijing/internal/model"
)

// AgentEntry deliberately has no Path, Relative, root path, or filename field.
type AgentEntry struct {
	ID        string        `json:"id"`
	RootID    string        `json:"root_id"`
	Kind      model.Kind    `json:"kind"`
	Extension string        `json:"extension,omitempty"`
	Size      int64         `json:"size"`
	AgeDays   int64         `json:"age_days"`
	Classes   []model.Class `json:"classes,omitempty"`
	HasError  bool          `json:"has_error"`
}

type AgentRelation struct {
	FromID string `json:"from_id"`
	ToID   string `json:"to_id"`
	Type   string `json:"type"`
}

type AgentScan struct {
	ID         string          `json:"id"`
	Entries    []AgentEntry    `json:"entries"`
	Relations  []AgentRelation `json:"relations"`
	ErrorCount int             `json:"error_count"`
}

// Isolate performs the sole supported conversion to agent-visible scan data.
func Isolate(scan model.Scan) AgentScan {
	out, _ := IsolateStream(scan, func(visit func(model.Entry) error) error {
		for _, entry := range scan.Entries {
			if err := visit(entry); err != nil {
				return err
			}
		}
		return nil
	})
	return out
}

// IsolateStream is the same conversion driven by a caller-supplied walk, so a
// snapshot can be isolated straight out of storage without ever holding every
// privileged entry in memory. The scan argument supplies metadata only; its
// Entries field is not read.
func IsolateStream(scan model.Scan, each func(visit func(model.Entry) error) error) (AgentScan, error) {
	out := AgentScan{ID: scan.ID, ErrorCount: scan.ErrorCount}
	if out.ErrorCount == 0 && len(scan.Errors) > 0 {
		out.ErrorCount = len(scan.Errors)
	}
	err := each(func(entry model.Entry) error {
		out.Entries = append(out.Entries, isolateEntry(scan, entry))
		return nil
	})
	if err != nil {
		return AgentScan{}, err
	}
	for _, relation := range scan.Relations {
		out.Relations = append(out.Relations, AgentRelation(relation))
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].ID < out.Entries[j].ID })
	return out, nil
}

// isolateEntry constructs the agent-visible entry from an allowlist of fields.
// It never copies the privileged entry and then strips it: every field present
// here is written explicitly, so a new field on model.Entry cannot leak by
// default.
func isolateEntry(scan model.Scan, entry model.Entry) AgentEntry {
	age := scan.EndedAt.Sub(entry.ModTime)
	if age < 0 {
		age = 0
	}
	return AgentEntry{
		ID: entry.ID, RootID: entry.RootID, Kind: entry.Kind,
		Extension: filepath.Ext(entry.Extension), Size: entry.Size,
		AgeDays: int64(age.Hours() / 24),
		Classes: append([]model.Class(nil), entry.Classes...), HasError: entry.Error != "",
	}
}
