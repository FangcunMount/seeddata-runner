package dailysim

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func ensureDailySimulationEntryAndTarget(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
) (*AssessmentEntryResponse, *dailySimulationResolvedTarget, string, error) {
	var clinicianID string

	if !cfg.EntryID.IsZero() {
		entry, err := deps.APIClient.GetAssessmentEntry(ctx, cfg.EntryID.String())
		if err != nil {
			return nil, nil, "", fmt.Errorf("get daily simulation entry %s: %w", cfg.EntryID.String(), err)
		}
		if entry == nil {
			return nil, nil, "", fmt.Errorf("daily simulation entry %s not found", cfg.EntryID.String())
		}
		if !entry.IsActive {
			entry, err = deps.APIClient.ReactivateAssessmentEntry(ctx, entry.ID)
			if err != nil {
				return nil, nil, "", fmt.Errorf("reactivate daily simulation entry %s: %w", entry.ID, err)
			}
		}
		clinicianID = strings.TrimSpace(entry.ClinicianID)
		if clinicianID == "" {
			return nil, nil, "", fmt.Errorf("daily simulation entry %s has empty clinician_id", entry.ID)
		}
		target, err := resolveDailySimulationTarget(ctx, deps.APIClient, deps.CollectionClient, entry.TargetType, entry.TargetCode, entry.TargetVersion)
		if err != nil {
			return nil, nil, "", err
		}
		return entry, target, clinicianID, nil
	}

	clinicianIDs := collectDailySimulationClinicianIDs(cfg.ClinicianIDs)
	if len(clinicianIDs) == 0 {
		return nil, nil, "", fmt.Errorf("dailySimulation clinicianIds is required when entryId is not set")
	}
	clinicianID = clinicianIDs[0]

	targetType := strings.ToLower(strings.TrimSpace(cfg.TargetType))
	targetCode := strings.TrimSpace(cfg.TargetCode)
	targetVersion := strings.TrimSpace(cfg.TargetVersion)
	if targetType == "" || targetCode == "" {
		return nil, nil, "", fmt.Errorf("dailySimulation targetType and targetCode are required when entryId is not set")
	}

	entries, err := listAllClinicianAssessmentEntries(ctx, deps.APIClient, clinicianID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("list daily simulation clinician assessment entries: %w", err)
	}
	targetKey := assessmentEntryTargetKey(targetType, targetCode, targetVersion)
	for _, item := range entries {
		if item == nil {
			continue
		}
		if assessmentEntryTargetKey(item.TargetType, item.TargetCode, item.TargetVersion) != targetKey {
			continue
		}
		if !item.IsActive {
			item, err = deps.APIClient.ReactivateAssessmentEntry(ctx, item.ID)
			if err != nil {
				return nil, nil, "", fmt.Errorf("reactivate daily simulation entry %s: %w", item.ID, err)
			}
		}
		target, err := resolveDailySimulationTarget(ctx, deps.APIClient, deps.CollectionClient, item.TargetType, item.TargetCode, item.TargetVersion)
		if err != nil {
			return nil, nil, "", err
		}
		return item, target, clinicianID, nil
	}

	entry, err := deps.APIClient.CreateClinicianAssessmentEntry(ctx, clinicianID, CreateAssessmentEntryRequest{
		TargetType:    targetType,
		TargetCode:    targetCode,
		TargetVersion: targetVersion,
	})
	if err != nil {
		return nil, nil, "", fmt.Errorf("create daily simulation entry: %w", err)
	}
	target, err := resolveDailySimulationTarget(ctx, deps.APIClient, deps.CollectionClient, entry.TargetType, entry.TargetCode, entry.TargetVersion)
	if err != nil {
		return nil, nil, "", err
	}
	return entry, target, clinicianID, nil
}

