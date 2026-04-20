package seedprofile

import (
	"testing"
	"time"
	"unicode"
)

func TestGenerateUsesChineseNamesAndTeenageDOB(t *testing.T) {
	generator := New("+86199", "fangcunmount.com")
	runDate := time.Date(2026, time.April, 20, 0, 0, 0, 0, time.UTC)

	for idx := range 64 {
		profile := generator.Generate(runDate, idx)
		if !containsHan(profile.GuardianName) {
			t.Fatalf("guardian name should contain Han characters, got %q", profile.GuardianName)
		}
		if !containsHan(profile.ChildName) {
			t.Fatalf("child name should contain Han characters, got %q", profile.ChildName)
		}

		dob, err := time.Parse("2006-01-02", profile.ChildDOB)
		if err != nil {
			t.Fatalf("parse child dob %q: %v", profile.ChildDOB, err)
		}
		age := ageAt(runDate, dob)
		if age < 8 || age > 18 {
			t.Fatalf("child age should be within 8-18, got %d for dob %s", age, profile.ChildDOB)
		}
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	generator := New("+86199", "fangcunmount.com")
	runDate := time.Date(2026, time.April, 20, 0, 0, 0, 0, time.UTC)

	first := generator.Generate(runDate, 7)
	second := generator.Generate(runDate, 7)

	if first != second {
		t.Fatalf("expected deterministic generation, first=%+v second=%+v", first, second)
	}
}

func containsHan(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func ageAt(now, dob time.Time) int {
	age := now.Year() - dob.Year()
	birthdayThisYear := time.Date(now.Year(), dob.Month(), dob.Day(), 0, 0, 0, 0, now.Location())
	if now.Before(birthdayThisYear) {
		age--
	}
	return age
}
