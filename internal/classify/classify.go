package classify

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kyfd/qijing/internal/config"
	"github.com/kyfd/qijing/internal/model"
)

// Apply replaces all derived classes on entries using a stable clock value.
func Apply(entries []model.Entry, now time.Time, cfg config.Config) {
	for i := range entries {
		One(&entries[i], now, cfg)
	}
}

// One derives the classes of a single entry. Callers must pass one stable
// clock value per scan (the scan start), so streamed entries classify
// identically to a whole-slice Apply pass.
func One(entry *model.Entry, now time.Time, cfg config.Config) {
	entry.Classes = entry.Classes[:0]
	age := now.Sub(entry.ModTime)
	if age < 0 {
		age = 0
	}
	if entry.Kind == model.KindFile {
		switch {
		case age <= cfg.SeedlingAge:
			entry.Classes = append(entry.Classes, model.ClassSeedling)
		case age >= cfg.DormantAge:
			entry.Classes = append(entry.Classes, model.ClassDormant)
		default:
			entry.Classes = append(entry.Classes, model.ClassActive)
		}
		if cfg.GiantBytes > 0 && entry.Size >= cfg.GiantBytes {
			entry.Classes = append(entry.Classes, model.ClassGiant)
		}
		if containsExtension(cfg.OrphanExtensions, entry.Extension) {
			entry.Classes = append(entry.Classes, model.ClassOrphan)
		}
		if age >= cfg.RottenAge && containsExtension(cfg.RottenExtensions, entry.Extension) {
			entry.Classes = append(entry.Classes, model.ClassRotten)
		}
	}
	if entry.Kind == model.KindDirectory && entry.GitProject && age >= cfg.GitZombieAge {
		entry.Classes = append(entry.Classes, model.ClassGitZombie)
	}
	slices.Sort(entry.Classes)
}

func containsExtension(list []string, extension string) bool {
	extension = strings.ToLower(extension)
	if extension == "" {
		extension = strings.ToLower(filepath.Ext(extension))
	}
	for _, candidate := range list {
		if strings.ToLower(candidate) == extension {
			return true
		}
	}
	return false
}
