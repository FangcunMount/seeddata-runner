package scheduler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Clock 定义时间点
type Clock struct {
	Hour   int
	Minute int
}

// Window 定义时间窗口
type Window struct {
	StartAt  Clock
	EndAt    Clock
	Interval time.Duration
}

// Interval 定义轮询间隔
type Interval struct {
	IdleInterval   time.Duration
	ActiveInterval time.Duration
}

// WindowConfig 定义 runAt/window/interval 配置
type WindowConfig struct {
	RunAt              string
	WindowStartAt      string
	WindowEndAt        string
	Interval           string
	DefaultRunAt       string
	DefaultWindowEndAt string
	DefaultInterval    string
	FieldPrefix        string
}

// ResolvedWindowConfig 返回解析后的窗口配置
type ResolvedWindowConfig struct {
	Window       Window
	StartAtRaw   string
	EndAtRaw     string
	IntervalRaw  string
	LegacySingle bool
}

// ParseClock 解析时间点
func ParseClock(raw string) (Clock, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Clock{}, fmt.Errorf("clock is empty")
	}
	parsed, err := time.ParseInLocation("15:04", trimmed, time.Local)
	if err != nil {
		return Clock{}, err
	}
	return Clock{
		Hour:   parsed.Hour(),
		Minute: parsed.Minute(),
	}, nil
}

// String 返回时间点字符串
func (c Clock) String() string {
	return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute)
}

// ParseRelativeDuration 解析相对时长
func ParseRelativeDuration(raw string) (time.Duration, error) {
	trimmed := strings.TrimSpace(strings.ToLower(raw))
	if trimmed == "" {
		return 0, fmt.Errorf("duration is empty")
	}

	if strings.HasSuffix(trimmed, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(trimmed, "d"), 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	if strings.HasSuffix(trimmed, "w") {
		weeks, err := strconv.ParseFloat(strings.TrimSuffix(trimmed, "w"), 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(weeks * float64(7*24*time.Hour)), nil
	}
	return time.ParseDuration(trimmed)
}

// NewSingleRunWindow 创建单次运行窗口
func NewSingleRunWindow(clock Clock) Window {
	return Window{
		StartAt:  clock,
		EndAt:    clock,
		Interval: 24 * time.Hour,
	}
}

// NewWindow 创建时间窗口
func NewWindow(startAt, endAt Clock, interval time.Duration) (Window, error) {
	if endAt.Hour < startAt.Hour || (endAt.Hour == startAt.Hour && endAt.Minute < startAt.Minute) {
		return Window{}, fmt.Errorf("window end must be >= start")
	}
	if interval <= 0 {
		return Window{}, fmt.Errorf("window interval must be positive")
	}
	return Window{
		StartAt:  startAt,
		EndAt:    endAt,
		Interval: interval,
	}, nil
}

// ResolveWindowConfig 解析 runAt/window/interval 配置为统一窗口
func ResolveWindowConfig(cfg WindowConfig) (ResolvedWindowConfig, error) {
	startRaw := strings.TrimSpace(cfg.WindowStartAt)
	endRaw := strings.TrimSpace(cfg.WindowEndAt)
	intervalRaw := strings.TrimSpace(cfg.Interval)

	if startRaw == "" && endRaw == "" && intervalRaw == "" {
		runAt := firstNonEmpty(cfg.RunAt, cfg.DefaultRunAt)
		runClock, err := ParseClock(runAt)
		if err != nil {
			return ResolvedWindowConfig{}, fmt.Errorf("%s is invalid: %w", cfg.field("runAt"), err)
		}
		return ResolvedWindowConfig{
			Window:       NewSingleRunWindow(runClock),
			StartAtRaw:   runAt,
			EndAtRaw:     runAt,
			IntervalRaw:  "24h",
			LegacySingle: true,
		}, nil
	}

	startRaw = firstNonEmpty(startRaw, cfg.RunAt, cfg.DefaultRunAt)
	endRaw = firstNonEmpty(endRaw, cfg.DefaultWindowEndAt)
	intervalRaw = firstNonEmpty(intervalRaw, cfg.DefaultInterval)

	startClock, err := ParseClock(startRaw)
	if err != nil {
		return ResolvedWindowConfig{}, fmt.Errorf("%s is invalid: %w", cfg.field("windowStartAt"), err)
	}
	endClock, err := ParseClock(endRaw)
	if err != nil {
		return ResolvedWindowConfig{}, fmt.Errorf("%s is invalid: %w", cfg.field("windowEndAt"), err)
	}
	interval, err := ParseRelativeDuration(intervalRaw)
	if err != nil {
		return ResolvedWindowConfig{}, fmt.Errorf("%s is invalid: %w", cfg.field("interval"), err)
	}
	window, err := NewWindow(startClock, endClock, interval)
	if err != nil {
		return ResolvedWindowConfig{}, fmt.Errorf("%s is invalid: %w", cfg.field("window"), err)
	}

	return ResolvedWindowConfig{
		Window:      window,
		StartAtRaw:  startRaw,
		EndAtRaw:    endRaw,
		IntervalRaw: intervalRaw,
	}, nil
}

// SlotTime 返回窗口开始时间
func (w Window) SlotTime(day time.Time) time.Time {
	localDay := day.In(time.Local)
	return time.Date(localDay.Year(), localDay.Month(), localDay.Day(), w.StartAt.Hour, w.StartAt.Minute, 0, 0, time.Local)
}

// EndTime 返回窗口结束时间
func (w Window) EndTime(day time.Time) time.Time {
	localDay := day.In(time.Local)
	return time.Date(localDay.Year(), localDay.Month(), localDay.Day(), w.EndAt.Hour, w.EndAt.Minute, 0, 0, time.Local)
}

// LatestEligibleSlot 返回最新可用的 slot
func (w Window) LatestEligibleSlot(now, day time.Time) (time.Time, bool) {
	start := w.SlotTime(day)
	end := w.EndTime(day)
	limit := now.In(time.Local)
	if limit.After(end) {
		limit = end
	}
	if limit.Before(start) {
		return time.Time{}, false
	}
	elapsed := limit.Sub(start)
	steps := int(elapsed / w.Interval)
	slot := start.Add(time.Duration(steps) * w.Interval)
	if slot.After(end) {
		slot = end
	}
	return slot, true
}

// NewInterval 创建轮询间隔
func NewInterval(idleInterval, activeInterval, defaultIdle, defaultActive time.Duration) Interval {
	if idleInterval <= 0 {
		idleInterval = defaultIdle
	}
	if activeInterval <= 0 {
		activeInterval = defaultActive
	}
	return Interval{
		IdleInterval:   idleInterval,
		ActiveInterval: activeInterval,
	}
}

// NextDelay 返回下一轮轮询间隔
func (i Interval) NextDelay(active bool) time.Duration {
	if active {
		return i.ActiveInterval
	}
	return i.IdleInterval
}

// Wait 等待指定时间
func Wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c WindowConfig) field(name string) string {
	if strings.TrimSpace(c.FieldPrefix) == "" {
		return name
	}
	return strings.TrimSpace(c.FieldPrefix) + "." + name
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
