package web

import "embed"

// Assets contains the dependency-free local user interface. The module
// directories mirror the front-end layout: each directory is one feature
// module loaded natively via ES modules (no bundler, no build step).
//
//go:embed index.html app.css app.js assets api state components map scan agent recycle settings diagnostics workers
var Assets embed.FS
