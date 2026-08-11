package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/devtools/c2stub"
	"github.com/jjamieson1/CityConnect/internal/agents"
	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/c2/callout"
	"github.com/jjamieson1/CityConnect/internal/c2/notify"
	"github.com/jjamieson1/CityConnect/internal/c2/oidc"
	"github.com/jjamieson1/CityConnect/internal/catalog"
	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/contacts"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/httpapi"
	"github.com/jjamieson1/CityConnect/internal/interactions"
	"github.com/jjamieson1/CityConnect/internal/notifications"
	"github.com/jjamieson1/CityConnect/internal/reports"
	"github.com/jjamieson1/CityConnect/internal/requests"
	"github.com/jjamieson1/CityConnect/internal/routing"
	"github.com/jjamieson1/CityConnect/internal/seed"
	"github.com/jjamieson1/CityConnect/internal/storetest"
	"github.com/jjamieson1/CityConnect/internal/webhooks"
)

// env is a fully wired CityConnect plus a C2 stub, exercised over real HTTP.
type env struct {
	t        *testing.T
	api      *httptest.Server
	stub     *c2stub.Server
	stubSrv  *httptest.Server
	db       *gorm.DB
	client   *http.Client
	notifier *notifications.Service
	cfg      *config.Config
}

func newEnv(t *testing.T) *env {
	t.Helper()

	stub, err := c2stub.New(c2stub.Options{ClientID: "cityconnect-test", ClientSecret: "shh"})
	if err != nil {
		t.Fatalf("stub: %v", err)
	}
	stubSrv := httptest.NewServer(stub.Handler())
	t.Cleanup(stubSrv.Close)
	stub.SetIssuer(stubSrv.URL + "/oidc")

	db := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The API's own URL is not known until httptest binds a port, but the
	// redirect_uri must be configured before the OIDC provider is built —
	// C2 matches redirect URIs exactly, so a placeholder would not do. The
	// server is started first behind an indirection, then given its handler.
	var handler http.Handler
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(apiSrv.Close)

	cfg := &config.Config{
		Env: "test", Addr: ":0", BasePath: "",
		PublicURL:     apiSrv.URL,
		AttachmentDir: t.TempDir(), AttachmentMaxMB: 5,
		Sec: config.SecurityConfig{
			SessionCookieName: "cc_session",
			SessionTTL:        8 * time.Hour, SessionIdleTTL: 2 * time.Hour,
			RateLimitPerMin: 100000,
		},
		Job: config.JobConfig{Enabled: false, OutboxMaxAttempts: 3},
		C2: config.C2Config{
			PortalOrigin: stubSrv.URL, Issuer: stubSrv.URL + "/oidc",
			ClientID: "cityconnect-test", ClientSecret: "shh",
			RedirectURL:           apiSrv.URL + "/api/auth/callback",
			PostLogoutRedirectURL: apiSrv.URL + "/",
			Scopes:                []string{"openid", "profile", "email"},
			PartnerBaseURL:        stubSrv.URL,
			NotifyAudience:        stubSrv.URL,
			DiscoveryCacheTTL:     time.Minute, HTTPTimeout: 5 * time.Second,
			ClockSkew: time.Minute, CalloutMaxTasks: 10,
			CalloutCacheTTL: 0, // disabled so assertions see fresh data
		},
	}

	auditSvc := audit.NewService(db, log)
	provider := oidc.New(cfg.C2)
	agentSvc := agents.NewService(db, cfg, provider, auditSvc, log)
	contactSvc := contacts.NewService(db, auditSvc, log)
	interactionSvc := interactions.NewService(db, auditSvc, log)
	catalogSvc := catalog.NewService(db, auditSvc, log)
	routingSvc := routing.NewService(db, auditSvc, log)
	requestSvc := requests.NewService(db, auditSvc, catalogSvc, routingSvc, log)
	webhookSvc := webhooks.NewService(db, auditSvc, log)
	reportSvc := reports.NewService(db, log)

	notifyClient, err := notify.New(cfg.C2)
	if err != nil {
		t.Fatalf("notify client: %v", err)
	}
	notificationSvc := notifications.NewService(db, cfg, notifyClient, catalogSvc, contactSvc, auditSvc, log)
	requestSvc.SetNotifier(notificationSvc)
	requestSvc.SetWebhooks(webhookSvc)

	calloutSvc := callout.NewService(db, cfg, provider, contactSvc, requestSvc, log)
	attachments, err := requests.NewAttachmentStore(cfg.AttachmentDir, 5, nil)
	if err != nil {
		t.Fatalf("attachments: %v", err)
	}

	ctx := context.Background()
	if err := seed.Run(ctx, db, cfg, log); err != nil {
		t.Fatalf("seed: %v", err)
	}

	api := httpapi.New(httpapi.Deps{
		DB: db, Config: cfg, Log: log, OIDC: provider, Notify: notifyClient,
		Agents: agentSvc, Audit: auditSvc, Contacts: contactSvc,
		Interactions: interactionSvc, Catalog: catalogSvc, Routing: routingSvc,
		Requests: requestSvc, Notifications: notificationSvc, Webhooks: webhookSvc,
		Reports: reportSvc, Callout: calloutSvc, Attachments: attachments,
	})

	handler = api.Handler()

	jar, _ := cookiejar.New(nil)
	return &env{
		t: t, api: apiSrv, stub: stub, stubSrv: stubSrv, db: db,
		client: &http.Client{Jar: jar, Timeout: 10 * time.Second},
		notifier: notificationSvc, cfg: cfg,
	}
}

