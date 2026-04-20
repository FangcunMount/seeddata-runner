package dailysim

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDailySimulationStateStoreLoadMissingFile(t *testing.T) {
	store := newDailySimulationStateStore(filepath.Join(t.TempDir(), "missing", "state.json"))

	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if state.LastSuccessDate != "" || !state.LastSuccessAt.IsZero() || !state.LastCompletedSlot.IsZero() || state.DailyUserCountDate != "" || state.DailyUserCount != 0 {
		t.Fatalf("expected zero-value state, got %#v", state)
	}
}

func TestDailySimulationStateStoreSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "state.json")
	store := newDailySimulationStateStore(path)
	want := &dailySimulationDaemonState{
		LastSuccessDate:    "2026-04-20",
		LastSuccessAt:      time.Date(2026, 4, 20, 10, 30, 0, 0, time.Local),
		LastCompletedSlot:  time.Date(2026, 4, 20, 10, 0, 0, 0, time.Local),
		DailyUserCountDate: "2026-04-20",
		DailyUserCount:     18,
	}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.LastSuccessDate != want.LastSuccessDate ||
		!got.LastSuccessAt.Equal(want.LastSuccessAt) ||
		!got.LastCompletedSlot.Equal(want.LastCompletedSlot) ||
		got.DailyUserCountDate != want.DailyUserCountDate ||
		got.DailyUserCount != want.DailyUserCount {
		t.Fatalf("loaded state mismatch: got %#v want %#v", got, want)
	}
}
