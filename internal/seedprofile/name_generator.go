package seedprofile

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// Each day owns a disjoint ordinal range inside the stable permutation.
	// Keep this aligned with the configured daily simulation maximum.
	namePermutationDailyCapacity = 300
	nameNoRepeatWindowDays       = 400
)

//go:embed chinese_names.json
var embeddedChineseNameData []byte

var defaultChineseNameCorpus = mustLoadChineseNameCorpus(embeddedChineseNameData)

type rawChineseNameData struct {
	Version     int               `json:"version"`
	Description string            `json:"description"`
	Surnames    []weightedSurname `json:"surnames"`
	Adult       rawRoleNameData   `json:"adult"`
	Child       rawRoleNameData   `json:"child"`
}

type rawRoleNameData struct {
	Male   rawGivenNameData `json:"male"`
	Female rawGivenNameData `json:"female"`
}

type rawGivenNameData struct {
	Single string `json:"single"`
	First  string `json:"first"`
	Second string `json:"second"`
}

type weightedSurname struct {
	Value  string `json:"value"`
	Weight int    `json:"weight"`
}

type chineseNameCorpus struct {
	version             int
	surnames            []weightedSurname
	adultMale           givenNameCandidates
	adultFemale         givenNameCandidates
	childMale           givenNameCandidates
	childFemale         givenNameCandidates
	childMalePaternal   []string
	childFemalePaternal []string
	fatherNameOrdinal   map[string]int
	adultMaleSpace      []string
	adultFemaleSpace    []string
	childMaleSpace      []string
	childFemaleSpace    []string
}

type givenNameCandidates struct {
	single []string
	double []string
}

func generateChineseName(runDate time.Time, idx int, gender uint8, adult bool) string {
	return defaultChineseNameCorpus.generate(runDate, idx, gender, adult)
}

func generateChineseChildName(runDate time.Time, idx int, gender uint8, fatherName string) string {
	return defaultChineseNameCorpus.generateChild(runDate, idx, gender, fatherName)
}

func (c chineseNameCorpus) generate(runDate time.Time, idx int, gender uint8, adult bool) string {
	if idx < 0 {
		idx = 0
	}
	role := "child"
	nameSpace := c.childMaleSpace
	if adult {
		role = "adult"
		nameSpace = c.adultMaleSpace
	}
	genderLabel := "male"
	if gender == 2 {
		genderLabel = "female"
		if adult {
			nameSpace = c.adultFemaleSpace
		} else {
			nameSpace = c.childFemaleSpace
		}
	}

	// A stable affine permutation gives every accepted ordinal a unique
	// name without mutable process state, so retries and concurrent workers agree.
	ordinal := civilDayNumber(runDate)*namePermutationDailyCapacity + idx
	seed := newSeed(fmt.Sprintf("chinese-name:v%d:%s:%s", c.version, role, genderLabel))
	return nameSpace[permutedIndex(ordinal, len(nameSpace), seed)]
}

func (c chineseNameCorpus) generateChild(runDate time.Time, idx int, gender uint8, fatherName string) string {
	if fatherName == "" {
		return c.generate(runDate, idx, gender, false)
	}

	ordinal, exists := c.fatherNameOrdinal[fatherName]
	if !exists {
		panic(fmt.Sprintf("generated father name %q is missing from the name corpus", fatherName))
	}
	surname := firstRune(fatherName)
	candidates := c.childMalePaternal
	genderLabel := "male"
	if gender == 2 {
		candidates = c.childFemalePaternal
		genderLabel = "female"
	}
	seed := newSeed(fmt.Sprintf("chinese-paternal-name:v%d:%s:%s", c.version, genderLabel, surname))
	return surname + candidates[permutedIndex(ordinal, len(candidates), seed)]
}

func civilDayNumber(value time.Time) int {
	date := time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
	return int(date.Unix() / int64(24*time.Hour/time.Second))
}

