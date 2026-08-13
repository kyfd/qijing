package classify

import (
	"testing"
	"time"

	"fileecosystem/internal/config"
	"fileecosystem/internal/model"
)

func TestApplyLifecycleAndOrthogonalClasses(t *testing.T) {
	now := time.Now()
	cfg := config.Default()
	cfg.GiantBytes = 10
	entries := []model.Entry{{Kind: model.KindFile, Extension: ".bak", Size: 20, ModTime: now.Add(-800 * 24 * time.Hour)}}
	Apply(entries, now, cfg)
	want := map[model.Class]bool{model.ClassDormant: true, model.ClassGiant: true, model.ClassOrphan: true, model.ClassRotten: true}
	for _, class := range entries[0].Classes {
		delete(want, class)
	}
	if len(want) != 0 {
		t.Fatalf("missing classes: %v; got %v", want, entries[0].Classes)
	}
}
