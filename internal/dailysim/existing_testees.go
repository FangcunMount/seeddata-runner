package dailysim

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

func loadDailySimulationExistingTesteesByIndex(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
	runDate time.Time,
	count int,
) (map[int]*ApiserverTesteeResponse, error) {
	if !dailySimulationUsesIAMMockConsumer(deps.Config.IAM) || count <= 0 {
		return map[int]*ApiserverTesteeResponse{}, nil
	}
	items, err := listDailySimulationTesteesByOrg(ctx, deps.APIClient, deps.Config.Global.OrgID, runDate)
	if err != nil {
		return nil, err
	}
	return matchDailySimulationExistingTesteesByIndex(cfg, runDate, count, items), nil
}

func listDailySimulationTesteesByOrg(
	ctx context.Context,
	client *APIClient,
	orgID int64,
	runDate time.Time,
) ([]*ApiserverTesteeResponse, error) {
	if client == nil {
		return nil, fmt.Errorf("daily simulation api client is nil")
	}
	const pageSize = 100 // apiserver /api/v1/testees currently caps page_size at 100.
	page := 1
	items := make([]*ApiserverTesteeResponse, 0, pageSize)
	for {
		resp, err := client.ListTesteesByOrgCreatedOnDate(ctx, orgID, runDate, page, pageSize)
		if err != nil {
			return nil, fmt.Errorf("list testees by org %d page %d: %w", orgID, page, err)
		}
		items = append(items, resp.Items...)
		if len(resp.Items) == 0 || page >= resp.TotalPages {
			break
		}
		page++
	}
	return items, nil
}

func matchDailySimulationExistingTesteesByIndex(
	cfg DailySimulationConfig,
	runDate time.Time,
	count int,
	items []*ApiserverTesteeResponse,
) map[int]*ApiserverTesteeResponse {
	matched := make(map[int]*ApiserverTesteeResponse, count)
	if count <= 0 {
		return matched
	}

	indexesBySignature := make(map[string][]int, count)
	for idx := 0; idx < count; idx++ {
		profile := buildDailySimulationProfile(cfg, runDate, idx)
		signature := dailySimulationProfileSignature(profile)
		indexesBySignature[signature] = append(indexesBySignature[signature], profile.Index)
	}

	candidates := make([]*ApiserverTesteeResponse, 0, len(items))
	for _, item := range items {
		if !isDailySimulationExistingTesteeCandidate(item, cfg, runDate) {
			continue
		}
		candidates = append(candidates, item)
	}
	sort.Slice(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if left.CreatedAt.Equal(right.CreatedAt) {
			return strings.TrimSpace(left.ID) < strings.TrimSpace(right.ID)
		}
		return left.CreatedAt.Before(right.CreatedAt)
	})

	for _, item := range candidates {
		signature := dailySimulationExistingTesteeSignature(item)
		indexes := indexesBySignature[signature]
		if len(indexes) == 0 {
			continue
		}
		index := indexes[0]
		indexesBySignature[signature] = indexes[1:]
		matched[index] = item
	}
	return matched
}

func sortedDailySimulationExistingIndexes(items map[int]*ApiserverTesteeResponse) []int {
	if len(items) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(items))
	for index, item := range items {
		if item == nil {
			continue
		}
		indexes = append(indexes, index-1)
	}
	sort.Ints(indexes)
	return indexes
}

func isDailySimulationExistingTesteeCandidate(
	item *ApiserverTesteeResponse,
	cfg DailySimulationConfig,
	runDate time.Time,
) bool {
	if item == nil {
		return false
	}
	if strings.TrimSpace(item.Source) != normalizeDailySimulationSource(cfg.TesteeSource) {
		return false
	}
	createdAt := item.CreatedAt.In(time.Local)
	dayStart := time.Date(runDate.Year(), runDate.Month(), runDate.Day(), 0, 0, 0, 0, time.Local)
	dayEnd := dayStart.Add(24 * time.Hour)
	if createdAt.Before(dayStart) || !createdAt.Before(dayEnd) {
		return false
	}
	return containsAllDailySimulationTags(item.Tags, cfg.TesteeTags)
}

func containsAllDailySimulationTags(actual, expected []string) bool {
	if len(expected) == 0 {
		return true
	}
	actualSet := make(map[string]struct{}, len(actual))
	for _, tag := range actual {
		actualSet[strings.TrimSpace(tag)] = struct{}{}
	}
	for _, tag := range expected {
		if _, ok := actualSet[strings.TrimSpace(tag)]; !ok {
			return false
		}
	}
	return true
}

func dailySimulationProfileSignature(profile dailySimulationProfile) string {
	return strings.Join([]string{
		strings.TrimSpace(profile.ChildName),
		strings.TrimSpace(profile.ChildDOB),
		dailySimulationProfileGender(profile.ChildGender),
		normalizeDailySimulationGuardianPhone(profile.GuardianPhone),
	}, "|")
}

func dailySimulationExistingTesteeSignature(item *ApiserverTesteeResponse) string {
	birthday := ""
	if item != nil && item.Birthday != nil {
		birthday = item.Birthday.In(time.Local).Format("2006-01-02")
	}
	guardianPhone, ok := dailySimulationSingleGuardianPhone(item.Guardians)
	if !ok {
		return ""
	}
	return strings.Join([]string{
		strings.TrimSpace(item.Name),
		birthday,
		normalizeDailySimulationExistingTesteeGender(item.Gender),
		guardianPhone,
	}, "|")
}

func dailySimulationSingleGuardianPhone(guardians []GuardianResponse) (string, bool) {
	phones := make(map[string]struct{}, len(guardians))
	for _, guardian := range guardians {
		phone := normalizeDailySimulationGuardianPhone(guardian.Phone)
		if phone == "" {
			continue
		}
		phones[phone] = struct{}{}
	}
	if len(phones) != 1 {
		return "", false
	}
	for phone := range phones {
		return phone, true
	}
	return "", false
}

func normalizeDailySimulationGuardianPhone(value string) string {
	return strings.TrimSpace(value)
}

func dailySimulationProfileGender(value uint8) string {
	switch value {
	case 1:
		return "male"
	case 2:
		return "female"
	default:
		return "unknown"
	}
}

func normalizeDailySimulationExistingTesteeGender(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "male":
		return "male"
	case "2", "female":
		return "female"
	case "3", "unknown", "other":
		return "unknown"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

// resolveDailySimulationRoutesForRun 解析每日模拟用户入口场景。