func permutedIndex(ordinal, size int, seed uint64) int {
	if size <= 1 {
		return 0
	}
	offset := int(seed % uint64(size))
	step := int((seed/uint64(size))%uint64(size-1)) + 1
	for greatestCommonDivisor(step, size) != 1 {
		step++
		if step >= size {
			step = 1
		}
	}
	normalizedOrdinal := ordinal % size
	if normalizedOrdinal < 0 {
		normalizedOrdinal += size
	}
	return int((uint64(normalizedOrdinal)*uint64(step) + uint64(offset)) % uint64(size))
}

func greatestCommonDivisor(left, right int) int {
	for right != 0 {
		left, right = right, left%right
	}
	return left
}

func mustLoadChineseNameCorpus(data []byte) chineseNameCorpus {
	corpus, err := loadChineseNameCorpus(data)
	if err != nil {
		panic(fmt.Sprintf("load embedded Chinese name corpus: %v", err))
	}
	return corpus
}

func loadChineseNameCorpus(data []byte) (chineseNameCorpus, error) {
	var raw rawChineseNameData
	if err := json.Unmarshal(data, &raw); err != nil {
		return chineseNameCorpus{}, fmt.Errorf("decode JSON: %w", err)
	}
	if raw.Version <= 0 {
		return chineseNameCorpus{}, fmt.Errorf("version must be positive")
	}

	corpus := chineseNameCorpus{version: raw.Version}
	seenSurnames := make(map[string]struct{}, len(raw.Surnames))
	for _, surname := range raw.Surnames {
		surname.Value = strings.TrimSpace(surname.Value)
		if utf8.RuneCountInString(surname.Value) != 1 || !allHan(surname.Value) {
			return chineseNameCorpus{}, fmt.Errorf("surname %q must contain exactly one Han rune", surname.Value)
		}
		if surname.Weight <= 0 {
			return chineseNameCorpus{}, fmt.Errorf("surname %q must have positive weight", surname.Value)
		}
		if _, exists := seenSurnames[surname.Value]; exists {
			return chineseNameCorpus{}, fmt.Errorf("duplicate surname %q", surname.Value)
		}
		seenSurnames[surname.Value] = struct{}{}
		corpus.surnames = append(corpus.surnames, surname)
	}
	if len(corpus.surnames) == 0 {
		return chineseNameCorpus{}, fmt.Errorf("at least one surname is required")
	}

	var err error
	if corpus.adultMale, err = buildGivenNameCandidates(raw.Adult.Male); err != nil {
		return chineseNameCorpus{}, fmt.Errorf("adult male names: %w", err)
	}
	if corpus.adultFemale, err = buildGivenNameCandidates(raw.Adult.Female); err != nil {
		return chineseNameCorpus{}, fmt.Errorf("adult female names: %w", err)
	}
	if corpus.childMale, err = buildGivenNameCandidates(raw.Child.Male); err != nil {
		return chineseNameCorpus{}, fmt.Errorf("child male names: %w", err)
	}
	if corpus.childFemale, err = buildGivenNameCandidates(raw.Child.Female); err != nil {
		return chineseNameCorpus{}, fmt.Errorf("child female names: %w", err)
	}
	if err := ensureDisjointNames("adult", corpus.adultMale, corpus.adultFemale); err != nil {
		return chineseNameCorpus{}, err
	}
	if err := ensureDisjointNames("child", corpus.childMale, corpus.childFemale); err != nil {
		return chineseNameCorpus{}, err
	}
	if err := ensureDisjointNameGroups(
		"adult and child",
		[][]string{givenNameValues(corpus.adultMale), givenNameValues(corpus.adultFemale)},
		[][]string{givenNameValues(corpus.childMale), givenNameValues(corpus.childFemale)},
	); err != nil {
		return chineseNameCorpus{}, err
	}
	if corpus.adultMaleSpace, err = buildWeightedNameSpace(corpus.version, "adult-male", corpus.surnames, corpus.adultMale); err != nil {
		return chineseNameCorpus{}, err
	}
	if corpus.adultFemaleSpace, err = buildWeightedNameSpace(corpus.version, "adult-female", corpus.surnames, corpus.adultFemale); err != nil {
		return chineseNameCorpus{}, err
	}
	fatherNameOrdinal, maximumFatherNamesPerSurname := buildSurnameOrdinals(corpus.adultMaleSpace)
	corpus.fatherNameOrdinal = fatherNameOrdinal
	// Paternal and independent child names must stay disjoint. Otherwise forcing
	// the father's surname would collapse independently generated full names.
	childMalePaternal, childMaleIndependent := partitionGivenNames(corpus.version, "child-male", corpus.childMale)
	childFemalePaternal, childFemaleIndependent := partitionGivenNames(corpus.version, "child-female", corpus.childFemale)
	corpus.childMalePaternal = childMalePaternal
	corpus.childFemalePaternal = childFemalePaternal
	if len(corpus.childMalePaternal) < maximumFatherNamesPerSurname {
		return chineseNameCorpus{}, fmt.Errorf("child male paternal candidates have %d entries; at least %d are required", len(corpus.childMalePaternal), maximumFatherNamesPerSurname)
	}
	if len(corpus.childFemalePaternal) < maximumFatherNamesPerSurname {
		return chineseNameCorpus{}, fmt.Errorf("child female paternal candidates have %d entries; at least %d are required", len(corpus.childFemalePaternal), maximumFatherNamesPerSurname)
	}
	if corpus.childMaleSpace, err = buildWeightedNameSpaceFromValues(corpus.version, "child-male-independent", corpus.surnames, childMaleIndependent); err != nil {
		return chineseNameCorpus{}, err
	}
	if corpus.childFemaleSpace, err = buildWeightedNameSpaceFromValues(corpus.version, "child-female-independent", corpus.surnames, childFemaleIndependent); err != nil {
		return chineseNameCorpus{}, err
	}
	if err := ensureDisjointNameSpaces(
		"adult and child",
		[][]string{corpus.adultMaleSpace, corpus.adultFemaleSpace},
		[][]string{corpus.childMaleSpace, corpus.childFemaleSpace},
	); err != nil {
		return chineseNameCorpus{}, err
	}
	return corpus, nil
}

