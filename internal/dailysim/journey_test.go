package dailysim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	"github.com/FangcunMount/seeddata-runner/internal/seedconfig"
)

func TestResolveDailySimulationJourneyTargetStable(t *testing.T) {
	cfg := DailySimulationConfig{
		JourneyMix: DailySimulationJourneyMixConfig{
			RegisterOnlyWeight: 10,
			CreateTesteeWeight: 20,
			ResolveEntryWeight: 30,
			SubmitAnswerWeight: 40,
		},
	}
	runDate := time.Date(2026, 4, 19, 0, 0, 0, 0, time.Local)

	first := resolveDailySimulationJourneyTarget(cfg, runDate, 7)
	second := resolveDailySimulationJourneyTarget(cfg, runDate, 7)
	if first != second {
		t.Fatalf("expected stable journey target, got %q and %q", first, second)
	}
	switch first {
	case dailySimulationJourneyRegisterOnly,
		dailySimulationJourneyCreateTestee,
		dailySimulationJourneyResolveEntry,
		dailySimulationJourneySubmitAnswer:
	default:
		t.Fatalf("unexpected journey target %q", first)
	}
}

func TestResolveDailySimulationJourneyTargetDefaultsToSubmit(t *testing.T) {
	target := resolveDailySimulationJourneyTarget(DailySimulationConfig{}, time.Date(2026, 4, 19, 0, 0, 0, 0, time.Local), 1)
	if target != dailySimulationJourneySubmitAnswer {
		t.Fatalf("expected default target %q, got %q", dailySimulationJourneySubmitAnswer, target)
	}
}

func TestResolveDailySimulationJourneyTargetForMockConsumerAlwaysSubmits(t *testing.T) {
	cfg := DailySimulationConfig{
		JourneyMix: DailySimulationJourneyMixConfig{
			RegisterOnlyWeight: 20,
			CreateTesteeWeight: 20,
			ResolveEntryWeight: 20,
			SubmitAnswerWeight: 40,
		},
	}
	target := resolveDailySimulationJourneyTargetForMode(cfg, IAMConfig{
		MockConsumer: IAMMockConsumerConfig{Enabled: true},
	}, time.Date(2026, 4, 19, 0, 0, 0, 0, time.Local), 3)
	if target != dailySimulationJourneySubmitAnswer {
		t.Fatalf("expected mock-consumer mode to always submit answer, got %q", target)
	}
}