func resolveDailySimulationTarget(
	ctx context.Context,
	apiClient *APIClient,
	collectionClient *APIClient,
	targetType, targetCode, targetVersion string,
) (*dailySimulationResolvedTarget, error) {
	targetType = strings.ToLower(strings.TrimSpace(targetType))
	targetCode = strings.TrimSpace(targetCode)
	targetVersion = strings.TrimSpace(targetVersion)
	if targetType == "" || targetCode == "" {
		return nil, fmt.Errorf("daily simulation targetType and targetCode are required")
	}

	switch targetType {
	case "scale":
		if apiClient == nil {
			return nil, fmt.Errorf("apiserver client is not initialized")
		}
		if collectionClient == nil {
			return nil, fmt.Errorf("collection client is not initialized")
		}
		scaleItem, err := apiClient.GetPublishedAssessmentModel(ctx, targetCode, targetVersion)
		if err != nil {
			return nil, fmt.Errorf("get scale %s: %w", targetCode, err)
		}
		if scaleItem == nil {
			return nil, fmt.Errorf("scale %s not found", targetCode)
		}
		questionnaireVersion := strings.TrimSpace(scaleItem.QuestionnaireVersion)
		if questionnaireVersion == "" {
			return nil, fmt.Errorf("published assessment model %s has empty questionnaire_version", targetCode)
		}
		detail, err := collectionClient.GetPublishedQuestionnaire(ctx, scaleItem.QuestionnaireCode, questionnaireVersion)
		if err != nil {
			return nil, fmt.Errorf("get questionnaire %s for scale %s: %w", scaleItem.QuestionnaireCode, targetCode, err)
		}
		return &dailySimulationResolvedTarget{
			TargetType:           targetType,
			TargetCode:           targetCode,
			TargetVersion:        targetVersion,
			QuestionnaireCode:    strings.TrimSpace(scaleItem.QuestionnaireCode),
			QuestionnaireVersion: questionnaireVersion,
			QuestionnaireTitle:   strings.TrimSpace(detail.Title),
			QuestionnaireDetail:  detail,
			RequiresAssessment:   true,
		}, nil
	case "questionnaire":
		if collectionClient == nil {
			return nil, fmt.Errorf("collection client is not initialized")
		}
		detail, err := collectionClient.GetPublishedQuestionnaire(ctx, targetCode, targetVersion)
		if err != nil {
			return nil, fmt.Errorf("get questionnaire %s: %w", targetCode, err)
		}
		if detail == nil {
			return nil, fmt.Errorf("questionnaire %s not found", targetCode)
		}
		version := targetVersion
		if version == "" {
			version = strings.TrimSpace(detail.Version)
		}
		return &dailySimulationResolvedTarget{
			TargetType:           targetType,
			TargetCode:           targetCode,
			TargetVersion:        targetVersion,
			QuestionnaireCode:    targetCode,
			QuestionnaireVersion: version,
			QuestionnaireTitle:   strings.TrimSpace(detail.Title),
			QuestionnaireDetail:  detail,
			RequiresAssessment:   strings.EqualFold(strings.TrimSpace(detail.Type), questionnaireTypeMedicalScale),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported daily simulation targetType %q", targetType)
	}
}

func ensureDailySimulationTestee(
	ctx context.Context,
	collectionClient *APIClient,
	cfg DailySimulationConfig,
	profile dailySimulationProfile,
) (*TesteeResponse, bool, error) {
	existing, err := findDailySimulationGuardianTestee(ctx, collectionClient, cfg, profile)
	if err != nil {
		// Fail closed: creating when the guardian lookup is unavailable would
		// turn a transient read failure into another profile/testee chain.
		return nil, false, fmt.Errorf("list guardian testees before create: %w", err)
	}
	if existing != nil {
		return existing, false, nil
	}

	testeeResp, err := collectionClient.CreateCollectionTestee(ctx, CollectionCreateTesteeRequest{
		Name:       profile.ChildName,
		Gender:     int32(profile.ChildGender),
		Birthday:   profile.ChildDOB,
		Relation:   normalizeDailySimulationGuardianRelation(cfg.GuardianRelation),
		Tags:       append([]string(nil), cfg.TesteeTags...),
		Source:     normalizeDailySimulationSource(cfg.TesteeSource),
		IsKeyFocus: cfg.IsKeyFocus,
	})
	if err != nil {
		// A timeout can happen after the server commits. Reconcile once before
		// returning the error so the caller never blindly repeats the POST.
		reconciled, reconcileErr := findDailySimulationGuardianTestee(ctx, collectionClient, cfg, profile)
		if reconcileErr == nil && reconciled != nil {
			return reconciled, false, nil
		}
		if reconcileErr != nil {
			return nil, false, fmt.Errorf("create collection testee %s: %w; reconcile guardian testees: %v", profile.ChildName, err, reconcileErr)
		}
		return nil, false, fmt.Errorf("create collection testee %s: %w", profile.ChildName, err)
	}
	return testeeResp, true, nil
}

func findDailySimulationGuardianTestee(
	ctx context.Context,
	collectionClient *APIClient,
	cfg DailySimulationConfig,
	profile dailySimulationProfile,
) (*TesteeResponse, error) {
	if collectionClient == nil {
		return nil, fmt.Errorf("guardian collection client is nil")
	}
	const pageSize = 100
	items := make([]*TesteeResponse, 0, pageSize)
	for offset := 0; ; offset += pageSize {
		resp, err := collectionClient.ListCollectionTestees(ctx, offset, pageSize)
		if err != nil {
			return nil, err
		}
		items = append(items, resp.Items...)
		if len(resp.Items) == 0 || int64(len(items)) >= resp.Total {
			break
		}
	}

	wantSource := normalizeDailySimulationSource(cfg.TesteeSource)
	matches := make([]*TesteeResponse, 0, 1)
	for _, item := range items {
		if item == nil || strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.IAMProfileID) == "" {
			continue
		}
		if strings.TrimSpace(item.Name) != strings.TrimSpace(profile.ChildName) ||
			item.Gender != int32(profile.ChildGender) ||
			strings.TrimSpace(item.Birthday) != strings.TrimSpace(profile.ChildDOB) ||
			strings.TrimSpace(item.Source) != wantSource {
			continue
		}
		matches = append(matches, item)
	}
	if len(matches) == 0 {
		return nil, nil
	}
	sort.Slice(matches, func(i, j int) bool {
		left, right := matches[i], matches[j]
		if left.CreatedAt.Equal(right.CreatedAt) {
			return strings.TrimSpace(left.ID) < strings.TrimSpace(right.ID)
		}
		if left.CreatedAt.IsZero() {
			return false
		}
		if right.CreatedAt.IsZero() {
			return true
		}
		return left.CreatedAt.Before(right.CreatedAt)
	})
	return matches[0], nil
}

func hasAssessmentEntryRelation(
	ctx context.Context,
	apiClient *APIClient,
	testeeID, entryID string,
) (bool, error) {
	relations, err := apiClient.GetTesteeClinicians(ctx, testeeID)
	if err != nil {
		return false, fmt.Errorf("list testee clinicians for %s: %w", testeeID, err)
	}
	for _, item := range relations.Items {
		if item == nil || item.Relation == nil {
			continue
		}
		relation := item.Relation
		if !relation.IsActive {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(relation.SourceType), "assessment_entry") {
			continue
		}
		if strings.TrimSpace(nullableString(relation.SourceID)) == strings.TrimSpace(entryID) {
			return true, nil
		}
	}
	return false, nil
}
