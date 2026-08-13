package web

import "embed"

// Assets contains the dependency-free local user interface.
//
//go:embed index.html app.css app.js assets
var Assets embed.FS