func TestShouldStopDailySimulationJourneyAfter(t *testing.T) {
	cases := []struct {
		name   string
		target dailySimulationJourneyTarget
		stage  dailySimulationJourneyStage
		want   bool
	}{
		{name: "register stops after guardian", target: dailySimulationJourneyRegisterOnly, stage: dailySimulationJourneyStageGuardianAccount, want: true},
		{name: "register does not stop after entry", target: dailySimulationJourneyRegisterOnly, stage: dailySimulationJourneyStageAssessmentEntry, want: false},
		{name: "testee stops after testee", target: dailySimulationJourneyCreateTestee, stage: dailySimulationJourneyStageTesteeProfile, want: true},
		{name: "testee does not stop after plan enrollment", target: dailySimulationJourneyCreateTestee, stage: dailySimulationJourneyStagePlanEnrollment, want: false},
		{name: "resolve stops after entry", target: dailySimulationJourneyResolveEntry, stage: dailySimulationJourneyStageAssessmentEntry, want: true},
		{name: "submit stops after submit", target: dailySimulationJourneySubmitAnswer, stage: dailySimulationJourneyStageAnswerSheet, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldStopDailySimulationJourneyAfter(tc.target, tc.stage)
			if got != tc.want {
				t.Fatalf("unexpected stop decision: got=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestResolveDailySimulationIAMMockConsumerEndpointPathDefaults(t *testing.T) {
	got := resolveDailySimulationIAMMockConsumerEndpointPath(IAMConfig{})
	if got != "/api/v2/internal/authn/mock-consumers/ensure" {
		t.Fatalf("unexpected default endpoint path: %q", got)
	}
}

func TestDailySimulationUsesIAMMockConsumer(t *testing.T) {
	if dailySimulationUsesIAMMockConsumer(IAMConfig{}) {
		t.Fatalf("expected mock-consumer mode disabled by default")
	}
	if !dailySimulationUsesIAMMockConsumer(IAMConfig{
		MockConsumer: IAMMockConsumerConfig{Enabled: true},
	}) {
		t.Fatalf("expected mock-consumer mode enabled")
	}
}

func TestDailySimulationTesteeID(t *testing.T) {
	if got := dailySimulationTesteeID(nil); got != "" {
		t.Fatalf("expected empty testee id for nil testee, got %q", got)
	}
	if got := dailySimulationTesteeID(&TesteeResponse{ID: " 615 "}); got != "615" {
		t.Fatalf("expected trimmed testee id, got %q", got)
	}
}

func TestShouldRetryDailySimulationIAMLogin(t *testing.T) {
	if !shouldRetryDailySimulationIAMLogin(context.DeadlineExceeded) {
		t.Fatalf("expected timeout to be retryable")
	}
	if !shouldRetryDailySimulationIAMLogin(assertErr("iam login failed: status=502 body=bad gateway")) {
		t.Fatalf("expected 502 to be retryable")
	}
	if shouldRetryDailySimulationIAMLogin(assertErr("iam login failed: status=401 body=unauthorized")) {
		t.Fatalf("expected 401 not to be retryable")
	}
}

func TestEnsureDailySimulationTesteeDoesNotSendSeedTagByDefault(t *testing.T) {
	var captured CollectionCreateTesteeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/testees/exists":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"exists":false,"testee_id":""}}`))
			return
		case "/api/v1/testees":
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"id":"testee-1","name":"王子轩"}}`))
			return
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	childGender := uint8(1)
	profile := dailySimulationProfile{
		GuardianName: "王敏",
		ChildName:    "王子轩",
		ChildDOB:     "2014-04-20",
		ChildGender:  1,
	}
	child := &IAMChildResponse{
		ID:        "child-1",
		LegalName: "王子轩",
		Gender:    &childGender,
		DOB:       "2014-04-20",
	}

	testee, created, err := ensureDailySimulationTestee(context.Background(), client, "guardian-1", DailySimulationConfig{}, profile, child)
	if err != nil {
		t.Fatalf("ensure testee: %v", err)
	}
	if !created {
		t.Fatalf("expected created testee")
	}
	if testee == nil || testee.ID != "testee-1" {
		t.Fatalf("unexpected testee response: %+v", testee)
	}
	if len(captured.Tags) != 0 {
		t.Fatalf("expected no default testee tags, got %v", captured.Tags)
	}
}

func TestEnsureDailySimulationChildNormalizesLegacyGuardianRelation(t *testing.T) {
	var captured IAMChildRegisterRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/identity/me/profiles":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"total":0,"items":[]}}`))
		case "/api/v2/identity/profiles":
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"profile":{"id":"child-1","legalName":"王子轩","dob":"2014-04-20"}}}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	child, created, err := ensureDailySimulationChild(context.Background(), client, DailySimulationConfig{
		GuardianRelation: "guardian",
	}, dailySimulationProfile{
		ChildName:   "王子轩",
		ChildDOB:    "2014-04-20",
		ChildGender: 1,
	})
	if err != nil {
		t.Fatalf("ensure child: %v", err)
	}
	if !created {
		t.Fatalf("expected child to be created")
	}
	if child == nil || child.ID != "child-1" {
		t.Fatalf("unexpected child response: %+v", child)
	}
	if captured.Relation != seedconfig.DefaultDailySimulationGuardianRelation {
		t.Fatalf("unexpected relation %q", captured.Relation)
	}
}

