//go:build race

package webui

// raceEnabled reports whether the binary was built with the race
// detector, which instruments every memory access and (combined with
// many packages running in parallel) noticeably slows wall-clock
// timings. Timing-based assertions scale their budgets accordingly.
const raceEnabled = true
