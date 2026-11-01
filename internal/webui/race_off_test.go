//go:build !race

package webui

// raceEnabled reports whether the binary was built with the race
// detector. See race_on_test.go.
const raceEnabled = false
