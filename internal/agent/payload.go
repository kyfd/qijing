package agent

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kyfd/qijing/internal/model"
)

const PayloadSchemaVersion = 1

type CloudPayload struct {
	SchemaVersion int             `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	SnapshotID    string          `json:"snapshot_id"`
	Overview      CloudOverview   `json:"overview"`
	Regions       []RegionSummary `json:"regions"`
}

type CloudOverview struct {
	EntryCountBucket string              `json:"entry_count_bucket"`
	TotalSizeBucket  string              `json:"total_size_bucket"`
	ClassCounts      map[model.Class]int `json:"class_counts"`
	ErrorCountBucket string              `json:"error_count_bucket"`
}

type RegionSummary struct {
	ID          string              `json:"id"`
	Kind        model.Kind          `json:"kind"`
	TypeGroup   string              `json:"type_group"`
	CountBucket string              `json:"count_bucket"`
	SizeBucket  string              `json:"size_bucket"`
	AgeBuckets  map[string]int      `json:"age_buckets"`
	ClassCounts map[model.Class]int `json:"class_counts"`
	ErrorCount  int                 `json:"error_count"`
}

type PayloadPreview struct {
	Body []byte `json:"body"`
	Hash string `json:"sha256"`
	Size int    `json:"size"`
}

// BuildCloudPayload aggregates a snapshot into the anonymous summary. It is a
// convenience wrapper over BuildCloudPayloadStream for callers that already
// hold the entries.
func BuildCloudPayload(scan model.Scan) (CloudPayload, error) {
	return BuildCloudPayloadStream(scan, func(visit func(model.Entry) error) error {
		for _, entry := range scan.Entries {
			if err := visit(entry); err != nil {
				return err
			}
		}
		return nil
	})
}

// regionAccumulator holds only the aggregate numbers for one region. Entries
// are folded into it and discarded: no privileged value is retained, and the
// payload is constructed field by field from an allowlist rather than by
// copying an entry and removing what must not be sent.
type regionAccumulator struct {
	kind        model.Kind
	typeGroup   string
	count       int
	size        int64
	ageBuckets  map[string]int
	classCounts map[model.Class]int
	errorCount  int
}

// BuildCloudPayloadStream aggregates a snapshot supplied one entry at a time,
// so the payload can be built straight from storage without materializing the
// scan. The scan argument supplies metadata only; its Entries field is not
// read.
func BuildCloudPayloadStream(scan model.Scan, each func(visit func(model.Entry) error) error) (CloudPayload, error) {
	runSalt := make([]byte, 32)
	if _, err := rand.Read(runSalt); err != nil {
		return CloudPayload{}, fmt.Errorf("create run identity: %w", err)
	}
	requestID, err := randomID("run")
	if err != nil {
		return CloudPayload{}, err
	}
	snapshotID := transientID(runSalt, scan.ID)
	type key struct{ root, kind, group string }
	regions := make(map[key]*regionAccumulator)
	classCounts := make(map[model.Class]int)
	var total int64
	var entryCount int
	err = each(func(entry model.Entry) error {
		entryCount++
		total += max(entry.Size, 0)
		for _, class := range entry.Classes {
			classCounts[class]++
		}
		k := key{entry.RootID, string(entry.Kind), typeGroup(entry.Extension)}
		region := regions[k]
		if region == nil {
			region = &regionAccumulator{kind: entry.Kind, typeGroup: k.group,
				ageBuckets: map[string]int{}, classCounts: map[model.Class]int{}}
			regions[k] = region
		}
		region.count++
		region.size += max(entry.Size, 0)
		region.ageBuckets[ageBucket(scan.EndedAt.Sub(entry.ModTime).Hours()/24)]++
		if entry.Error != "" {
			region.errorCount++
		}
		for _, class := range entry.Classes {
			region.classCounts[class]++
		}
		return nil
	})
	if err != nil {
		return CloudPayload{}, err
	}
	errorCount := scan.ErrorCount
	if errorCount == 0 && len(scan.Errors) > 0 {
		errorCount = len(scan.Errors)
	}
	payload := CloudPayload{SchemaVersion: PayloadSchemaVersion, RequestID: requestID, SnapshotID: snapshotID, Overview: CloudOverview{EntryCountBucket: countBucket(entryCount), TotalSizeBucket: sizeBucket(total), ClassCounts: classCounts, ErrorCountBucket: countBucket(errorCount)}}
	for k, region := range regions {
		payload.Regions = append(payload.Regions, RegionSummary{
			ID:          transientID(runSalt, k.root+"\x00"+k.kind+"\x00"+k.group),
			Kind:        region.kind,
			TypeGroup:   region.typeGroup,
			CountBucket: countBucket(region.count),
			SizeBucket:  sizeBucket(region.size),
			AgeBuckets:  region.ageBuckets,
			ClassCounts: region.classCounts,
			ErrorCount:  region.errorCount,
		})
	}
	sort.Slice(payload.Regions, func(i, j int) bool { return payload.Regions[i].ID < payload.Regions[j].ID })
	return payload, nil
}

func PreviewPayload(payload CloudPayload) (PayloadPreview, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return PayloadPreview{}, err
	}
	hash := sha256.Sum256(body)
	return PayloadPreview{Body: body, Hash: hex.EncodeToString(hash[:]), Size: len(body)}, nil
}

func randomID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b), nil
}

func transientID(salt []byte, source string) string {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(source))
	return "region_" + hex.EncodeToString(h.Sum(nil)[:8])
}

func typeGroup(ext string) string {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	groups := map[string]string{"txt": "document", "md": "document", "pdf": "document", "doc": "document", "docx": "document", "jpg": "image", "jpeg": "image", "png": "image", "gif": "image", "webp": "image", "mp4": "video", "mkv": "video", "mov": "video", "avi": "video", "zip": "archive", "7z": "archive", "rar": "archive", "tar": "archive", "gz": "archive", "go": "source_code", "js": "source_code", "ts": "source_code", "py": "source_code", "java": "source_code", "rs": "source_code"}
	if group, ok := groups[ext]; ok {
		return group
	}
	return "other"
}

func countBucket(n int) string {
	switch {
	case n == 0:
		return "0"
	case n < 10:
		return "1-9"
	case n < 100:
		return "10-99"
	case n < 1000:
		return "100-999"
	default:
		return "1000+"
	}
}

func sizeBucket(n int64) string {
	const mib, gib = int64(1 << 20), int64(1 << 30)
	switch {
	case n == 0:
		return "0"
	case n < mib:
		return "under_1_mib"
	case n < 100*mib:
		return "1_100_mib"
	case n < gib:
		return "100_mib_1_gib"
	case n < 10*gib:
		return "1_10_gib"
	default:
		return "10_gib_plus"
	}
}

func ageBucket(days float64) string {
	switch {
	case days < 0:
		return "future_or_unknown"
	case days < 7:
		return "under_7_days"
	case days < 30:
		return "7_29_days"
	case days < 180:
		return "30_179_days"
	case days < 365:
		return "180_364_days"
	default:
		return "365_days_plus"
	}
}
