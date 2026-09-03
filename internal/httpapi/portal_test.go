package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// portalSignIn opens a portal session for a C2 subject, returning a client
// that carries only the portal cookie.
func (e *env) portalSignIn(t *testing.T, sub string) *http.Client {
	t.Helper()

	client := newJarClient()
	e.stub.SignOutAll()
	e.stub.SignIn(sub)

	resp, err := client.Get(e.api.URL + "/api/portal/auth/login")
	if err != nil {
		t.Fatalf("portal login: %v", err)
	}
	resp.Body.Close()

	var profile map[string]any
	if code := doJSON(t, client, http.MethodGet, e.api.URL+"/api/portal/me", nil, &profile); code != 200 {
		t.Fatalf("portal sign-in did not establish a session (GET /portal/me -> %d)", code)
	}
	return client
}

// TestPortalSignInProvisionsContact covers the deliberate asymmetry with staff
// access: an unknown C2 subject is refused the console but welcomed by the
// portal, because the portal only ever shows that person their own records.
func TestPortalSignInProvisionsContact(t *testing.T) {
	e := newEnv(t)

	client := e.portalSignIn(t, "citizen-brand-new")

	var profile struct {
		Name         string `json:"name"`
		OpenRequests int    `json:"openRequests"`
	}
	if code := doJSON(t, client, http.MethodGet, e.api.URL+"/api/portal/me", nil, &profile); code != 200 {
		t.Fatalf("GET /portal/me -> %d", code)
	}
	if profile.Name == "" {
		t.Error("a contact was not provisioned for a new citizen")
	}

	var n int64
	e.db.Model(&domain.ContactIdentity{}).
		Where("provider = ? AND external_id = ?", domain.ProviderC2, "citizen-brand-new").Count(&n)
	if n != 1 {
		t.Errorf("expected exactly one C2 identity link, found %d", n)
	}
}

// TestPortalSessionCannotReachStaffEndpoints is the security boundary that
// justifies a separate session table. A citizen holding a valid portal cookie
// must be refused everywhere on the staff surface — otherwise any resident who
// can sign into C2 owns the CRM.
func TestPortalSessionCannotReachStaffEndpoints(t *testing.T) {
	e := newEnv(t)
	client := e.portalSignIn(t, "citizen-nosy")

	staffOnly := []string{
		"/api/requests",
		"/api/contacts",
		"/api/users",
		"/api/audit",
		"/api/reports/volume",
		"/api/queues",
		"/api/notifications",
		"/api/connected-systems",
		"/api/tokens",
		"/api/auth/me",
	}

	for _, path := range staffOnly {
		t.Run(strings.TrimPrefix(path, "/api/"), func(t *testing.T) {
			code := doJSON(t, client, http.MethodGet, e.api.URL+path, nil, nil)
			if code != http.StatusUnauthorized {
				t.Errorf("a portal session reached %s and got %d, want 401", path, code)
			}
		})
	}
}

// TestStaffSessionCannotReachPortal is the mirror image: the staff cookie is
// not a portal cookie, so the two surfaces cannot be crossed in either
// direction.
func TestStaffSessionCannotReachPortal(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	// e.client holds the staff session from signIn.
	code := doJSON(t, e.client, http.MethodGet, e.api.URL+"/api/portal/requests", nil, nil)
	if code != http.StatusUnauthorized {
		t.Errorf("a staff session reached the portal and got %d, want 401", code)
	}
}

