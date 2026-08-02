package dailysim

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func resolveDailySimulationRoutesForRun(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
	runDate time.Time,
) ([]dailySimulationRoute, error) {
	if !cfg.EntryID.IsZero() {
		entry, target, clinicianID, err := ensureDailySimulationEntryAndTarget(ctx, deps, cfg)
		if err != nil {
			return nil, err
		}
		return []dailySimulationRoute{{
			ClinicianID: clinicianID,
			Entry:       entry,
			Target:      target,
		}}, nil
	}

	selectedClinicianIDs, err := selectDailySimulationClinicianIDsForRun(collectDailySimulationClinicianIDs(cfg.ClinicianIDs), cfg, runDate)
	if err != nil {
		return nil, err
	}

	routes := make([]dailySimulationRoute, 0, len(selectedClinicianIDs))
	for _, clinicianID := range selectedClinicianIDs {
		routeCfg := cfg
		routeCfg.EntryID = FlexibleID("")
		routeCfg.ClinicianIDs = []FlexibleID{FlexibleID(clinicianID)}

		entry, target, resolvedClinicianID, err := ensureDailySimulationEntryAndTarget(ctx, deps, routeCfg)
		if err != nil {
			return nil, err
		}
		routes = append(routes, dailySimulationRoute{
			ClinicianID: resolvedClinicianID,
			Entry:       entry,
			Target:      target,
		})
	}
	return routes, nil
}

func resolveDailySimulationAdditionalTargetsForRun(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
) ([]*dailySimulationResolvedTarget, error) {
	codes := collectDailySimulationAdditionalTargetCodes(cfg)
	if len(codes) == 0 || cfg.AdditionalTargetMaxCount <= 0 {
		return nil, nil
	}
	targets := make([]*dailySimulationResolvedTarget, 0, len(codes))
	for _, code := range codes {
		target, err := resolveDailySimulationTarget(ctx, deps.APIClient, deps.CollectionClient, cfg.TargetType, code, cfg.TargetVersion)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func selectDailySimulationAdditionalTargetsForTestee(
	targets []*dailySimulationResolvedTarget,
	cfg DailySimulationConfig,
	runDate time.Time,
	index int,
) []*dailySimulationResolvedTarget {
	if len(targets) == 0 || cfg.AdditionalTargetMaxCount <= 0 {
		return nil
	}
	rng := newDailySimulationRand(fmt.Sprintf(
		"additional-targets:%s:%d",
		runDate.Format("20060102"),
		index,
	))
	maxCount := cfg.AdditionalTargetMaxCount
	if maxCount > len(targets) {
		maxCount = len(targets)
	}
	count := 1 + rng.Intn(maxCount)
	if count >= len(targets) {
		return append([]*dailySimulationResolvedTarget(nil), targets...)
	}
	order := rng.Perm(len(targets))
	selected := make([]*dailySimulationResolvedTarget, 0, count)
	for _, targetIndex := range order[:count] {
		selected = append(selected, targets[targetIndex])
	}
	return selected
}

func dailySimulationResolvedTargetCodes(targets []*dailySimulationResolvedTarget) []string {
	codes := make([]string, 0, len(targets))
	for _, target := range targets {
		if target == nil {
			continue
		}
		if code := strings.TrimSpace(target.TargetCode); code != "" {
			codes = append(codes, code)
		}
	}
	return codes
}

// selectDailySimulationClinicianIDsForRun 选择每日模拟用户临床ID
func selectDailySimulationClinicianIDsForRun(
	clinicianIDs []string,
	cfg DailySimulationConfig,
	runDate time.Time,
) ([]string, error) {
	if len(clinicianIDs) == 0 {
		return nil, fmt.Errorf("dailySimulation clinician scope resolved zero clinicians")
	}
	if len(clinicianIDs) == 1 {
		return clinicianIDs, nil
	}

	minCount := cfg.FocusCliniciansPerRunMin
	maxCount := cfg.FocusCliniciansPerRunMax
	if minCount <= 0 && maxCount <= 0 {
		minCount = len(clinicianIDs)
		maxCount = len(clinicianIDs)
	}
	if minCount <= 0 {
		minCount = 1
	}
	if maxCount <= 0 {
		maxCount = minCount
	}
	if maxCount < minCount {
		return nil, fmt.Errorf("dailySimulation focusCliniciansPerRunMax must be >= focusCliniciansPerRunMin")
	}
	if minCount > len(clinicianIDs) {
		minCount = len(clinicianIDs)
	}
	if maxCount > len(clinicianIDs) {
		maxCount = len(clinicianIDs)
	}

	selectedCount := minCount
	if maxCount > minCount {
		rng := newDailySimulationRand("focus-clinicians:" + runDate.Format("20060102"))
		selectedCount = minCount + rng.Intn(maxCount-minCount+1)
	}

	rng := newDailySimulationRand("focus-clinician-order:" + runDate.Format("20060102"))
	order := rng.Perm(len(clinicianIDs))
	selected := make([]string, 0, selectedCount)
	for _, idx := range order[:selectedCount] {
		selected = append(selected, clinicianIDs[idx])
	}
	return selected, nil
}

// collectDailySimulationClinicianIDs 收集每日模拟用户临床ID
func collectDailySimulationClinicianIDs(ids []FlexibleID) []string {
	collected := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		value := strings.TrimSpace(id.String())
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		collected = append(collected, value)
	}
	return collected
}

// collectDailySimulationAdditionalTargetCodes 收集每日模拟额外填报量表编码。
func collectDailySimulationAdditionalTargetCodes(cfg DailySimulationConfig) []string {
	collected := make([]string, 0, len(cfg.AdditionalTargetCodes))
	seen := make(map[string]struct{}, len(cfg.AdditionalTargetCodes))
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		collected = append(collected, value)
	}
	for _, code := range cfg.AdditionalTargetCodes {
		add(code)
	}
	return collected
}

// collectDailySimulationPlanIDs 收集每日模拟用户计划ID
func collectDailySimulationPlanIDs(ids []FlexibleID) []string {
	collected := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		value := strings.TrimSpace(id.String())
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		collected = append(collected, value)
	}
	return collected
}

// resolveDailySimulationSchedule 解析每日模拟用户调度配置