func buildWeightedNameSpace(version int, label string, surnames []weightedSurname, candidates givenNameCandidates) ([]string, error) {
	return buildWeightedNameSpaceFromValues(version, label, surnames, givenNameValues(candidates))
}

func buildWeightedNameSpaceFromValues(version int, label string, surnames []weightedSurname, givenNames []string) ([]string, error) {
	maximumWeight := 0
	for _, surname := range surnames {
		if surname.Weight > maximumWeight {
			maximumWeight = surname.Weight
		}
	}
	if maximumWeight <= 0 || len(givenNames) == 0 {
		return nil, fmt.Errorf("%s name space has no capacity", label)
	}

	nameSpace := make([]string, 0, len(surnames)*len(givenNames))
	for _, surname := range surnames {
		// Common surnames receive a larger non-repeating slice of the given-name
		// space; rare surnames remain represented without duplicating full names.
		candidateLimit := len(givenNames) * surname.Weight / maximumWeight
		if candidateLimit == 0 {
			candidateLimit = 1
		}
		candidateSeed := newSeed(fmt.Sprintf("chinese-name-space:v%d:%s:%s", version, label, surname.Value))
		for ordinal := range candidateLimit {
			candidate := givenNames[permutedIndex(ordinal, len(givenNames), candidateSeed)]
			nameSpace = append(nameSpace, surname.Value+candidate)
		}
	}
	minimumCapacity := nameNoRepeatWindowDays * namePermutationDailyCapacity
	if len(nameSpace) < minimumCapacity {
		return nil, fmt.Errorf("%s name space has %d entries; at least %d are required", label, len(nameSpace), minimumCapacity)
	}
	return nameSpace, nil
}

func givenNameValues(candidates givenNameCandidates) []string {
	return append(append([]string(nil), candidates.single...), candidates.double...)
}

