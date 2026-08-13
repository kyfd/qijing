// Package appicon exposes the shared Windows application icon.
package appicon

import _ "embed"

// ICO contains the multi-resolution application icon used by the window,
// executable and notification area.
//
//go:embed appicon.ico
var ICO []byte
