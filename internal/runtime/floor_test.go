package runtime

import "testing"

// Floor bounds: SetX floor branches return clamped values.

func TestSetStreamTimeoutSec_NegativeFloorsToZero(t *testing.T) {
	d := New()
	d.SetStreamTimeoutSec(-1)
	if d.StreamTimeoutSec() != 0 {
		t.Errorf("got %d, want 0", d.StreamTimeoutSec())
	}
}

func TestSetStreamTimeoutSec_TooLargeClampsToHour(t *testing.T) {
	d := New()
	d.SetStreamTimeoutSec(7200) // 2h
	if d.StreamTimeoutSec() != 3600 {
		t.Errorf("got %d, want 3600", d.StreamTimeoutSec())
	}
}

func TestSetStreamTimeoutSec_BoundaryExact(t *testing.T) {
	d := New()
	d.SetStreamTimeoutSec(3600)
	if d.StreamTimeoutSec() != 3600 {
		t.Errorf("got %d, want 3600", d.StreamTimeoutSec())
	}
}

func TestSetStreamTimeoutSec_NormalValue(t *testing.T) {
	d := New()
	d.SetStreamTimeoutSec(60)
	if d.StreamTimeoutSec() != 60 {
		t.Errorf("got %d, want 60", d.StreamTimeoutSec())
	}
}

func TestSetStreamMaxBodyBytes_NegativeFloorsToZero(t *testing.T) {
	d := New()
	d.SetStreamMaxBodyBytes(-5)
	if d.StreamMaxBodyBytes() != 0 {
		t.Errorf("got %d, want 0", d.StreamMaxBodyBytes())
	}
}

func TestSetStreamMaxBodyBytes_TooLargeClampsToMax(t *testing.T) {
	d := New()
	d.SetStreamMaxBodyBytes(1 << 31) // >1GiB
	if got := d.StreamMaxBodyBytes(); got <= 0 || got > (1<<30) {
		t.Errorf("expected clamp to 1GiB or default, got %d", got)
	}
}

func TestSetMaxLogSubscribers_NegativeFloorsToZero(t *testing.T) {
	d := New()
	d.SetMaxLogSubscribers(-100)
	if d.MaxLogSubscribers() != 0 {
		t.Errorf("got %d, want 0", d.MaxLogSubscribers())
	}
}

func TestSetMaxLogSubscribers_NormalValue(t *testing.T) {
	d := New()
	d.SetMaxLogSubscribers(50)
	if d.MaxLogSubscribers() != 50 {
		t.Errorf("got %d, want 50", d.MaxLogSubscribers())
	}
}

func TestSetLogLevel_NormalValue(t *testing.T) {
	d := New()
	d.SetLogLevel(2)
	if d.LogLevel() != 2 {
		t.Errorf("got %d, want 2", d.LogLevel())
	}
}

func TestSetLogLevel_OutOfRangeClamps(t *testing.T) {
	d := New()
	d.SetLogLevel(99)
	if got := d.LogLevel(); got < 0 || got > 3 {
		t.Errorf("expected clamp to [0..3], got %d", got)
	}
}

func TestSetLogRetentionDays_NegativeFloorsToZero(t *testing.T) {
	d := New()
	d.SetLogRetentionDays(-1)
	if d.LogRetentionDays() != 0 {
		t.Errorf("got %d, want 0", d.LogRetentionDays())
	}
}

func TestSetLogRetentionDays_TooLargeClamps(t *testing.T) {
	d := New()
	d.SetLogRetentionDays(100000)
	if got := d.LogRetentionDays(); got > 3650 {
		t.Errorf("expected clamp to 3650, got %d", got)
	}
}

func TestSetBreakerResetTimeoutMs_TooSmallFloors(t *testing.T) {
	d := New()
	d.SetBreakerResetTimeoutMs(50)
	if got := d.BreakerResetTimeoutMs(); got < 100 {
		t.Errorf("got %d, want >=100", got)
	}
}

