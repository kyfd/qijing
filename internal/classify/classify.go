package classify

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"fileecosystem/internal/config"
	"fileecosystem/internal/model"
)

// Apply replaces all derived classes on entries using a stable clock value.
func Apply(entries []model.Entry, now time.Time, cfg config.Config) {
	for i := range entries {
		e := &entries[i]
		e.Classes = e.Classes[:0]
		age := now.Sub(e.ModTime)
		if age < 0 {
			age = 0
		}
		if e.Kind == model.KindFile {
			switch {
			case age <= cfg.SeedlingAge:
				e.Classes = append(e.Classes, model.ClassSeedling)
			case age >= cfg.DormantAge:
				e.Classes = append(e.Classes, model.ClassDormant)
			default:
				e.Classes = append(e.Classes, model.ClassActive)
			}
			if cfg.GiantBytes > 0 && e.Size >= cfg.GiantBytes {
				e.Classes = append(e.Classes, model.ClassGiant)
			}
			if containsExtension(cfg.OrphanExtensions, e.Extension) {
				e.Classes = append(e.Classes, model.ClassOrphan)
			}
			if age >= cfg.RottenAge && containsExtension(cfg.RottenExtensions, e.Extension) {
				e.Classes = append(e.Classes, model.ClassRotten)
			}
		}
		if e.Kind == model.KindDirectory && e.GitProject && age >= cfg.GitZombieAge {
			e.Classes = append(e.Classes, model.ClassGitZombie)
		}
		slices.Sort(e.Classes)
	}
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