// TestPortalCannotSeeAnotherCitizensRequest is the scoping guarantee. Quoting
// somebody else's reference must be indistinguishable from quoting one that
// does not exist, or the endpoint becomes a way to confirm which references
// are real.
func TestPortalCannotSeeAnotherCitizensRequest(t *testing.T) {
	e := newEnv(t)

	// A staff agent files a request for one citizen.
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)
	victim := e.seedRequest(t)
	if err := e.db.Create(&domain.ContactIdentity{
		ContactID: victim.ContactID, Provider: domain.ProviderC2,
		ExternalID: "citizen-victim", Verified: true,
	}).Error; err != nil {
		t.Fatalf("link victim identity: %v", err)
	}

	// A different citizen signs in and goes looking.
	attacker := e.portalSignIn(t, "citizen-attacker")

	code := doJSON(t, attacker, http.MethodGet,
		e.api.URL+"/api/portal/requests/"+victim.Reference, nil, nil)
	if code != http.StatusNotFound {
		t.Errorf("GET another citizen's request -> %d, want 404", code)
	}

	// Nor can they act on it. Each action gets a body its own handler accepts,
	// so a rejected field cannot mask the ownership check being the thing that
	// refuses.
	actions := map[string]map[string]any{
		"comments": {"body": "mine now"},
		"cancel":   {"reason": "not mine"},
		"rating":   {"score": 5},
	}
	for action, body := range actions {
		code := doJSON(t, attacker, http.MethodPost,
			e.api.URL+"/api/portal/requests/"+victim.Reference+"/"+action, body, nil)
		if code != http.StatusNotFound {
			t.Errorf("POST %s on another citizen's request -> %d, want 404", action, code)
		}
	}

	// And their own list stays empty.
	var mine struct {
		Items []json.RawMessage `json:"items"`
	}
	doJSON(t, attacker, http.MethodGet, e.api.URL+"/api/portal/requests", nil, &mine)
	if len(mine.Items) != 0 {
		t.Errorf("attacker sees %d request(s), want 0", len(mine.Items))
	}
}

// TestPortalHidesInternalComments proves the citizen view is a projection, not
// the domain object: staff must be able to talk frankly on a request without it
// appearing on the reporter's screen.
func TestPortalHidesInternalComments(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	req := e.seedRequest(t)
	if err := e.db.Create(&domain.ContactIdentity{
		ContactID: req.ContactID, Provider: domain.ProviderC2,
		ExternalID: "citizen-owner", Verified: true,
	}).Error; err != nil {
		t.Fatalf("link identity: %v", err)
	}

	e.post("/api/requests/"+req.ID+"/comments",
		map[string]any{"body": "SECRET: possible insurance claim, do not tell them", "visibility": "internal"}, nil)
	e.post("/api/requests/"+req.ID+"/comments",
		map[string]any{"body": "A crew is scheduled for Tuesday.", "visibility": "citizen"}, nil)

	client := e.portalSignIn(t, "citizen-owner")

	var view struct {
		Updates []struct {
			Body string `json:"body"`
		} `json:"updates"`
	}
	if code := doJSON(t, client, http.MethodGet,
		e.api.URL+"/api/portal/requests/"+req.Reference, nil, &view); code != 200 {
		t.Fatalf("the owner cannot see their own request: %d", code)
	}

	var sawPublic bool
	for _, u := range view.Updates {
		if strings.Contains(u.Body, "SECRET") {
			t.Fatalf("an internal comment leaked to the citizen: %q", u.Body)
		}
		if strings.Contains(u.Body, "crew is scheduled") {
			sawPublic = true
		}
	}
	if !sawPublic {
		t.Error("the citizen-visible comment was not shown to the citizen")
	}
}

