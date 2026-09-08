package httpapi_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// catalogCodes lists what the public catalogue is currently offering.
func catalogCodes(t *testing.T, e *env) map[string]bool {
	t.Helper()

	var catalog struct {
		Items []struct {
			Code          string `json:"code"`
			Promoted      bool   `json:"promoted"`
			PromotedOrder int    `json:"promotedOrder"`
		} `json:"items"`
	}
	if status := doJSON(t, newJarClient(), http.MethodGet,
		e.api.URL+"/api/portal/catalog", nil, &catalog); status != 200 {
		t.Fatalf("catalog -> %d", status)
	}
	out := map[string]bool{}
	for _, c := range catalog.Items {
		out[c.Code] = true
	}
	return out
}

// The unauthenticated submission endpoint is the one place where a service that
// should not be reachable must not be reachable by guessing its id.
func TestPortalRefusesAServiceThatIsNotLive(t *testing.T) {
	past := time.Now().Add(-48 * time.Hour)
	yesterday := time.Now().Add(-24 * time.Hour)
	future := time.Now().Add(48 * time.Hour)

	cases := []struct {
		name string
		st   domain.ServiceType
	}{
		{"a draft", domain.ServiceType{
			Code: "DRAFT", Name: "Not ready yet",
			PublishState: domain.PublishDraft, PublicVisible: true,
		}},
		{"an archived service", domain.ServiceType{
			Code: "GONE", Name: "Withdrawn",
			PublishState: domain.PublishArchived, PublicVisible: true,
		}},
		{"a service whose season has not started", domain.ServiceType{
			Code: "SOON", Name: "Spring cleanup",
			PublishState: domain.PublishPublished, PublicVisible: true,
			EffectiveStart: &future,
		}},
		{"a service whose season has ended", domain.ServiceType{
			Code: "OVER", Name: "Leaf collection",
			PublishState: domain.PublishPublished, PublicVisible: true,
			EffectiveStart: &past, EffectiveEnd: &yesterday,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)

			st := tc.st
			st.DefaultPriority = domain.PriorityNormal
			if err := e.db.Create(&st).Error; err != nil {
				t.Fatalf("seed: %v", err)
			}

			if catalogCodes(t, e)[st.Code] {
				t.Errorf("%s is listed in the public catalogue", tc.name)
			}

			// Listing is not the control. A caller who already holds the id —
			// from an old link, or by guessing — must still be refused.
			code := doJSON(t, newJarClient(), http.MethodPost, e.api.URL+"/api/portal/requests",
				map[string]any{
					"serviceTypeId": st.ID,
					"subject":       "Filed against something not live",
					"address1":      "1 Nowhere Road",
					"formToken":     formToken(t, e),
					"contactEmail":  "resident@example.com",
				}, nil)
			if code != http.StatusBadRequest {
				t.Errorf("submission against %s -> %d, want 400", tc.name, code)
			}
		})
	}
}

// A published service inside its window is offered, so the tests above are
// failing for the right reason rather than because nothing is ever listed.
func TestPortalOffersAServiceInsideItsWindow(t *testing.T) {
	e := newEnv(t)

	past := time.Now().Add(-24 * time.Hour)
	future := time.Now().Add(24 * time.Hour)
	st := domain.ServiceType{
		Code: "INSEASON", Name: "Yard waste collection", DefaultPriority: domain.PriorityNormal,
		PublishState: domain.PublishPublished, PublicVisible: true,
		EffectiveStart: &past, EffectiveEnd: &future,
	}
	if err := e.db.Create(&st).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	if !catalogCodes(t, e)["INSEASON"] {
		t.Fatal("a published, in-season service is missing from the catalogue")
	}

	var created struct {
		Reference string `json:"reference"`
	}
	code := doJSON(t, newJarClient(), http.MethodPost, e.api.URL+"/api/portal/requests",
		map[string]any{
			"serviceTypeId": st.ID,
			"subject":       "Yard waste not collected",
			"address1":      "44 Elm Street",
			"formToken":     formToken(t, e),
			"contactEmail":  "resident@example.com",
		}, &created)
	if code != http.StatusCreated {
		t.Fatalf("submission -> %d, want 201", code)
	}
}

// The landing-view shortcuts reach the portal on the same response as the
// catalogue itself, with the order staff chose.
func TestPromotedShortcutsReachThePortal(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-admin", "admin@city.example", domain.RoleAdmin)

	pothole := publicServiceType(t, e, "POTHOLE")
	general := publicServiceType(t, e, "GENERAL")

	var promoted struct {
		Items []struct {
			Code string `json:"code"`
		} `json:"items"`
	}
	if code := e.do(http.MethodPut, "/api/service-types/promoted",
		map[string]any{"ids": []string{general, pothole}}, &promoted); code != 200 {
		t.Fatalf("promote -> %d", code)
	}
	if len(promoted.Items) != 2 || promoted.Items[0].Code != "GENERAL" {
		t.Errorf("promoted = %v, want GENERAL first", promoted.Items)
	}

	var catalog struct {
		Items []struct {
			Code          string `json:"code"`
			Promoted      bool   `json:"promoted"`
			PromotedOrder int    `json:"promotedOrder"`
		} `json:"items"`
	}
	if status := doJSON(t, newJarClient(), http.MethodGet,
		e.api.URL+"/api/portal/catalog", nil, &catalog); status != 200 {
		t.Fatalf("catalog -> %d", status)
	}

	seen := 0
	for _, c := range catalog.Items {
		switch c.Code {
		case "GENERAL":
			seen++
			if !c.Promoted || c.PromotedOrder != 0 {
				t.Errorf("GENERAL promoted=%v order=%d, want true/0", c.Promoted, c.PromotedOrder)
			}
		case "POTHOLE":
			seen++
			if !c.Promoted || c.PromotedOrder != 1 {
				t.Errorf("POTHOLE promoted=%v order=%d, want true/1", c.Promoted, c.PromotedOrder)
			}
		default:
			if c.Promoted {
				t.Errorf("%s is promoted and should not be", c.Code)
			}
		}
	}
	if seen != 2 {
		t.Errorf("saw %d of the two promoted services in the catalogue", seen)
	}
}
