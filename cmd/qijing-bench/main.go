// Command qijing-bench measures scans over reproducible synthetic fixtures
// and writes a report that states the environment and build behind every
// number. It writes only inside the directory given by --work.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kyfd/qijing/internal/config"
	"github.com/kyfd/qijing/internal/scanbench"
	"github.com/kyfd/qijing/internal/scanbroker"
	"github.com/kyfd/qijing/internal/scanner"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("qijing-bench", flag.ContinueOnError)
	work := flags.String("work", "", "directory for fixtures and reports (required)")
	files := flags.Int("files", 100_000, "files per standard fixture")
	scale := flags.Bool("million", false, "also run the 1,000,000 file scenario")
	subprocess := flags.Bool("subprocess", false, "measure the qijing-scanner subprocess instead of the in-process scanner")
	timeout := flags.Duration("timeout", time.Hour, "overall run timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *work == "" {
		return fmt.Errorf("--work is required; the benchmark writes fixtures there and nowhere else")
	}
	if *files <= 0 {
		return fmt.Errorf("--files must be positive")
	}

	newEngine, err := engineFactory(*subprocess)
	if err != nil {
		return err
	}

	base := config.Default()
	// The budgets must not truncate the fixture, or the report would measure
	// a shorter scan than it claims.
	base.MaxEntries = 0
	base.MaxErrors = 0
	base.MaxDuration = 0
	base.ExcludedNames = nil

	hashed := base
	hashed.HashSHA256 = true

	scenarios := []scanbench.Scenario{
		{
			Name:      fmt.Sprintf("%d 个小文件（4 KiB，哈希关）", *files),
			Fixture:   scanbench.Fixture{Files: *files, Dirs: *files / 200, FileBytes: 4 << 10, Seed: 1},
			Config:    base,
			NewEngine: newEngine,
		},
		{
			Name:      fmt.Sprintf("%d 个小文件（4 KiB，哈希开）", *files),
			Fixture:   scanbench.Fixture{Files: *files, Dirs: *files / 200, FileBytes: 4 << 10, Seed: 1},
			Config:    hashed,
			NewEngine: newEngine,
		},
		{
			Name:      "5,000 个大文件（1 MiB，哈希关）",
			Fixture:   scanbench.Fixture{Files: 5_000, Dirs: 50, FileBytes: 1 << 20, Seed: 2},
			Config:    base,
			NewEngine: newEngine,
		},
	}
	if *scale {
		scenarios = append(scenarios, scanbench.Scenario{
			Name:      "1,000,000 个小文件（1 KiB，哈希关）",
			Fixture:   scanbench.Fixture{Files: 1_000_000, Dirs: 5_000, FileBytes: 1 << 10, Seed: 3},
			Config:    base,
			NewEngine: newEngine,
		})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	report, err := scanbench.Run(ctx, *work, scenarios)
	if err != nil {
		return err
	}

	jsonPath := filepath.Join(*work, "report.json")
	jsonFile, err := os.Create(jsonPath)
	if err != nil {
		return err
	}
	if err := scanbench.WriteJSON(jsonFile, report); err != nil {
		jsonFile.Close()
		return err
	}
	if err := jsonFile.Close(); err != nil {
		return err
	}

	mdPath := filepath.Join(*work, "report.md")
	mdFile, err := os.Create(mdPath)
	if err != nil {
		return err
	}
	if err := scanbench.WriteMarkdown(mdFile, report); err != nil {
		mdFile.Close()
		return err
	}
	if err := mdFile.Close(); err != nil {
		return err
	}

	if err := scanbench.WriteMarkdown(os.Stdout, report); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n报告已写入 %s 与 %s\n", jsonPath, mdPath)
	return nil
}

// engineFactory picks the measured implementation. The subprocess path is the
// one the product ships; the in-process path isolates the traversal cost.
func engineFactory(subprocess bool) (func(config.Config) (scanbench.Engine, error), error) {
	if !subprocess {
		return func(cfg config.Config) (scanbench.Engine, error) { return scanner.New(cfg) }, nil
	}
	executable, err := scanbroker.ResolveScannerExecutable()
	if err != nil {
		return nil, fmt.Errorf("resolve scanner executable: %w", err)
	}
	return func(cfg config.Config) (scanbench.Engine, error) {
		// No sink: the broker returns the scan in memory, which is what this
		// harness measures. The application wires a store-backed sink.
		return scanbroker.New(scanbroker.Options{Config: cfg, Executable: executable}), nil
	}, nil
}
