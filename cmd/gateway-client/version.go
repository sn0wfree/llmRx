package main

import (
	"runtime"
	"runtime/debug"
)

// Build identity, injected via -ldflags by goreleaser / Makefile:
//   -X main.version=v0.1.0 -X main.commit=abc1234 -X main.date=...
// Falls back to the toolchain's VCS stamp so a bare `go build` still
// reports a real revision.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func buildVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	if version == "" {
		return "dev"
	}
	return version
}

func buildCommit() string {
	if commit != "" && commit != "none" {
		return short(commit)
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				return short(s.Value)
			}
		}
	}
	return "none"
}

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
	return "unknown"
}

func buildInfoString() string {
	return buildVersion() + " (" + buildCommit() + ", " + buildDate() + ", " + runtime.Version() + ", edge)"
}

func short(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}
