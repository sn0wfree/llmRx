package main

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// Build identity. These are meant to be set at link time:
//
//	go build -ldflags "-X main.version=v1.2.3 -X main.commit=abc1234 -X main.date=2026-11-01T10:00:00Z"
//
// goreleaser injects them automatically (see .goreleaser.yaml). When a
// binary is built without ldflags, buildInfoString falls back to the
// VCS stamp the Go toolchain embeds automatically, so even a bare
// `go build ./cmd/gateway` reports a real revision instead of "unknown".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// buildVersion returns the semantic version, preferring the ldflags
// value and falling back to the module version reported by the
// toolchain (e.g. when installed via `go install ...@v1.2.3`).
func buildVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			return bi.Main.Version
		}
	}
	if version == "" {
		return "dev"
	}
	return version
}

// buildCommit returns the git revision, preferring ldflags and falling
// back to the VCS stamp the toolchain writes into the binary.
func buildCommit() string {
	if commit != "" && commit != "none" {
		return shortCommit(commit)
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				return shortCommit(s.Value)
			}
		}
	}
	if commit == "" {
		return "none"
	}
	return commit
}

// buildDate returns the build timestamp, preferring ldflags and
// falling back to the VCS commit time.
func buildDate() string {
	if date != "" && date != "unknown" {
		return date
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.time" && s.Value != "" {
				return s.Value
			}
		}
	}
	if date == "" {
		return "unknown"
	}
	return date
}

// buildInfoString renders the one-line identity shown by -version, the
// startup log and GET /health: "v1.2.3 (abc1234, 2026-11-01T10:00Z, go1.22.12)".
func buildInfoString() string {
	return buildVersion() + " (" + buildCommit() + ", " + buildDate() + ", " + runtime.Version() + ")"
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// vcsDirty reports whether the working tree had uncommitted changes at
// build time. Only meaningful for plain `go build`; release builds are
// always clean.
func vcsDirty() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.modified" {
			return strings.EqualFold(s.Value, "true")
		}
	}
	return false
}
