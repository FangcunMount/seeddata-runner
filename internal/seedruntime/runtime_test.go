package seedruntime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	"github.com/FangcunMount/seeddata-runner/internal/seedconfig"
)

func TestLoadDependenciesRequiresCollectionBaseURL(t *testing.T) {
	cfg := &seedconfig.Config{
		API: seedconfig.APIConfig{BaseURL: "https://api.example.com", Token: "test-token"},
	}
	_, err := LoadDependencies(context.Background(), cfg, log.New(log.NewOptions()))
	if err == nil || !strings.Contains(err.Error(), "api.collectionBaseUrl is required") {
		t.Fatalf("LoadDependencies error=%v, want collection base URL validation", err)
	}
}

func TestNormalizePlanWorkers(t *testing.T) {
	tests := []struct {
		workers   int
		taskCount int
		expected  int
	}{
		{workers: 0, taskCount: 0, expected: 1},
		{workers: 0, taskCount: 5, expected: 1},
		{workers: 8, taskCount: 3, expected: 3},
		{workers: 4, taskCount: 10, expected: 4},
	}

	for _, tt := range tests {
		if got := NormalizePlanWorkers(tt.workers, tt.taskCount); got != tt.expected {
			t.Fatalf("NormalizePlanWorkers(%d, %d)=%d, want=%d", tt.workers, tt.taskCount, got, tt.expected)
		}
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
