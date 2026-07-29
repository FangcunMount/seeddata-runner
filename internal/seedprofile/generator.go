package seedprofile

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"strings"
	"time"

	"github.com/mozillazg/go-pinyin"
)

type Generator struct {
	phonePrefix string
	emailDomain string
}

type Profile struct {
	Index         int
	RunDate       time.Time
	GuardianName  string
	GuardianPhone string
	GuardianEmail string
	ChildName     string
	ChildDOB      string
	ChildGender   uint8
}

func New(phonePrefix, emailDomain string) *Generator {
	return &Generator{
		phonePrefix: strings.TrimSpace(phonePrefix),
		emailDomain: strings.TrimPrefix(strings.ToLower(strings.TrimSpace(emailDomain)), "@"),
	}
}

func (g *Generator) Generate(runDate time.Time, idx int) Profile {
	rng := rand.New(rand.NewSource(int64(newSeed(fmt.Sprintf("profile:%s:%d", runDate.Format("20060102"), idx)))))

	guardianGender := uint8(1)
	if idx%2 == 1 {
		guardianGender = 2
	}
	childGender := uint8(1)
	if rng.Intn(2) == 1 {
		childGender = 2
	}

	guardianName := generateChineseName(runDate, idx, guardianGender, true)
	fatherName := ""
	if guardianGender == 1 {
		fatherName = guardianName
	}
	childName := generateChineseChildName(runDate, idx, childGender, fatherName)

	phoneSuffix := fmt.Sprintf("%02d%02d%04d", int(runDate.Month()), runDate.Day(), idx+1)
	phone := strings.TrimSpace(g.phonePrefix) + phoneSuffix

	emailLocal := normalizedEmailLocal(guardianName)
	if emailLocal == "" {
		emailLocal = fmt.Sprintf("dailyguardian%04d", idx+1)
	}
	emailDomain := g.emailDomain
	if emailDomain == "" {
		emailDomain = "fangcunmount.com"
	}
	email := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%s_%s_%04d@%s", emailLocal, runDate.Format("20060102"), idx+1, emailDomain)))

	// Keep the non-name profile stream stable across the name-generator migration.
	// The legacy implementation consumed four draws before generating the DOB.
	advanceLegacyNameRandomness(rng)
	childDOB := generateChildDOB(rng, runDate)

	return Profile{
		Index:         idx + 1,
		RunDate:       runDate,
		GuardianName:  guardianName,
		GuardianPhone: phone,
		GuardianEmail: email,
		ChildName:     childName,
		ChildDOB:      childDOB,
		ChildGender:   childGender,
	}
}

func advanceLegacyNameRandomness(rng *rand.Rand) {
	if rng == nil {
		return
	}
	for _, candidateCount := range []int{30, 20, 30, 20} {
		_ = rng.Intn(candidateCount)
	}
}

func generateChildDOB(rng *rand.Rand, runDate time.Time) string {
	oldestDOB := runDate.AddDate(-18, 0, 0)
	youngestDOB := runDate.AddDate(-8, 0, 0)
	if youngestDOB.Before(oldestDOB) {
		oldestDOB, youngestDOB = youngestDOB, oldestDOB
	}
	daySpan := int(youngestDOB.Sub(oldestDOB).Hours() / 24)
	if daySpan < 0 {
		daySpan = 0
	}
	offsetDays := 0
	if rng != nil && daySpan > 0 {
		offsetDays = rng.Intn(daySpan + 1)
	}
	return oldestDOB.AddDate(0, 0, offsetDays).Format("2006-01-02")
}

func normalizedEmailLocal(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	args := pinyin.NewArgs()
	args.Style = pinyin.Normal
	parts := pinyin.LazyPinyin(name, args)
	if len(parts) == 0 {
		parts = []string{name}
	}

	var builder strings.Builder
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		for _, r := range part {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				builder.WriteRune(r)
			}
		}
	}
	return builder.String()
}

func newSeed(seed string) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(seed))
	return hash.Sum64()
}
