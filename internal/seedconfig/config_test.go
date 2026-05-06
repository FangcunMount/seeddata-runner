package seedconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadNormalizesAndValidatesPlanSubmit(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "seeddata.yaml")
	content := `
global:
  orgId: 1
api:
  baseUrl: "https://qs.example.com"
dailySimulation:
  clinicianIds: ["1001"]
  targetType: "scale"
  targetCode: "SAS"
  planIds: ["614333603412718126"]
planSubmit:
  planIds: ["614333603412718126", "614187067651404334"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.PlanSubmit.PlanIDStrings()) != 2 {
		t.Fatalf("unexpected plan ids: %#v", cfg.PlanSubmit.PlanIDStrings())
	}
	if cfg.PlanSubmit.PlanIDStrings()[0] != "614333603412718126" || cfg.PlanSubmit.PlanIDStrings()[1] != "614187067651404334" {
		t.Fatalf("unexpected normalized plan ids: %#v", cfg.PlanSubmit.PlanIDStrings())
	}
	if cfg.PlanSubmit.Workers != DefaultPlanSubmitWorkers {
		t.Fatalf("unexpected default plan workers: %d", cfg.PlanSubmit.Workers)
	}
	if cfg.PlanSubmit.CompletionPercent == nil || *cfg.PlanSubmit.CompletionPercent != DefaultPlanSubmitCompletionPercent {
		t.Fatalf("unexpected default completion percent: %v", cfg.PlanSubmit.CompletionPercent)
	}
	if cfg.PlanSubmit.IdleInterval != DefaultPlanSubmitIdleInterval {
		t.Fatalf("unexpected default idle interval: %q", cfg.PlanSubmit.IdleInterval)
	}
	if cfg.PlanSubmit.ActiveInterval != DefaultPlanSubmitActiveInterval {
		t.Fatalf("unexpected default active interval: %q", cfg.PlanSubmit.ActiveInterval)
	}
	if cfg.DailySimulation.CountPerRun != DefaultDailySimulationCountPerRun {
		t.Fatalf("unexpected default count per run: %d", cfg.DailySimulation.CountPerRun)
	}
	if cfg.DailySimulation.RunAt != DefaultDailySimulationRunAt {
		t.Fatalf("unexpected default runAt: %q", cfg.DailySimulation.RunAt)
	}
	if cfg.DailySimulation.GuardianRelation != DefaultDailySimulationGuardianRelation {
		t.Fatalf("unexpected default guardianRelation: %q", cfg.DailySimulation.GuardianRelation)
	}
}

func TestLoadRequiresPlanSubmitPlanID(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "seeddata.yaml")
	content := `
global:
  orgId: 1
api:
  baseUrl: "https://qs.example.com"
dailySimulation:
  clinicianIds: ["1001"]
  targetType: "scale"
  targetCode: "SAS"
  planIds: ["614333603412718126"]
planSubmit:
  workers: 2
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil || !strings.Contains(err.Error(), "planSubmit.planIds is required") {
		t.Fatalf("expected missing planSubmit.planIds error, got %v", err)
	}
}

func TestLoadRejectsNonPositivePlanSubmitIntervals(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "seeddata.yaml")
	content := `
global:
  orgId: 1
api:
  baseUrl: "https://qs.example.com"
dailySimulation:
  clinicianIds: ["1001"]
  targetType: "scale"
  targetCode: "SAS"
  planIds: ["614333603412718126"]
planSubmit:
  planIds: ["614333603412718126"]
  idleInterval: "0s"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil || !strings.Contains(err.Error(), "planSubmit.idleInterval must be positive") {
		t.Fatalf("expected invalid planSubmit.idleInterval error, got %v", err)
	}
}

func TestLoadRejectsInvalidPlanSubmitCompletionPercent(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "seeddata.yaml")
	content := `
global:
  orgId: 1
api:
  baseUrl: "https://qs.example.com"
dailySimulation:
  clinicianIds: ["1001"]
  targetType: "scale"
  targetCode: "SAS"
  planIds: ["614333603412718126"]
planSubmit:
  planIds: ["614333603412718126"]
  completionPercent: 101
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil || !strings.Contains(err.Error(), "planSubmit.completionPercent must be between 0 and 100") {
		t.Fatalf("expected invalid planSubmit.completionPercent error, got %v", err)
	}
}

func TestLoadAcceptsZeroPlanSubmitCompletionPercent(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "seeddata.yaml")
	content := `
global:
  orgId: 1
api:
  baseUrl: "https://qs.example.com"
dailySimulation:
  clinicianIds: ["1001"]
  targetType: "scale"
  targetCode: "SAS"
  planIds: ["614333603412718126"]
planSubmit:
  planIds: ["614333603412718126"]
  completionPercent: 0
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.PlanSubmit.CompletionPercent == nil || *cfg.PlanSubmit.CompletionPercent != 0 {
		t.Fatalf("expected explicit zero completion percent, got %v", cfg.PlanSubmit.CompletionPercent)
	}
}

func TestLoadOverridesIAMCredentialsFromEnv(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "seeddata.yaml")
	content := `
global:
  orgId: 1
api:
  baseUrl: "https://qs.example.com"
iam:
  username: "yaml-user"
  password: "yaml-pass"
dailySimulation:
  clinicianIds: ["1001"]
  targetType: "scale"
  targetCode: "SAS"
  planIds: ["614333603412718126"]
planSubmit:
  planIds: ["614333603412718126"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("IAM_USERNAME", "env-user")
	t.Setenv("IAM_PASSWORD", "env-pass")

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.IAM.Username != "env-user" {
		t.Fatalf("unexpected iam username: %q", cfg.IAM.Username)
	}
	if cfg.IAM.Password != "env-pass" {
		t.Fatalf("unexpected iam password: %q", cfg.IAM.Password)
	}
}

