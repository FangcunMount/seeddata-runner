package seedruntime

import (
	"path/filepath"
	"testing"
	"time"
)

type testJSONState struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updatedAt"`
	Count     int       `json:"count"`
}

func TestJSONStateFileLoadMissingFile(t *testing.T) {
	store := NewJSONStateFile[testJSONState](filepath.Join(t.TempDir(), "missing", "state.json"))

	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if state.Name != "" || !state.UpdatedAt.IsZero() || state.Count != 0 {
		t.Fatalf("expected zero-value state, got %#v", state)
	}
}

func TestJSONStateFileSaveAndLoad(t *testing.T) {
	store := NewJSONStateFile[testJSONState](filepath.Join(t.TempDir(), "cache", "state.json"))
	want := &testJSONState{
		Name:      "daily-sim",
		UpdatedAt: time.Date(2026, 4, 20, 10, 30, 0, 0, time.Local),
		Count:     12,
	}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.Name != want.Name || !got.UpdatedAt.Equal(want.UpdatedAt) || got.Count != want.Count {
		t.Fatalf("loaded state mismatch: got %#v want %#v", got, want)
	}
}