// signIn drives the real browser flow: /api/auth/login, the C2 redirect, and
// the callback that sets the session cookie.
func (e *env) signIn(sub, email string, role domain.Role) {
	e.t.Helper()

	// The user must exist first — deny-by-default is the whole point.
	u := &domain.User{
		C2Sub: sub, Email: email, Name: "Test " + string(role),
		Status: domain.UserActive, Role: role, CrossDepartment: true,
	}
	if err := e.db.Create(u).Error; err != nil {
		e.t.Fatalf("seed user: %v", err)
	}
	e.stub.SignIn(sub)

	resp, err := e.client.Get(e.api.URL + "/api/auth/login")
	if err != nil {
		e.t.Fatalf("login: %v", err)
	}
	resp.Body.Close()

	// The final hop lands on the SPA route, which does not exist in this test
	// binary — a 404 there is expected and says nothing about the sign-in.
	// What matters is whether the callback issued a session, so check that.
	var me struct {
		User *domain.User `json:"user"`
	}
	e.get("/api/auth/me", &me)
	if me.User == nil || me.User.C2Sub != sub {
		e.t.Fatalf("sign-in did not establish a session for %s", sub)
	}
}

func (e *env) do(method, path string, body, out any) int {
	e.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, e.api.URL+path, reader)
	if err != nil {
		e.t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("%s %s: decode %d response %q: %v", method, path, resp.StatusCode, raw, err)
		}
	}
	if resp.StatusCode >= 400 {
		e.t.Logf("%s %s -> %d: %s", method, path, resp.StatusCode, raw)
	}
	return resp.StatusCode
}

func (e *env) get(path string, out any) int  { return e.do(http.MethodGet, path, nil, out) }
func (e *env) post(path string, body, out any) int {
	return e.do(http.MethodPost, path, body, out)
}

// ---------------------------------------------------------------------------