func TestLoadAcceptsDailySimulationWindowSchedule(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "seeddata.yaml")
	content := `
global:
  orgId: 1
api:
  baseUrl: "https://qs.example.com"
dailySimulation:
  clinicianIds: ["1001"]
  targetType: "scale"
  targetCode: "SAS"
  planIds: ["614333603412718126"]
  windowStartAt: "10:00"
  windowEndAt: "18:00"
  interval: "30m"
  dailyMaxUsers: 80
planSubmit:
  planIds: ["614333603412718126"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.DailySimulation.WindowStartAt != "10:00" {
		t.Fatalf("unexpected window start: %q", cfg.DailySimulation.WindowStartAt)
	}
	if cfg.DailySimulation.WindowEndAt != "18:00" {
		t.Fatalf("unexpected window end: %q", cfg.DailySimulation.WindowEndAt)
	}
	if cfg.DailySimulation.Interval != "30m" {
		t.Fatalf("unexpected interval: %q", cfg.DailySimulation.Interval)
	}
	if cfg.DailySimulation.DailyMaxUsers != 80 {
		t.Fatalf("unexpected daily max users: %d", cfg.DailySimulation.DailyMaxUsers)
	}
}

func TestLoadAcceptsDailySimulationAdditionalTargetCodes(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "seeddata.yaml")
	content := `
global:
  orgId: 1
api:
  baseUrl: "https://qs.example.com"
dailySimulation:
  clinicianIds: ["1001"]
  targetType: "scale"
  targetCode: "SAS"
  additionalTargetCodes: ["PHQ9", "GAD7", "PHQ9"]
  additionalTargetMaxCount: 2
  planIds: ["614333603412718126"]
planSubmit:
  planIds: ["614333603412718126"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got, want := cfg.DailySimulation.AdditionalTargetCodes, []string{"PHQ9", "GAD7"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("unexpected additional target codes: %#v", got)
	}
	if cfg.DailySimulation.AdditionalTargetMaxCount != 2 {
		t.Fatalf("unexpected additional target max count: %d", cfg.DailySimulation.AdditionalTargetMaxCount)
	}
}

func TestLoadRejectsDailySimulationAdditionalTargetMaxCountGreaterThanCodes(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "seeddata.yaml")
	content := `
global:
  orgId: 1
api:
  baseUrl: "https://qs.example.com"
dailySimulation:
  clinicianIds: ["1001"]
  targetType: "scale"
  targetCode: "SAS"
  additionalTargetCodes: ["PHQ9", "GAD7"]
  additionalTargetMaxCount: 3
  planIds: ["614333603412718126"]
planSubmit:
  planIds: ["614333603412718126"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil || !strings.Contains(err.Error(), "dailySimulation.additionalTargetMaxCount must be <= number of additional target codes") {
		t.Fatalf("expected invalid additionalTargetMaxCount error, got %v", err)
	}
}

func TestLoadRequiresIAMMockConsumerSharedSecretWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "seeddata.yaml")
	content := `
global:
  orgId: 1
api:
  baseUrl: "https://qs.example.com"
iam:
  baseUrl: "https://iam.example.com"
  mockConsumer:
    enabled: true
dailySimulation:
  clinicianIds: ["1001"]
  targetType: "scale"
  targetCode: "SAS"
  planIds: ["614333603412718126"]
planSubmit:
  planIds: ["614333603412718126"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil || !strings.Contains(err.Error(), "iam.mockConsumer.sharedSecret is required") {
		t.Fatalf("expected missing mock consumer secret error, got %v", err)
	}
}

func TestLoadDefaultsIAMMockConsumerMaxConcurrentWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "seeddata.yaml")
	content := `
global:
  orgId: 1
api:
  baseUrl: "https://qs.example.com"
iam:
  baseUrl: "https://iam.example.com"
  mockConsumer:
    enabled: true
    sharedSecret: "top-secret"
dailySimulation:
  clinicianIds: ["1001"]
  targetType: "scale"
  targetCode: "SAS"
  planIds: ["614333603412718126"]
planSubmit:
  planIds: ["614333603412718126"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.IAM.MockConsumer.MaxConcurrent != 1 {
		t.Fatalf("unexpected default mock-consumer maxConcurrent: %d", cfg.IAM.MockConsumer.MaxConcurrent)
	}
}

func TestLoadNormalizesLegacyGuardianRelation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "seeddata.yaml")
	content := `
global:
  orgId: 1
api:
  baseUrl: "https://qs.example.com"
dailySimulation:
  clinicianIds: ["1001"]
  targetType: "scale"
  targetCode: "SAS"
  guardianRelation: "guardian"
  planIds: ["614333603412718126"]
planSubmit:
  planIds: ["614333603412718126"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.DailySimulation.GuardianRelation != DefaultDailySimulationGuardianRelation {
		t.Fatalf("unexpected normalized guardianRelation: %q", cfg.DailySimulation.GuardianRelation)
	}
}

func TestLoadRejectsInvalidGuardianRelation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "seeddata.yaml")
	content := `
global:
  orgId: 1
api:
  baseUrl: "https://qs.example.com"
dailySimulation:
  clinicianIds: ["1001"]
  targetType: "scale"
  targetCode: "SAS"
  guardianRelation: "uncle"
  planIds: ["614333603412718126"]
planSubmit:
  planIds: ["614333603412718126"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil || !strings.Contains(err.Error(), "dailySimulation.guardianRelation must be one of self,parent,grandparent,other") {
		t.Fatalf("expected invalid guardianRelation error, got %v", err)
	}
}
