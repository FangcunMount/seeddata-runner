package scheduler

import (
	"context"
	"testing"
	"time"
)

func TestParseClock(t *testing.T) {
	clock, err := ParseClock("10:30")
	if err != nil {
		t.Fatalf("ParseClock returned error: %v", err)
	}
	if clock.Hour != 10 || clock.Minute != 30 {
		t.Fatalf("unexpected clock: %#v", clock)
	}
}

func TestParseRelativeDuration(t *testing.T) {
	tests := []struct {
		raw      string
		expected time.Duration
	}{
		{raw: "30m", expected: 30 * time.Minute},
		{raw: "2d", expected: 48 * time.Hour},
		{raw: "1.5w", expected: time.Duration(1.5 * float64(7*24*time.Hour))},
	}

	for _, tt := range tests {
		got, err := ParseRelativeDuration(tt.raw)
		if err != nil {
			t.Fatalf("ParseRelativeDuration(%q) returned error: %v", tt.raw, err)
		}
		if got != tt.expected {
			t.Fatalf("ParseRelativeDuration(%q)=%s, want=%s", tt.raw, got, tt.expected)
		}
	}
}

func TestWindowLatestEligibleSlot(t *testing.T) {
	window, err := NewWindow(
		Clock{Hour: 10, Minute: 0},
		Clock{Hour: 18, Minute: 0},
		30*time.Minute,
	)
	if err != nil {
		t.Fatalf("NewWindow returned error: %v", err)
	}

	day := time.Date(2026, 4, 20, 0, 0, 0, 0, time.Local)
	slot, ok := window.LatestEligibleSlot(time.Date(2026, 4, 20, 10, 20, 0, 0, time.Local), day)
	if !ok {
		t.Fatal("expected eligible slot")
	}
	want := time.Date(2026, 4, 20, 10, 0, 0, 0, time.Local)
	if !slot.Equal(want) {
		t.Fatalf("unexpected slot: got %s want %s", slot, want)
	}

	slot, ok = window.LatestEligibleSlot(time.Date(2026, 4, 20, 19, 5, 0, 0, time.Local), day)
	if !ok {
		t.Fatal("expected eligible slot after window end")
	}
	want = time.Date(2026, 4, 20, 18, 0, 0, 0, time.Local)
	if !slot.Equal(want) {
		t.Fatalf("unexpected end slot: got %s want %s", slot, want)
	}
}

func TestIntervalNextDelay(t *testing.T) {
	intervals := NewInterval(30*time.Second, time.Minute, 0, 0)
	if got := intervals.NextDelay(false); got != 30*time.Second {
		t.Fatalf("unexpected idle delay: %s", got)
	}
	if got := intervals.NextDelay(true); got != time.Minute {
		t.Fatalf("unexpected active delay: %s", got)
	}
}

func TestResolveWindowConfig(t *testing.T) {
	t.Run("legacy single run", func(t *testing.T) {
		resolved, err := ResolveWindowConfig(WindowConfig{
			RunAt:        "10:00",
			DefaultRunAt: "09:00",
			FieldPrefix:  "dailySimulation",
		})
		if err != nil {
			t.Fatalf("ResolveWindowConfig returned error: %v", err)
		}
		if !resolved.LegacySingle {
			t.Fatal("expected legacy single-run schedule")
		}
		if resolved.IntervalRaw != "24h" {
			t.Fatalf("unexpected legacy interval: %q", resolved.IntervalRaw)
		}
	})

	t.Run("window defaults", func(t *testing.T) {
		resolved, err := ResolveWindowConfig(WindowConfig{
			RunAt:              "10:00",
			WindowStartAt:      "",
			WindowEndAt:        "",
			Interval:           "30m",
			DefaultRunAt:       "10:00",
			DefaultWindowEndAt: "18:00",
			DefaultInterval:    "30m",
			FieldPrefix:        "dailySimulation",
		})
		if err != nil {
			t.Fatalf("ResolveWindowConfig returned error: %v", err)
		}
		if resolved.LegacySingle {
			t.Fatal("expected window schedule")
		}
		if resolved.StartAtRaw != "10:00" || resolved.EndAtRaw != "18:00" || resolved.IntervalRaw != "30m" {
			t.Fatalf("unexpected resolved config: %#v", resolved)
		}
	})
}

func TestWaitHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Wait(ctx, time.Second); err == nil {
		t.Fatal("expected context cancellation error")
	}
}