func TestSetBreakerResetTimeoutMs_TooLargeClamps(t *testing.T) {
	d := New()
	d.SetBreakerResetTimeoutMs(48 * 60 * 60 * 1000)
	if got := d.BreakerResetTimeoutMs(); got > 24*60*60*1000 {
		t.Errorf("got %d, want <=24h", got)
	}
}

func TestSetAlertCooldownSec_NegativeFloors(t *testing.T) {
	d := New()
	d.SetAlertCooldownSec(-1)
	if d.AlertCooldownSec() != 0 {
		t.Errorf("got %d, want 0", d.AlertCooldownSec())
	}
}

func TestSetAlertCooldownSec_TooLargeClamps(t *testing.T) {
	d := New()
	d.SetAlertCooldownSec(48 * 60 * 60)
	if got := d.AlertCooldownSec(); got > 24*60*60 {
		t.Errorf("got %d, want <=24h", got)
	}
}

func TestSetBreakerMaxFailures_NegativeFloors(t *testing.T) {
	d := New()
	d.SetBreakerMaxFailures(-1)
	if d.BreakerMaxFailures() != 5 {
		t.Errorf("got %d, want 5 (default)", d.BreakerMaxFailures())
	}
}

func TestSetBreakerMaxFailures_ZeroFloors(t *testing.T) {
	d := New()
	d.SetBreakerMaxFailures(0)
	if d.BreakerMaxFailures() != 5 {
		t.Errorf("got %d, want 5 (default)", d.BreakerMaxFailures())
	}
}

func TestSetCostStrategy_EmptyFloorsToCheapest(t *testing.T) {
	d := New()
	d.SetCostStrategy("")
	if d.CostStrategy() != "cheapest" {
		t.Errorf("got %q, want cheapest", d.CostStrategy())
	}
}

func TestSetMarkupRatio_NegativeStaysAtOne(t *testing.T) {
	d := New()
	d.SetMarkupRatio(-0.5)
	if got := d.MarkupRatio(); got != 1.0 {
		t.Errorf("got %v, want 1.0", got)
	}
}

// --- levelForLine branches ---

func TestLevelForLine_Error(t *testing.T) {
	if got := levelForLine("error: boom"); got != 3 {
		t.Errorf("error: = %d, want 3", got)
	}
}

func TestLevelForLine_Warn(t *testing.T) {
	if got := levelForLine("warn: warning"); got != 2 {
		t.Errorf("warn: = %d, want 2", got)
	}
}

func TestLevelForLine_Info(t *testing.T) {
	if got := levelForLine("info: note"); got != 1 {
		t.Errorf("info: = %d, want 1", got)
	}
}

func TestLevelForLine_Debug(t *testing.T) {
	if got := levelForLine("debug: detail"); got != 0 {
		t.Errorf("debug: = %d, want 0", got)
	}
}

func TestLevelForLine_NoPrefixDefaultsToInfo(t *testing.T) {
	if got := levelForLine("nothing"); got != 1 {
		t.Errorf("no-prefix = %d, want 1", got)
	}
}

func TestLevelForLine_TooShortDefaultsToInfo(t *testing.T) {
	if got := levelForLine("err"); got != 1 {
		t.Errorf("too-short = %d, want 1", got)
	}
}

func TestLevelForLine_Empty(t *testing.T) {
	if got := levelForLine(""); got != 1 {
		t.Errorf("empty = %d, want 1", got)
	}
}

// --- FormatLevel branches ---

func TestFormatLevel_Debug(t *testing.T) {
	if got := FormatLevel(0); got != "debug: " {
		t.Errorf("got %q, want 'debug: '", got)
	}
}

func TestFormatLevel_Info(t *testing.T) {
	if got := FormatLevel(1); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestFormatLevel_Warn(t *testing.T) {
	if got := FormatLevel(2); got != "warn: " {
		t.Errorf("got %q, want 'warn: '", got)
	}
}

func TestFormatLevel_Error(t *testing.T) {
	if got := FormatLevel(3); got != "error: " {
		t.Errorf("got %q, want 'error: '", got)
	}
}

func TestFormatLevel_UnknownDefaultsEmpty(t *testing.T) {
	if got := FormatLevel(99); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