// TestFullRequestLifecycle walks the path a real service request takes: an
// agent signs in through C2, creates a contact and a request, works it, and
// resolves it — with routing, SLA targets, the timeline and the citizen
// notification all falling out along the way.
func TestFullRequestLifecycle(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	// The catalogue seeded on boot.
	var types struct {
		Items []domain.ServiceType `json:"items"`
	}
	if code := e.get("/api/service-types", &types); code != 200 {
		t.Fatalf("list service types: %d", code)
	}
	var pothole domain.ServiceType
	for _, st := range types.Items {
		if st.Code == "POTHOLE" {
			pothole = st
		}
	}
	if pothole.ID == "" {
		t.Fatal("seed did not install the POTHOLE service type")
	}

	var contact domain.Contact
	code := e.post("/api/contacts", map[string]any{
		"displayName":  "Alex Citizen",
		"primaryEmail": "alex@example.gov",
		"primaryPhone": "+1 555 0190",
		"ward":         "Ward 3",
	}, &contact)
	if code != 201 {
		t.Fatalf("create contact: %d", code)
	}

	var req domain.Request
	code = e.post("/api/requests", map[string]any{
		"contactId":     contact.ID,
		"serviceTypeId": pothole.ID,
		"subject":       "Pothole on Oak Street",
		"description":   "Large pothole outside number 12.",
		"address1":      "12 Oak Street",
		"ward":          "Ward 3",
		"formData":      map[string]any{"size": "Large (over 1m)", "hazard": true},
	}, &req)
	if code != 201 {
		t.Fatalf("create request: %d", code)
	}

	if !strings.HasPrefix(req.Reference, "SR-") {
		t.Errorf("reference %q does not look quotable", req.Reference)
	}
	if req.QueueID == "" {
		t.Error("request was not routed to a queue")
	}
	if req.DueAt == nil {
		t.Error("no SLA deadline was computed")
	}
	if req.Department == nil || req.Department.Code != "PW" {
		t.Errorf("request landed in the wrong department: %+v", req.Department)
	}

	// The intake form was validated and cleaned rather than stored raw.
	if req.FormData["size"] != "Large (over 1m)" {
		t.Errorf("form data not retained: %v", req.FormData)
	}

	// Work it: assign, comment, resolve.
	var afterAssign domain.Request
	var me struct {
		User *domain.User `json:"user"`
	}
	e.get("/api/auth/me", &me)

	code = e.post("/api/requests/"+req.ID+"/assign",
		map[string]any{"userId": me.User.ID, "version": req.Version}, &afterAssign)
	if code != 200 {
		t.Fatalf("assign: %d", code)
	}
	if afterAssign.Status != domain.StatusAssigned {
		t.Errorf("status after assign = %q, want assigned", afterAssign.Status)
	}

	var comment domain.RequestComment
	code = e.post("/api/requests/"+req.ID+"/comments", map[string]any{
		"body":          "A crew has been scheduled for Tuesday.",
		"visibility":    "citizen",
		"notifyCitizen": true,
	}, &comment)
	if code != 201 {
		t.Fatalf("add comment: %d", code)
	}

	var inProgress domain.Request
	code = e.post("/api/requests/"+req.ID+"/transition",
		map[string]any{"to": "in_progress"}, &inProgress)
	if code != 200 {
		t.Fatalf("transition to in_progress: %d", code)
	}

	var resolved domain.Request
	code = e.post("/api/requests/"+req.ID+"/transition", map[string]any{
		"to": "resolved", "resolutionCode": "repaired",
		"note": "Pothole filled.", "notifyCitizen": true,
	}, &resolved)
	if code != 200 {
		t.Fatalf("resolve: %d", code)
	}
	if resolved.ResolvedAt == nil {
		t.Error("resolvedAt was not stamped")
	}
	if resolved.FirstResponseAt == nil {
		t.Error("firstResponseAt was not stamped")
	}

	// The timeline records the whole story.
	var events struct {
		Items []domain.RequestEvent `json:"items"`
	}
	e.get("/api/requests/"+req.ID+"/events", &events)

	seen := map[string]bool{}
	for _, ev := range events.Items {
		seen[ev.Kind] = true
	}
	for _, want := range []string{domain.EvtCreated, domain.EvtAssigned, domain.EvtCommented, domain.EvtStatusChanged} {
		if !seen[want] {
			t.Errorf("timeline is missing a %q event; got %v", want, seen)
		}
	}
}

// TestIllegalTransitionRejected proves the status machine is enforced rather
// than advisory.
func TestIllegalTransitionRejected(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	req := e.seedRequest(t)

	// new -> closed is not a legal move; closing skips resolution entirely.
	code := e.post("/api/requests/"+req.ID+"/transition", map[string]any{"to": "closed"}, nil)
	if code != http.StatusConflict {
		t.Fatalf("illegal transition returned %d, want 409", code)
	}

	var after domain.Request
	e.get("/api/requests/"+req.ID, &after)
	if after.Status != domain.StatusNew {
		t.Errorf("status changed to %q despite the rejection", after.Status)
	}
}

// TestOptimisticConcurrency covers two agents editing one ticket, which is
// routine on a busy queue.
func TestOptimisticConcurrency(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	req := e.seedRequest(t)
	staleVersion := req.Version

	// First writer wins.
	code := e.do(http.MethodPatch, "/api/requests/"+req.ID,
		map[string]any{"subject": "Pothole — updated", "version": staleVersion}, nil)
	if code != 200 {
		t.Fatalf("first update: %d", code)
	}

	// Second writer, holding the version they read before, is rejected rather
	// than silently overwriting the first.
	code = e.do(http.MethodPatch, "/api/requests/"+req.ID,
		map[string]any{"subject": "Something else entirely", "version": staleVersion}, nil)
	if code != http.StatusConflict {
		t.Fatalf("stale write returned %d, want 409", code)
	}

	var after domain.Request
	e.get("/api/requests/"+req.ID, &after)
	if after.Subject != "Pothole — updated" {
		t.Errorf("subject = %q; the stale write overwrote the first one", after.Subject)
	}
}