func partitionGivenNames(version int, label string, candidates givenNameCandidates) ([]string, []string) {
	values := givenNameValues(candidates)
	seed := newSeed(fmt.Sprintf("chinese-name-partition:v%d:%s", version, label))
	paternal := make([]string, 0, (len(values)+1)/2)
	independent := make([]string, 0, len(values)/2)
	for ordinal := range len(values) {
		candidate := values[permutedIndex(ordinal, len(values), seed)]
		if ordinal%2 == 0 {
			paternal = append(paternal, candidate)
		} else {
			independent = append(independent, candidate)
		}
	}
	return paternal, independent
}

func buildSurnameOrdinals(names []string) (map[string]int, int) {
	ordinals := make(map[string]int, len(names))
	counts := make(map[string]int)
	maximum := 0
	for _, name := range names {
		surname := firstRune(name)
		ordinals[name] = counts[surname]
		counts[surname]++
		if counts[surname] > maximum {
			maximum = counts[surname]
		}
	}
	return ordinals, maximum
}

func buildGivenNameCandidates(raw rawGivenNameData) (givenNameCandidates, error) {
	singleRunes, err := uniqueHanRunes(raw.Single)
	if err != nil {
		return givenNameCandidates{}, fmt.Errorf("single candidates: %w", err)
	}
	firstRunes, err := uniqueHanRunes(raw.First)
	if err != nil {
		return givenNameCandidates{}, fmt.Errorf("first-position candidates: %w", err)
	}
	secondRunes, err := uniqueHanRunes(raw.Second)
	if err != nil {
		return givenNameCandidates{}, fmt.Errorf("second-position candidates: %w", err)
	}

	candidates := givenNameCandidates{single: make([]string, 0, len(singleRunes))}
	for _, candidate := range singleRunes {
		candidates.single = append(candidates.single, string(candidate))
	}
	for _, first := range firstRunes {
		for _, second := range secondRunes {
			if first == second {
				continue
			}
			candidates.double = append(candidates.double, string([]rune{first, second}))
		}
	}
	if len(candidates.single) == 0 || len(candidates.double) == 0 {
		return givenNameCandidates{}, fmt.Errorf("single and double candidates are required")
	}
	return candidates, nil
}

func uniqueHanRunes(value string) ([]rune, error) {
	value = strings.TrimSpace(value)
	seen := make(map[rune]struct{}, utf8.RuneCountInString(value))
	result := make([]rune, 0, utf8.RuneCountInString(value))
	for _, candidate := range value {
		if !unicode.Is(unicode.Han, candidate) {
			return nil, fmt.Errorf("candidate %U is not a Han rune", candidate)
		}
		if _, exists := seen[candidate]; exists {
			return nil, fmt.Errorf("duplicate candidate %q", candidate)
		}
		seen[candidate] = struct{}{}
		result = append(result, candidate)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("candidate set is empty")
	}
	return result, nil
}

func ensureDisjointNames(label string, male, female givenNameCandidates) error {
	maleNames := make(map[string]struct{}, len(male.single)+len(male.double))
	for _, name := range append(append([]string(nil), male.single...), male.double...) {
		maleNames[name] = struct{}{}
	}
	for _, name := range append(append([]string(nil), female.single...), female.double...) {
		if _, exists := maleNames[name]; exists {
			return fmt.Errorf("%s male and female corpus overlap on %q", label, name)
		}
	}
	return nil
}

func ensureDisjointNameSpaces(label string, leftGroups, rightGroups [][]string) error {
	return ensureDisjointNameGroups(label, leftGroups, rightGroups)
}

func ensureDisjointNameGroups(label string, leftGroups, rightGroups [][]string) error {
	leftNames := make(map[string]struct{})
	for _, group := range leftGroups {
		for _, name := range group {
			leftNames[name] = struct{}{}
		}
	}
	for _, group := range rightGroups {
		for _, name := range group {
			if _, exists := leftNames[name]; exists {
				return fmt.Errorf("%s name spaces overlap on %q", label, name)
			}
		}
	}
	return nil
}

func firstRune(value string) string {
	for _, candidate := range value {
		return string(candidate)
	}
	return ""
}

func allHan(value string) bool {
	for _, candidate := range value {
		if !unicode.Is(unicode.Han, candidate) {
			return false
		}
	}
	return value != ""
}
