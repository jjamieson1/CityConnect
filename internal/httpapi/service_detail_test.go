package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

type catalogEntry struct {
	ID     string `json:"id"`
	Code   string `json:"code"`
	Name   string `json:"name"`
	Expect *struct {
		FirstResponseHours int `json:"firstResponseHours"`
		ResolutionHours    int `json:"resolutionHours"`
	} `json:"expect"`
	Department      string `json:"department"`
	RelatedArticles []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"relatedArticles"`
}

func portalCatalog(t *testing.T, e *env) map[string]catalogEntry {
	t.Helper()

	var body struct {
		Items []catalogEntry `json:"items"`
	}
	if status := doJSON(t, newJarClient(), http.MethodGet,
		e.api.URL+"/api/portal/catalog", nil, &body); status != 200 {
		t.Fatalf("catalog -> %d", status)
	}
	out := map[string]catalogEntry{}
	for _, c := range body.Items {
		out[c.Code] = c
	}
	return out
}

// The expected response time has to come from the City's own configuration.
// Static copy goes stale the first time somebody changes an SLA policy, and
// nobody notices because nothing breaks.
func TestServiceDetailCarriesAComputedExpectation(t *testing.T) {
	e := newEnv(t)

	entry, ok := portalCatalog(t, e)["POTHOLE"]
	if !ok {
		t.Fatal("POTHOLE is not in the catalogue")
	}
	if entry.Expect == nil {
		t.Fatal("no expected response time offered for a service with an SLA policy")
	}
	if entry.Expect.FirstResponseHours <= 0 {
		t.Errorf("first response = %dh, want a positive number of hours",
			entry.Expect.FirstResponseHours)
	}
	if entry.Expect.ResolutionHours < entry.Expect.FirstResponseHours {
		t.Errorf("resolution (%dh) is sooner than first response (%dh)",
			entry.Expect.ResolutionHours, entry.Expect.FirstResponseHours)
	}
	// The owning department is part of setting expectations, and the public
	// name is what a resident should see rather than the internal one.
	if entry.Department == "" {
		t.Error("no owning department shown for a service that has one")
	}
}

// Changing the policy must change what residents are told, with no deploy and
// no content edit. This is the whole argument for computing it.
func TestLooseningTheSLAChangesWhatResidentsAreTold(t *testing.T) {
	e := newEnv(t)

	before := portalCatalog(t, e)["POTHOLE"]
	if before.Expect == nil {
		t.Fatal("no baseline expectation")
	}

	var st domain.ServiceType
	if err := e.db.First(&st, "code = ?", "POTHOLE").Error; err != nil {
		t.Fatalf("load service: %v", err)
	}
	if st.SLAPolicyID == "" {
		t.Fatal("POTHOLE has no SLA policy; this test proves nothing")
	}
	if err := e.db.Model(&domain.SLAPolicy{}).Where("id = ?", st.SLAPolicyID).
		Updates(map[string]any{
			"first_response_minutes": 4800,
			"resolution_minutes":     9600,
		}).Error; err != nil {
		t.Fatalf("loosen policy: %v", err)
	}

	after := portalCatalog(t, e)["POTHOLE"]
	if after.Expect == nil {
		t.Fatal("the expectation disappeared after the policy changed")
	}
	if after.Expect.FirstResponseHours <= before.Expect.FirstResponseHours {
		t.Errorf("first response went from %dh to %dh after the target was loosened",
			before.Expect.FirstResponseHours, after.Expect.FirstResponseHours)
	}
}

// A service with no policy attached says nothing rather than inventing a
// target the City never agreed to.
func TestAServiceWithNoPolicyPromisesNothing(t *testing.T) {
	e := newEnv(t)

	st := domain.ServiceType{
		Code: "NOPOLICY", Name: "Unpolicied service", DefaultPriority: domain.PriorityNormal,
		PublishState: domain.PublishPublished, PublicVisible: true,
	}
	if err := e.db.Create(&st).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	entry, ok := portalCatalog(t, e)["NOPOLICY"]
	if !ok {
		t.Fatal("the service is missing from the catalogue")
	}
	if entry.Expect != nil {
		t.Errorf("promised %+v for a service with no SLA policy", *entry.Expect)
	}
}

// The knowledge-article slot exists so the CRM adapter is a wiring job. Until
// something fills it, it must be absent rather than an empty box on the page.
func TestKnowledgeArticleSlotIsEmptyRatherThanFabricated(t *testing.T) {
	e := newEnv(t)

	entry := portalCatalog(t, e)["POTHOLE"]
	if len(entry.RelatedArticles) != 0 {
		t.Errorf("%d related articles from a deployment with no knowledge base",
			len(entry.RelatedArticles))
	}
}