// TestPortalReportAndFollowUp walks the feature end to end: browse the
// catalogue, report a problem, then reply on it.
func TestPortalReportAndFollowUp(t *testing.T) {
	e := newEnv(t)
	client := e.portalSignIn(t, "citizen-reporter")

	var catalog struct {
		Items []struct {
			ID       string `json:"id"`
			Code     string `json:"code"`
			Name     string `json:"name"`
			Requires bool   `json:"requiresLocation"`
			Fields   []struct {
				Key string `json:"key"`
			} `json:"fields"`
		} `json:"items"`
	}
	if code := doJSON(t, client, http.MethodGet, e.api.URL+"/api/portal/catalog", nil, &catalog); code != 200 {
		t.Fatalf("catalog -> %d", code)
	}
	if len(catalog.Items) == 0 {
		t.Fatal("the catalogue is empty; a citizen has nothing to report")
	}

	var pothole string
	for _, c := range catalog.Items {
		if c.Code == "POTHOLE" {
			pothole = c.ID
			if len(c.Fields) == 0 {
				t.Error("the pothole service exposes no intake fields")
			}
		}
	}
	if pothole == "" {
		t.Fatal("POTHOLE is not in the public catalogue")
	}

	var created struct {
		Reference   string `json:"reference"`
		StatusLabel string `json:"statusLabel"`
		Open        bool   `json:"open"`
	}
	code := doJSON(t, client, http.MethodPost, e.api.URL+"/api/portal/requests", map[string]any{
		"serviceTypeId": pothole,
		"subject":       "Pothole outside my house",
		"description":   "Deep and getting worse.",
		"address1":      "44 Elm Street",
		"formData":      map[string]any{"size": "Medium"},
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("report -> %d", code)
	}
	if !strings.HasPrefix(created.Reference, "SR-") {
		t.Errorf("reference %q is not quotable", created.Reference)
	}
	if !created.Open {
		t.Error("a new report should be open")
	}

	// The reporter can reply on it.
	code = doJSON(t, client, http.MethodPost,
		e.api.URL+"/api/portal/requests/"+created.Reference+"/comments",
		map[string]any{"body": "It has got worse since I reported it."}, nil)
	if code != http.StatusCreated {
		t.Fatalf("reply -> %d", code)
	}

	var view struct {
		Updates []struct {
			Body string `json:"body"`
			Mine bool   `json:"mine"`
		} `json:"updates"`
	}
	doJSON(t, client, http.MethodGet, e.api.URL+"/api/portal/requests/"+created.Reference, nil, &view)

	var sawMine bool
	for _, u := range view.Updates {
		if u.Mine && strings.Contains(u.Body, "got worse") {
			sawMine = true
		}
	}
	if !sawMine {
		t.Error("the citizen's own reply is missing from their history")
	}

	// And it reaches the staff console as a real request.
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)
	staffView, err := e.lookupByReference(created.Reference)
	if err != nil {
		t.Fatalf("staff cannot see the reported request: %v", err)
	}
	if staffView.QueueID == "" {
		t.Error("a portal report was not routed to a queue")
	}
	if staffView.Source != domain.SourceC2Card {
		t.Errorf("source = %q, want %q", staffView.Source, domain.SourceC2Card)
	}
}

