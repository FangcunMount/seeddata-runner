package seedapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestOrdinaryDailyAPIPathsRemainStable(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	_, err := client.CreateCollectionTestee(ctx, CollectionCreateTesteeRequest{})
	check(err)
	_, err = client.ListTesteesByOrgCreatedOnDate(ctx, 7, time.Date(2026, 8, 2, 12, 0, 0, 0, time.Local), 1, 100)
	check(err)
	_, err = client.CreateClinicianAssessmentEntry(ctx, "clinician-1", CreateAssessmentEntryRequest{})
	check(err)
	_, err = client.ResolveAssessmentEntry(ctx, "entry-token")
	check(err)
	_, err = client.IntakeAssessmentEntry(ctx, "entry-token", IntakeAssessmentEntryRequest{})
	check(err)
	_, err = client.ReactivateAssessmentEntry(ctx, "entry-1")
	check(err)
	_, err = client.GetPlan(ctx, "plan-1")
	check(err)
	_, err = client.EnrollTesteeInPlan(ctx, EnrollTesteeRequest{})
	check(err)
	_, err = client.ListPlanTaskWindow(ctx, ListPlanTaskWindowRequest{})
	check(err)

	want := []string{
		"POST /api/v1/testees",
		"GET /api/v1/testees?org_id=7&page=1&page_size=100&created_start_date=2026-08-02&created_end_date=2026-08-02",
		"POST /api/v1/clinicians/clinician-1/assessment-entries",
		"GET /api/v1/public/assessment-entries/entry-token",
		"POST /api/v1/public/assessment-entries/entry-token/intake",
		"POST /api/v1/assessment-entries/entry-1/reactivate",
		"GET /api/v1/plans/plan-1",
		"POST /api/v1/plans/enroll",
		"POST /internal/v1/plans/tasks/window",
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("ordinary request paths changed:\n got: %#v\nwant: %#v", requests, want)
	}
}