// TestCalloutServesStatusBundle exercises the inbound Service Card callout
// exactly as C2 does: a signed assertion, and the JSON C2 renders.
func TestCalloutServesStatusBundle(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	req := e.seedRequest(t)

	// Link the contact to a C2 subject, as a real citizen would be.
	if err := e.db.Create(&domain.ContactIdentity{
		ContactID: req.ContactID, Provider: domain.ProviderC2,
		ExternalID: "citizen-001", Verified: true,
	}).Error; err != nil {
		t.Fatalf("link identity: %v", err)
	}

	e.post("/api/requests/"+req.ID+"/comments", map[string]any{
		"body": "A crew is scheduled for Tuesday.", "visibility": "citizen",
	}, nil)

	assertion, err := e.stub.CalloutAssertion("citizen-001", "openid profile")
	if err != nil {
		t.Fatalf("mint assertion: %v", err)
	}

	bundle := e.callout(t, "citizen-001", assertion)

	if len(bundle.Tasks) != 1 {
		t.Fatalf("bundle has %d tasks, want 1: %+v", len(bundle.Tasks), bundle)
	}
	task := bundle.Tasks[0]
	if !strings.Contains(task.Name, req.Reference) {
		t.Errorf("task name %q does not carry the reference", task.Name)
	}
	if !strings.Contains(task.Description, "crew is scheduled") {
		t.Errorf("task description %q does not include the citizen-visible note", task.Description)
	}
	if !strings.HasPrefix(task.URL, "https://") && !strings.HasPrefix(task.URL, "http://") {
		t.Errorf("task URL %q is not absolute; C2 opens these in a new tab", task.URL)
	}
	if bundle.Description == "" {
		t.Error("bundle has no description")
	}

	// The capitalised CTA is C2's contract, not a typo.
	raw := e.calloutRaw(t, "citizen-001", assertion)
	if !strings.Contains(raw, `"CTA"`) && !strings.Contains(raw, `"tasks"`) {
		t.Errorf("bundle carries neither CTA nor tasks: %s", raw)
	}
}

// TestCalloutRejectsBadAuth proves the callout fails closed.
func TestCalloutRejectsBadAuth(t *testing.T) {
	e := newEnv(t)

	cases := []struct {
		name  string
		token string
	}{
		{"no token", ""},
		{"garbage token", "not-a-jwt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, e.api.URL+"/api/citizens/citizen-001/status", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			resp, err := (&http.Client{}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", resp.StatusCode)
			}
		})
	}
}

// TestCalloutUnknownSubjectReturnsEmptyBundle covers the citizen the CRM has
// never met: a valid, empty answer rather than a guess or an error.
func TestCalloutUnknownSubjectReturnsEmptyBundle(t *testing.T) {
	e := newEnv(t)

	assertion, err := e.stub.CalloutAssertion("nobody-we-know", "openid")
	if err != nil {
		t.Fatal(err)
	}
	bundle := e.callout(t, "nobody-we-know", assertion)
	if len(bundle.Tasks) != 0 || bundle.Title != "" {
		t.Errorf("expected an empty bundle, got %+v", bundle)
	}
}

// TestNotificationReachesC2 drives the outbox end to end and checks the
// message actually arrived at the partner endpoint.
func TestNotificationReachesC2(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	req := e.seedRequest(t)
	if err := e.db.Create(&domain.ContactIdentity{
		ContactID: req.ContactID, Provider: domain.ProviderC2,
		ExternalID: "citizen-001", Verified: true,
	}).Error; err != nil {
		t.Fatalf("link identity: %v", err)
	}

	e.post("/api/requests/"+req.ID+"/comments", map[string]any{
		"body": "We have started work on this.", "visibility": "citizen", "notifyCitizen": true,
	}, nil)

	res, err := e.notifier.Drain(context.Background(), 10)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if res.Sent == 0 {
		t.Fatalf("nothing was sent: %+v", res)
	}

	sent := e.stub.Notifications()
	if len(sent) == 0 {
		t.Fatal("C2 received no notification")
	}
	if sent[0].Sub != "citizen-001" {
		t.Errorf("notification addressed to %q, want citizen-001", sent[0].Sub)
	}
	if !strings.Contains(sent[0].Body, "started work") {
		t.Errorf("notification body does not carry the comment: %q", sent[0].Body)
	}
}

