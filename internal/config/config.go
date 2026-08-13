package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

var DefaultSensitiveNames = []string{
	".aws", ".azure", ".git", ".gnupg", ".kube", ".ssh",
	"AppData", "Library", "System Volume Information",
	"node_modules", "$RECYCLE.BIN",
}

// Config controls a read-only scan. Roots are an explicit allowlist; the
// scanner never discovers or adds roots on its own.
type Config struct {
	Roots         []string
	ExcludedNames []string
	HashSHA256    bool
	// HashWholeDrive separately opts whole-drive roots into content hashing;
	// HashSHA256 alone only applies to ordinary roots.
	HashWholeDrive   bool
	MaxHashBytes     int64
	MaxEntries       int
	MaxErrors        int
	MaxDuration      time.Duration
	GiantBytes       int64
	SeedlingAge      time.Duration
	DormantAge       time.Duration
	RottenAge        time.Duration
	GitZombieAge     time.Duration
	OrphanExtensions []string
	RottenExtensions []string
}

func Default() Config {
	return Config{
		ExcludedNames:    append([]string(nil), DefaultSensitiveNames...),
		MaxHashBytes:     1 << 30,
		MaxEntries:       500_000,
		MaxErrors:        1_000,
		MaxDuration:      30 * time.Minute,
		GiantBytes:       1 << 30,
		SeedlingAge:      7 * 24 * time.Hour,
		DormantAge:       180 * 24 * time.Hour,
		RottenAge:        730 * 24 * time.Hour,
		GitZombieAge:     365 * 24 * time.Hour,
		OrphanExtensions: []string{".tmp", ".bak", ".old", ".orig", ".part"},
		RottenExtensions: []string{".tmp", ".bak", ".old", ".cache", ".log"},
	}
}

func (c Config) Validate() error {
	if len(c.Roots) == 0 {
		return errors.New("at least one allowlisted root is required")
	}
	for _, root := range c.Roots {
		if root == "" || !filepath.IsAbs(root) {
			return fmt.Errorf("root must be absolute: %q", root)
		}
	}
	if c.MaxHashBytes < 0 || c.GiantBytes < 0 {
		return errors.New("byte thresholds cannot be negative")
	}
	if c.MaxEntries < 0 || c.MaxErrors < 0 || c.MaxDuration < 0 {
		return errors.New("scan budgets cannot be negative")
	}
	if c.SeedlingAge < 0 || c.DormantAge < c.SeedlingAge || c.RottenAge < c.DormantAge {
		return errors.New("age thresholds must be non-negative and ordered")
	}
	return nil
}
