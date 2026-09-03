// Package buildinfo carries the application's build identity. A benchmark
// number is only interpretable next to the build that produced it, so the
// version has to be recorded rather than described in prose.
package buildinfo

import "runtime/debug"

// Version is the application version. Release builds set it through
// -ldflags "-X github.com/kyfd/qijing/internal/buildinfo.Version=v0.1.3";
// an unstamped build honestly reports "dev" rather than claiming a release.
var Version = "dev"

// Revision reports the VCS commit the binary was built from, when the Go
// toolchain recorded one. It is empty for builds made from a dirty or
// non-VCS tree, and is never guessed.
func Revision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if revision == "" {
		return ""
	}
	if modified == "true" {
		// A dirty tree does not correspond to the commit, and saying so is
		// the difference between a reproducible report and a misleading one.
		return revision + "+dirty"
	}
	return revision
}

// GoVersion reports the toolchain that built the binary.
func GoVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.GoVersion
	}
	return ""
}