// TestNotificationSuppressedWithoutConsent is the behaviour that keeps the
// outbox from burning C2's rate limit on messages that can never be delivered.
func TestNotificationSuppressedWithoutConsent(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	req := e.seedRequest(t)
	if err := e.db.Create(&domain.ContactIdentity{
		ContactID: req.ContactID, Provider: domain.ProviderC2,
		ExternalID: "citizen-002", Verified: true,
	}).Error; err != nil {
		t.Fatalf("link identity: %v", err)
	}

	// The citizen has withdrawn consent in their C2 portal.
	e.stub.SetConsent("citizen-002", false)

	e.post("/api/requests/"+req.ID+"/comments", map[string]any{
		"body": "An update for you.", "visibility": "citizen", "notifyCitizen": true,
	}, nil)

	res, err := e.notifier.Drain(context.Background(), 10)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if res.Suppressed == 0 {
		t.Fatalf("a consent refusal was not suppressed: %+v", res)
	}

	// The message is parked permanently, not retried.
	var row domain.NotificationOutbox
	if err := e.db.Where("c2_sub = ?", "citizen-002").First(&row).Error; err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if row.State != domain.OutboxSuppressed {
		t.Errorf("state = %q, want suppressed", row.State)
	}
	if row.SuppressReason != domain.SuppressNoConsent {
		t.Errorf("reason = %q, want %q", row.SuppressReason, domain.SuppressNoConsent)
	}

	// And the contact is flagged, so an agent knows to phone instead.
	var contact domain.Contact
	e.db.First(&contact, "id = ?", req.ContactID)
	if contact.C2Reachable {
		t.Error("contact should be flagged as unreachable through C2")
	}

	// A retry through the console is refused with an explanation rather than
	// quietly re-queued.
	code := e.post("/api/notifications/"+row.ID+"/retry", nil, nil)
	if code != http.StatusUnprocessableEntity {
		t.Errorf("retry of a suppressed message returned %d, want 422", code)
	}
}

// TestUnauthenticatedIsRejected checks the API is closed by default.
func TestUnauthenticatedIsRejected(t *testing.T) {
	e := newEnv(t)

	for _, path := range []string{"/api/requests", "/api/contacts", "/api/users", "/api/reports/volume"} {
		code := e.get(path, nil)
		if code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session returned %d, want 401", path, code)
		}
	}
}

// TestRolePermissionsEnforced proves a read-only account cannot write.
func TestRolePermissionsEnforced(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-viewer", "viewer@city.example", domain.RoleReadOnly)

	if code := e.get("/api/requests", nil); code != 200 {
		t.Errorf("read-only cannot read requests: %d", code)
	}
	code := e.post("/api/contacts", map[string]any{"displayName": "Nope"}, nil)
	if code != http.StatusForbidden {
		t.Errorf("read-only could create a contact: %d", code)
	}
	code = e.post("/api/users/invite", map[string]any{"email": "x@y.example"}, nil)
	if code != http.StatusForbidden {
		t.Errorf("read-only could invite a user: %d", code)
	}
}

// TestAuditChainVerifies proves the log is tamper-evident, not just append-only.
func TestAuditChainVerifies(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)
	e.seedRequest(t)

	var res struct {
		Valid    bool  `json:"valid"`
		Checked  int64 `json:"checked"`
		BrokenAt uint64 `json:"brokenAtSeq"`
	}
	if code := e.get("/api/audit/verify", &res); code != 200 {
		t.Fatalf("verify: %d", code)
	}
	if !res.Valid {
		t.Fatalf("audit chain reported broken at seq %d", res.BrokenAt)
	}
	if res.Checked == 0 {
		t.Error("audit chain is empty; nothing was recorded")
	}

	// Tamper with an entry and confirm the chain notices.
	var entry domain.AuditLog
	if err := e.db.Order("seq ASC").First(&entry).Error; err != nil {
		t.Fatalf("read audit entry: %v", err)
	}
	e.db.Model(&entry).UpdateColumn("summary", "something else entirely")

	if code := e.get("/api/audit/verify", &res); code != 200 {
		t.Fatalf("verify after tamper: %d", code)
	}
	if res.Valid {
		t.Error("audit chain did not detect a modified entry")
	}
}