func TestBuildDailySimulationEntryIntakeRequestUsesExistingTesteeProfileID(t *testing.T) {
	profileID := "615969735435104814"
	birthday := time.Date(2014, 4, 20, 0, 0, 0, 0, time.Local)

	req, err := buildDailySimulationEntryIntakeRequest(&dailySimulationJourneyState{
		existingTestee: &ApiserverTesteeResponse{
			ID:        "testee-1",
			ProfileID: &profileID,
			Name:      "王子轩",
			Gender:    "male",
			Birthday:  &birthday,
		},
	})
	if err != nil {
		t.Fatalf("build intake request: %v", err)
	}
	if req.ProfileID == nil || *req.ProfileID != 615969735435104814 {
		t.Fatalf("unexpected profile id: %+v", req.ProfileID)
	}
	if req.Name != "王子轩" {
		t.Fatalf("unexpected name: %q", req.Name)
	}
	if req.Gender != "male" {
		t.Fatalf("unexpected gender: %q", req.Gender)
	}
	if req.Birthday == nil || !req.Birthday.Equal(birthday) {
		t.Fatalf("unexpected birthday: %+v", req.Birthday)
	}
}

func TestResolveDailySimulationCanonicalTesteeIDUsesProfileLookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/testees/by-profile-id" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("org_id"); got != "1" {
			t.Fatalf("unexpected org_id %q", got)
		}
		if got := r.URL.Query().Get("profile_id"); got != "615969735435104814" {
			t.Fatalf("unexpected profile_id %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"id":"615508260325175854","profile_id":"615969735435104814","name":"王子轩"}}`))
	}))
	defer server.Close()

	profileID := "615969735435104814"
	state := &dailySimulationJourneyState{
		deps: &dependencies{
			Config: &seedconfig.Config{
				Global: seedconfig.GlobalConfig{OrgID: 1},
			},
			APIClient: NewAPIClient(server.URL, "", nil),
		},
		testee: &TesteeResponse{ID: "old-id"},
		existingTestee: &ApiserverTesteeResponse{
			ID:        "old-id",
			ProfileID: &profileID,
			Name:      "王子轩",
		},
	}

	canonicalID, err := resolveDailySimulationCanonicalTesteeID(context.Background(), state)
	if err != nil {
		t.Fatalf("resolve canonical testee id: %v", err)
	}
	if canonicalID != 615508260325175854 {
		t.Fatalf("unexpected canonical testee id: %d", canonicalID)
	}
}

func TestWaitForDailySimulationAssessmentFallsBackToAssessmentList(t *testing.T) {
	const (
		answerSheetID     = "615984776595124782"
		testeeID          = "615969746222854702"
		assessmentID      = "615984705628090926"
		questionnaireCode = "3adyDE"
		questionnaireVer  = "6.0.1"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/answersheets/"+answerSheetID+"/assessment":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":1,"message":"An internal server error occurred"}`))
			return
		case r.URL.Path == "/api/v1/evaluations/assessments":
			if got := r.URL.Query().Get("testee_id"); got != testeeID {
				t.Fatalf("unexpected testee_id %q", got)
			}
			if got := r.URL.Query().Get("page"); got != "1" {
				t.Fatalf("unexpected page %q", got)
			}
			if got := r.URL.Query().Get("page_size"); got != "100" {
				t.Fatalf("unexpected page_size %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"items":[{"id":"` + assessmentID + `","testee_id":"` + testeeID + `","questionnaire_code":"` + questionnaireCode + `","status":"done"}],"total":1,"page":1,"page_size":100,"total_pages":1}}`))
			return
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	logger := log.New(log.NewOptions())
	collectionClient := NewAPIClient(server.URL, "", logger)
	apiClient := NewAPIClient(server.URL, "", logger)

	gotAssessmentID, err := waitForDailySimulationAssessment(
		context.Background(),
		collectionClient,
		apiClient,
		answerSheetID,
		testeeID,
		questionnaireCode,
		questionnaireVer,
	)
	if err != nil {
		t.Fatalf("wait for assessment: %v", err)
	}
	if gotAssessmentID != assessmentID {
		t.Fatalf("unexpected assessment id: got=%q want=%q", gotAssessmentID, assessmentID)
	}
}

func assertErr(message string) error {
	return testErr(message)
}

type testErr string

func (e testErr) Error() string { return string(e) }
