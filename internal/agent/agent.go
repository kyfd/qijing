package agent

import (
	"fmt"
	"sort"

	"fileecosystem/internal/model"
	"fileecosystem/internal/privacy"
)

// Report is a path-free, deterministic observation generated without a model.
type Report struct {
	Headline string              `json:"headline"`
	Summary  string              `json:"summary"`
	Counts   map[model.Class]int `json:"counts"`
}

// Observe generates a useful local report from the isolated representation.
// This package cannot receive model.Entry and therefore cannot access paths.
func Observe(scan privacy.AgentScan) Report {
	counts := make(map[model.Class]int)
	for _, entry := range scan.Entries {
		for _, class := range entry.Classes {
			counts[class]++
		}
	}
	type pair struct {
		class model.Class
		count int
	}
	ordered := make([]pair, 0, len(counts))
	for class, count := range counts {
		ordered = append(ordered, pair{class, count})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].count > ordered[j].count })
	if len(ordered) == 0 {
		return Report{Headline: "生态尚未形成", Summary: "没有可供分析的文件元数据。", Counts: counts}
	}
	return Report{
		Headline: "本地生态观察完成",
		Summary:  fmt.Sprintf("共观察 %d 个匿名节点，当前最显著的生态特征是 %s（%d 个）。", len(scan.Entries), ordered[0].class, ordered[0].count),
		Counts:   counts,
	}
}
