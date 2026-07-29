package seedprofile

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
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

func TestGenerateMakesFatherAndChildShareSurname(t *testing.T) {
	generator := New("+86199", "fangcunmount.com")
	for _, runDate := range []time.Time{
		time.Date(2025, time.December, 31, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.April, 20, 0, 0, 0, 0, time.Local),
	} {
		for idx := 0; idx < namePermutationDailyCapacity; idx += 2 {
			profile := generator.Generate(runDate, idx)
			if got, want := surnameOf(profile.ChildName), surnameOf(profile.GuardianName); got != want {
				t.Fatalf("father and child should share surname for %s idx=%d: guardian=%q child=%q", runDate.Format("2006-01-02"), idx, profile.GuardianName, profile.ChildName)
			}
		}
	}
}

func TestGeneratePreservesLegacyGenderAndDOBStream(t *testing.T) {
	generator := New("+86199", "fangcunmount.com")
	for _, runDate := range []time.Time{
		time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.April, 20, 0, 0, 0, 0, time.Local),
	} {
		for _, idx := range []int{0, 1, 7, 63, 299} {
			profile := generator.Generate(runDate, idx)
			rng := rand.New(rand.NewSource(int64(newSeed(fmt.Sprintf("profile:%s:%d", runDate.Format("20060102"), idx)))))
			wantGender := uint8(1)
			if rng.Intn(2) == 1 {
				wantGender = 2
			}
			for _, candidateCount := range []int{30, 20, 30, 20} {
				_ = rng.Intn(candidateCount)
			}
			wantDOB := generateChildDOB(rng, runDate)
			if profile.ChildGender != wantGender || profile.ChildDOB != wantDOB {
				t.Fatalf("non-name profile stream changed for %s idx=%d: got gender=%d dob=%s want gender=%d dob=%s", runDate.Format("2006-01-02"), idx, profile.ChildGender, profile.ChildDOB, wantGender, wantDOB)
			}
		}
	}
}

func TestGenerateAvoidsDuplicateNamesWithinDailyMaximum(t *testing.T) {
	generator := New("+86199", "fangcunmount.com")
	runDate := time.Date(2026, time.April, 20, 0, 0, 0, 0, time.UTC)
	guardianNames := make(map[string]int, 1000)
	childNames := make(map[string]int, 1000)

	for idx := range 1000 {
		profile := generator.Generate(runDate, idx)
		assertUniqueName(t, guardianNames, profile.GuardianName, idx)
		assertUniqueName(t, childNames, profile.ChildName, idx)
		assertChineseFullName(t, profile.GuardianName)
		assertChineseFullName(t, profile.ChildName)

		guardianNamesForGender := defaultChineseNameCorpus.adultMale
		if idx%2 == 1 {
			guardianNamesForGender = defaultChineseNameCorpus.adultFemale
		}
		assertNameUsesCandidates(t, profile.GuardianName, guardianNamesForGender)
		childNamesForGender := defaultChineseNameCorpus.childMale
		if profile.ChildGender == 2 {
			childNamesForGender = defaultChineseNameCorpus.childFemale
		}
		assertNameUsesCandidates(t, profile.ChildName, childNamesForGender)
	}
}

func TestGenerateAvoidsNameCollisionsAcrossFourHundredDays(t *testing.T) {
	generator := New("+86199", "fangcunmount.com")
	startDate := time.Date(2024, time.July, 1, 0, 0, 0, 0, time.UTC)
	guardianNames := make(map[string]struct{}, nameNoRepeatWindowDays*namePermutationDailyCapacity)
	childNames := make(map[string]struct{}, nameNoRepeatWindowDays*namePermutationDailyCapacity)
	allNames := make(map[string]struct{}, nameNoRepeatWindowDays*namePermutationDailyCapacity*2)
	guardianDuplicates := 0
	childDuplicates := 0
	allDuplicates := 0
	combinedCollisionNames := make([]string, 0)

	for dayOffset := range nameNoRepeatWindowDays {
		runDate := startDate.AddDate(0, 0, dayOffset)
		for idx := range namePermutationDailyCapacity {
			profile := generator.Generate(runDate, idx)
			if _, exists := guardianNames[profile.GuardianName]; exists {
				guardianDuplicates++
			} else {
				guardianNames[profile.GuardianName] = struct{}{}
			}
			if _, exists := allNames[profile.GuardianName]; exists {
				allDuplicates++
				if len(combinedCollisionNames) < 20 {
					combinedCollisionNames = append(combinedCollisionNames, profile.GuardianName)
				}
			} else {
				allNames[profile.GuardianName] = struct{}{}
			}
			if _, exists := childNames[profile.ChildName]; exists {
				childDuplicates++
			} else {
				childNames[profile.ChildName] = struct{}{}
			}
			if _, exists := allNames[profile.ChildName]; exists {
				allDuplicates++
				if len(combinedCollisionNames) < 20 {
					combinedCollisionNames = append(combinedCollisionNames, profile.ChildName)
				}
			} else {
				allNames[profile.ChildName] = struct{}{}
			}
		}
	}

	if guardianDuplicates != 0 || childDuplicates != 0 || allDuplicates != 0 {
		t.Fatalf("400-day name collisions: guardian=%d child=%d combined=%d names=%v", guardianDuplicates, childDuplicates, allDuplicates, combinedCollisionNames)
	}
}

func TestGenerateIsStableUnderConcurrency(t *testing.T) {
	generator := New("+86199", "fangcunmount.com")
	runDate := time.Date(2026, time.April, 20, 0, 0, 0, 0, time.UTC)
	want := make([]Profile, 300)
	for idx := range want {
		want[idx] = generator.Generate(runDate, idx)
	}

	got := make([]Profile, len(want))
	var wg sync.WaitGroup
	for idx := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[idx] = generator.Generate(runDate, idx)
		}()
	}
	wg.Wait()

	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("concurrent generation changed idx=%d: got=%+v want=%+v", idx, got[idx], want[idx])
		}
	}
}

func assertUniqueName(t *testing.T, seen map[string]int, name string, idx int) {
	t.Helper()
	if previous, ok := seen[name]; ok {
		t.Fatalf("duplicate name %q at indexes %d and %d", name, previous, idx)
	}
	seen[name] = idx
}

func assertChineseFullName(t *testing.T, name string) {
	t.Helper()
	if runeCount := utf8.RuneCountInString(name); runeCount < 2 || runeCount > 3 {
		t.Fatalf("Chinese full name should contain 2-3 runes, got %q (%d)", name, runeCount)
	}
	for _, r := range name {
		if !unicode.Is(unicode.Han, r) {
			t.Fatalf("Chinese full name should contain Han runes only, got %q with %s", name, fmt.Sprintf("%U", r))
		}
	}
}

func assertNameUsesCandidates(t *testing.T, fullName string, candidates givenNameCandidates) {
	t.Helper()
	runes := []rune(fullName)
	if len(runes) < 2 {
		t.Fatalf("full name is too short: %q", fullName)
	}
	givenName := string(runes[1:])
	values := candidates.single
	if len(runes) == 3 {
		values = candidates.double
	}
	for _, candidate := range values {
		if givenName == candidate {
			return
		}
	}
	t.Fatalf("given name %q from %q is not in the expected gender corpus", givenName, fullName)
}

func surnameOf(fullName string) string {
	for _, candidate := range fullName {
		return string(candidate)
	}
	return ""
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
