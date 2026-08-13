package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"fileecosystem/internal/config"
	"fileecosystem/internal/privacy"
	"fileecosystem/internal/scanner"
)

// ecosystem-scanner is a deliberately narrow helper. Its only filesystem
// capability is read-only scanning; its JSON output never includes paths.
func main() {
	hash := flag.Bool("hash", false, "calculate local hashes")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: ecosystem-scanner [-hash] ROOT...")
		os.Exit(2)
	}
	cfg := config.Default()
	cfg.Roots = flag.Args()
	cfg.HashSHA256 = *hash
	engine, err := scanner.New(cfg)
	if err != nil {
		fail(err)
	}
	result, err := engine.Scan(context.Background())
	if err != nil {
		fail(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(privacy.Isolate(result)); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