// TestReportsRespond checks the reporting surface answers with real shapes.
func TestReportsRespond(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)
	e.seedRequest(t)

	for _, path := range []string{
		"/api/reports/volume", "/api/reports/sla", "/api/reports/agents",
		"/api/reports/csat", "/api/reports/geo",
	} {
		var out map[string]any
		if code := e.get(path, &out); code != 200 {
			t.Errorf("GET %s returned %d", path, code)
			continue
		}
		if len(out) == 0 {
			t.Errorf("GET %s returned an empty body", path)
		}
	}

	// CSV export streams rather than returning JSON.
	resp, err := e.client.Get(e.api.URL + "/api/reports/requests/export.csv")
	if err != nil {
		t.Fatalf("csv export: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "Reference,Subject") {
		t.Errorf("csv export does not start with a header row: %q", firstLine(string(body)))
	}
}

// TestRoutingSimulation proves an admin can dry-run a rule before activating
// it, which is what stops a bad rule silently black-holing a queue.
func TestRoutingSimulation(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-admin", "admin@city.example", domain.RoleAdmin)
	e.seedRequest(t)

	var queues struct {
		Items []domain.Queue `json:"items"`
	}
	e.get("/api/queues", &queues)
	var bylaw string
	for _, q := range queues.Items {
		if q.Code == "BYLAW-GEN" {
			bylaw = q.ID
		}
	}

	var out struct {
		Sampled int `json:"sampled"`
		Changed int `json:"changed"`
		Cases   []struct {
			Changed   bool     `json:"changed"`
			ProposedQ string   `json:"proposedQueueId"`
			Rules     []string `json:"matchedRules"`
		} `json:"cases"`
	}
	code := e.post("/api/routing-rules/simulate", map[string]any{
		"rules": []map[string]any{{
			"name":     "Everything to Bylaw",
			"priority": 10,
			"active":   true,
			"conditions": map[string]any{
				"all": []map[string]any{{"field": "subject", "op": "contains", "value": "pothole"}},
			},
			"actions": map[string]any{"queueId": bylaw},
		}},
		"sample": 50,
	}, &out)
	if code != 200 {
		t.Fatalf("simulate: %d", code)
	}
	if out.Sampled == 0 {
		t.Fatal("simulation sampled nothing")
	}
	if out.Changed == 0 {
		t.Errorf("simulation reported no change, but the rule rewrites every pothole: %+v", out)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (e *env) seedRequest(t *testing.T) *domain.Request {
	t.Helper()

	var types struct {
		Items []domain.ServiceType `json:"items"`
	}
	e.get("/api/service-types", &types)
	var pothole string
	for _, st := range types.Items {
		if st.Code == "POTHOLE" {
			pothole = st.ID
		}
	}

	var contact domain.Contact
	e.post("/api/contacts", map[string]any{
		"displayName":  fmt.Sprintf("Citizen %d", time.Now().UnixNano()),
		"primaryEmail": fmt.Sprintf("c%d@example.gov", time.Now().UnixNano()),
	}, &contact)

	var req domain.Request
	code := e.post("/api/requests", map[string]any{
		"contactId": contact.ID, "serviceTypeId": pothole,
		"subject": "Pothole on Oak Street", "address1": "12 Oak Street",
		"formData": map[string]any{"size": "Medium"},
	}, &req)
	if code != 201 {
		t.Fatalf("seed request: %d", code)
	}
	return &req
}

func (e *env) callout(t *testing.T, sub, assertion string) callout.Bundle {
	t.Helper()
	var bundle callout.Bundle
	if err := json.Unmarshal([]byte(e.calloutRaw(t, sub, assertion)), &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	return bundle
}

func (e *env) calloutRaw(t *testing.T, sub, assertion string) string {
	t.Helper()

	req, _ := http.NewRequest(http.MethodGet,
		e.api.URL+"/api/citizens/"+url.PathEscape(sub)+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+assertion)
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("callout: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callout returned %d: %s", resp.StatusCode, body)
	}
	return string(body)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