// TestPortalRatingClosesTheCSATLoop covers the gap the survey left: the
// scheduler could send a satisfaction request, but nothing could record an
// answer, so the report could only ever read zero.
func TestPortalRatingClosesTheCSATLoop(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	req := e.seedRequest(t)
	if err := e.db.Create(&domain.ContactIdentity{
		ContactID: req.ContactID, Provider: domain.ProviderC2,
		ExternalID: "citizen-rater", Verified: true,
	}).Error; err != nil {
		t.Fatalf("link identity: %v", err)
	}

	// Resolving is what makes a request ratable.
	e.post("/api/requests/"+req.ID+"/transition",
		map[string]any{"to": "in_progress"}, nil)
	e.post("/api/requests/"+req.ID+"/transition",
		map[string]any{"to": "resolved", "resolutionCode": "repaired", "note": "Filled."}, nil)

	client := e.portalSignIn(t, "citizen-rater")

	code := doJSON(t, client, http.MethodPost,
		e.api.URL+"/api/portal/requests/"+req.Reference+"/rating",
		map[string]any{"score": 4, "comment": "Quick, thanks."}, nil)
	if code != http.StatusOK {
		t.Fatalf("rating -> %d", code)
	}

	var stored domain.Request
	if err := e.db.First(&stored, "id = ?", req.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.CSATScore == nil || *stored.CSATScore != 4 {
		t.Fatalf("csatScore = %v, want 4", stored.CSATScore)
	}

	// Rating twice is refused, so a score cannot be revised after the fact.
	code = doJSON(t, client, http.MethodPost,
		e.api.URL+"/api/portal/requests/"+req.Reference+"/rating",
		map[string]any{"score": 1}, nil)
	if code != http.StatusConflict {
		t.Errorf("second rating -> %d, want 409", code)
	}
}

// TestPortalCannotReportInternalService checks that only the public catalogue
// is reportable, even if an internal service type's id is known.
func TestPortalCannotReportInternalService(t *testing.T) {
	e := newEnv(t)

	var internal domain.ServiceType
	if err := e.db.Where("code = ?", "GENERAL").First(&internal).Error; err != nil {
		t.Fatalf("load service type: %v", err)
	}
	e.db.Model(&internal).UpdateColumn("public_visible", false)

	client := e.portalSignIn(t, "citizen-probe")
	code := doJSON(t, client, http.MethodPost, e.api.URL+"/api/portal/requests", map[string]any{
		"serviceTypeId": internal.ID, "subject": "Trying it on",
	}, nil)
	if code != http.StatusBadRequest {
		t.Errorf("reporting against a non-public service -> %d, want 400", code)
	}
}

// A citizen signing out must be returned to the portal, not to the staff
// console.
//
// C2 matches post_logout_redirect_uri exactly, the same as redirect URIs, so
// sending the console's value from the portal is not a cosmetic problem: C2
// rejects the request. The citizen sees an error page from an unfamiliar host,
// and their portal session is already gone, so they cannot retry into anything
// useful. This was the original bug — one shared value for two origins.
func TestPortalLogoutReturnsToThePortal(t *testing.T) {
	e := newEnv(t)
	client := e.portalSignIn(t, "citizen-signing-out")

	var out struct {
		Status        string `json:"status"`
		EndSessionURL string `json:"endSessionUrl"`
	}
	if code := doJSON(t, client, http.MethodPost,
		e.api.URL+"/api/portal/auth/logout", nil, &out); code != 200 {
		t.Fatalf("POST /portal/auth/logout -> %d", code)
	}
	if out.Status != "signed_out" {
		t.Errorf("status = %q, want signed_out", out.Status)
	}

	u, err := url.Parse(out.EndSessionURL)
	if err != nil || out.EndSessionURL == "" {
		t.Fatalf("endSessionUrl = %q (%v)", out.EndSessionURL, err)
	}
	got := u.Query().Get("post_logout_redirect_uri")
	if want := e.cfg.C2.PortalPostLogoutRedirectURL; got != want {
		t.Errorf("post_logout_redirect_uri = %q, want the portal's %q", got, want)
	}
	if got == e.cfg.C2.PostLogoutRedirectURL {
		t.Error("the portal is sending the staff console's return address")
	}

	// And the session is genuinely over on our side, whatever C2 does next.
	if code := doJSON(t, client, http.MethodGet, e.api.URL+"/api/portal/me", nil, nil); code != 401 {
		t.Errorf("GET /portal/me after sign-out -> %d, want 401", code)
	}
}

// A request a citizen files through the portal is visible to that citizen and
// to staff, and to nobody else.
//
// The existing isolation test covers a request an agent filed. This one starts
// where the user does — the portal's own create path — because that is the
// object whose ownership was never written down by a member of staff, and
// because a citizen filing on their own behalf is now the common case.
func TestCitizenCreatedRequestIsVisibleOnlyToTheOwnerAndStaff(t *testing.T) {
	e := newEnv(t)

	// The owner files it themselves through the portal.
	owner := e.portalSignIn(t, "citizen-owner")
	var created struct {
		Reference string `json:"reference"`
	}
	if code := doJSON(t, owner, http.MethodPost, e.api.URL+"/api/portal/requests",
		map[string]any{
			"serviceTypeId": catalogID(t, e, owner, "POTHOLE"),
			"subject":       "Pothole outside 14 Elm Street",
			"description":   "Deep enough to damage a wheel.",
			"address1":      "14 Elm Street",
			"formData":      map[string]any{"size": "Medium"},
		}, &created); code != http.StatusCreated {
		t.Fatalf("citizen create -> %d", code)
	}
	if created.Reference == "" {
		t.Fatal("no reference returned")
	}

	// 1. The owner can read it back, with its detail.
	var mine struct {
		Reference string `json:"reference"`
		Subject   string `json:"subject"`
	}
	if code := doJSON(t, owner, http.MethodGet,
		e.api.URL+"/api/portal/requests/"+created.Reference, nil, &mine); code != http.StatusOK {
		t.Fatalf("owner GET own request -> %d", code)
	}
	if mine.Reference != created.Reference {
		t.Errorf("owner got reference %q, want %q", mine.Reference, created.Reference)
	}

	// 2. Another citizen cannot — not the request, not its detail, not by
	//    listing, and not by acting on it.
	other := e.portalSignIn(t, "citizen-stranger")

	if code := doJSON(t, other, http.MethodGet,
		e.api.URL+"/api/portal/requests/"+created.Reference, nil, nil); code != http.StatusNotFound {
		t.Errorf("stranger GET -> %d, want 404", code)
	}
	for action, body := range map[string]map[string]any{
		"comments": {"body": "adding myself to this"},
		"cancel":   {"reason": "no longer needed"},
		"rating":   {"score": 1},
	} {
		if code := doJSON(t, other, http.MethodPost,
			e.api.URL+"/api/portal/requests/"+created.Reference+"/"+action, body, nil,
		); code != http.StatusNotFound {
			t.Errorf("stranger POST %s -> %d, want 404", action, code)
		}
	}
	var strangerList struct {
		Items []struct {
			Reference string `json:"reference"`
		} `json:"items"`
	}
	doJSON(t, other, http.MethodGet, e.api.URL+"/api/portal/requests", nil, &strangerList)
	for _, item := range strangerList.Items {
		if item.Reference == created.Reference {
			t.Error("the owner's request appeared in another citizen's list")
		}
	}

	// 3. Signed out, it is not readable at all.
	if code := doJSON(t, newJarClient(), http.MethodGet,
		e.api.URL+"/api/portal/requests/"+created.Reference, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("anonymous GET -> %d, want 401", code)
	}

	// 4. Staff can see it. This half matters as much as the isolation: a
	//    request nobody at the City can read is not a service request.
	e.signIn("staff-admin", "admin@city.example", domain.RoleAdmin)
	var staffList struct {
		Items []struct {
			ID        string `json:"id"`
			Reference string `json:"reference"`
		} `json:"items"`
	}
	if code := e.get("/api/requests?q="+created.Reference, &staffList); code != http.StatusOK {
		t.Fatalf("staff list -> %d", code)
	}
	var id string
	for _, item := range staffList.Items {
		if item.Reference == created.Reference {
			id = item.ID
		}
	}
	if id == "" {
		t.Fatalf("staff cannot find %s: %+v", created.Reference, staffList.Items)
	}
	var full map[string]any
	if code := e.get("/api/requests/"+id, &full); code != http.StatusOK {
		t.Errorf("staff GET the request -> %d, want 200", code)
	}
}

// The refusal must not double as a way to discover which references exist.
//
// A 404 for somebody else's request and a 404 for a reference that was never
// issued have to be identical — same status, same body. Anything that differs
// turns the endpoint into an oracle: a citizen could walk the reference
// sequence and learn how many requests the City holds and for whom.
func TestPortalRefusalDoesNotRevealWhetherARequestExists(t *testing.T) {
	e := newEnv(t)

	owner := e.portalSignIn(t, "citizen-owner")
	var created struct {
		Reference string `json:"reference"`
	}
	if code := doJSON(t, owner, http.MethodPost, e.api.URL+"/api/portal/requests",
		map[string]any{
			"serviceTypeId": catalogID(t, e, owner, "GENERAL"),
			"subject":       "A question",
			"description":   "About recycling.",
		}, &created); code != http.StatusCreated {
		t.Fatalf("create -> %d", code)
	}

	stranger := e.portalSignIn(t, "citizen-stranger")

	realBody, realCode := rawGet(t, stranger, e.api.URL+"/api/portal/requests/"+created.Reference)
	fakeBody, fakeCode := rawGet(t, stranger, e.api.URL+"/api/portal/requests/SR-2026-999999")

	if realCode != http.StatusNotFound || fakeCode != http.StatusNotFound {
		t.Fatalf("codes were %d (real) and %d (invented), want 404 for both", realCode, fakeCode)
	}

	// Two fields legitimately differ and carry nothing about existence:
	// `instance` echoes the path that was asked for, and `requestId` is a
	// per-request correlation id. Everything else must match exactly — a
	// different detail string, code, or title for a real reference is what
	// would turn the refusal into an oracle.
	if got, want := scrubProblem(t, realBody), scrubProblem(t, fakeBody); got != want {
		t.Errorf("the refusals differ once the echoed path and correlation id are"+
			" set aside, so the endpoint confirms which references are real:\n"+
			" real:     %s\n invented: %s", got, want)
	}
}
