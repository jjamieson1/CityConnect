package agents

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/devtools/c2stub"
	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/c2/oidc"
	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/storetest"
)

type fixture struct {
	svc  *Service
	db   *gorm.DB
	stub *c2stub.Server
	cfg  *config.Config
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	stub, err := c2stub.New(c2stub.Options{ClientID: "cityconnect-test", ClientSecret: "shh"})
	if err != nil {
		t.Fatalf("stub: %v", err)
	}
	srv := httptest.NewServer(stub.Handler())
	t.Cleanup(srv.Close)
	stub.SetIssuer(srv.URL + "/oidc")

	cfg := &config.Config{
		Env: "test",
		Sec: config.SecurityConfig{SessionTTL: 8 * time.Hour, SessionIdleTTL: 2 * time.Hour},
		C2: config.C2Config{
			PortalOrigin: srv.URL, Issuer: srv.URL + "/oidc",
			ClientID: "cityconnect-test", ClientSecret: "shh",
			RedirectURL:           "http://localhost:4021/cityconnect/api/auth/callback",
			PostLogoutRedirectURL: "http://localhost:4021/cityconnect/",
			Scopes:                []string{"openid", "profile", "email"},
			DiscoveryCacheTTL:     time.Minute, HTTPTimeout: 5 * time.Second,
			ClockSkew: time.Minute,
		},
	}

	db := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	provider := oidc.New(cfg.C2)

	return &fixture{
		svc:  NewService(db, cfg, provider, audit.NewService(db, log), log),
		db:   db,
		stub: stub,
		cfg:  cfg,
	}
}

// login drives a full redirect round-trip and returns the result.
func (f *fixture) login(t *testing.T, sub, email, name string) (*LoginResult, error) {
	t.Helper()
	ctx := context.Background()

	authURL, err := f.svc.StartLogin(ctx, "/requests", false)
	if err != nil {
		t.Fatalf("start login: %v", err)
	}

	// Sign the subject in at the stub, then let SSO complete silently.
	f.stub.SignIn(sub)
	loc, err := noRedirectGet(authURL)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	code, state := u.Query().Get("code"), u.Query().Get("state")
	if code == "" {
		t.Fatalf("no code in %q", loc)
	}
	return f.svc.CompleteLogin(ctx, code, state, "test-agent", "127.0.0.1")
}

// TestLoginDeniesUnknownSubject is the central access-control guarantee. C2 is
// a *citizen* identity provider: everyone in the municipality can authenticate
// to it. If an unknown subject were auto-provisioned a staff account, the
// agent console would be open to the entire public.
func TestLoginDeniesUnknownSubject(t *testing.T) {
	f := newFixture(t)

	_, err := f.login(t, "random-citizen", "citizen@example.gov", "A Citizen")
	if !errors.Is(err, ErrNoAccess) {
		t.Fatalf("err = %v, want ErrNoAccess", err)
	}

	var n int64
	f.db.Model(&domain.User{}).Count(&n)
	if n != 0 {
		t.Errorf("a user was provisioned for an unknown subject (%d rows)", n)
	}
}

// TestLoginBindsInviteToSubject covers the invite path: an admin creates the
// user by email, and the first successful login pins it to the C2 subject.
func TestLoginBindsInviteToSubject(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	invited, err := f.svc.InviteUser(ctx, audit.JobActor("test"), InviteInput{
		Email: "Dana.Agent@city.example", Name: "Dana Agent", Role: domain.RoleAgent,
	})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if invited.Status != domain.UserInvited {
		t.Fatalf("status = %q, want invited", invited.Status)
	}

	// The stub asserts <sub>@example.gov for a silently signed-in subject, so
	// drive the email match through a directly-minted token instead.
	res, err := f.loginAs(t, "staff-dana", "dana.agent@city.example", "Dana Agent")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if res.User.ID != invited.ID {
		t.Errorf("login bound to user %s, want the invited %s", res.User.ID, invited.ID)
	}
	if res.User.C2Sub != "staff-dana" {
		t.Errorf("c2Sub = %q, want staff-dana", res.User.C2Sub)
	}
	if res.User.Status != domain.UserActive {
		t.Errorf("status = %q, want active after first login", res.User.Status)
	}

	// A second login must resolve by subject, not re-match on email.
	res2, err := f.loginAs(t, "staff-dana", "changed-address@city.example", "Dana Agent")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if res2.User.ID != invited.ID {
		t.Errorf("second login resolved to %s, want %s", res2.User.ID, invited.ID)
	}
}

// TestLoginDeniesUnverifiedEmailInvite is the trust-on-first-use hardening: an
// invitation must not bind to a C2 identity whose email C2 has not verified, or
// an attacker could seize it by setting an unverified copy of the address on
// their own identity and signing in first.
func TestLoginDeniesUnverifiedEmailInvite(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.svc.InviteUser(ctx, audit.JobActor("test"), InviteInput{
		Email: "victim.admin@city.example", Name: "Victim Admin", Role: domain.RoleAdmin,
	}); err != nil {
		t.Fatalf("invite: %v", err)
	}

	// An attacker whose C2 identity carries the invited email, but unverified.
	_, err := f.loginClaims(t, "attacker-sub", map[string]any{
		"email": "victim.admin@city.example", "name": "Attacker", "email_verified": false,
	})
	if !errors.Is(err, ErrNoAccess) {
		t.Fatalf("err = %v, want ErrNoAccess", err)
	}

	// The invitation must remain unbound and still pending.
	var u domain.User
	if err := f.db.Where("LOWER(email) = ?", "victim.admin@city.example").First(&u).Error; err != nil {
		t.Fatalf("load invite: %v", err)
	}
	if u.C2Sub != "" {
		t.Errorf("invitation bound to %q despite unverified email", u.C2Sub)
	}
	if u.Status != domain.UserInvited {
		t.Errorf("status = %q, want still invited", u.Status)
	}

	// Once the same address is verified, the legitimate holder binds normally.
	res, err := f.loginClaims(t, "real-admin-sub", map[string]any{
		"email": "victim.admin@city.example", "name": "Victim Admin", "email_verified": true,
	})
	if err != nil {
		t.Fatalf("verified login: %v", err)
	}
	if res.User.C2Sub != "real-admin-sub" {
		t.Errorf("c2Sub = %q, want real-admin-sub", res.User.C2Sub)
	}
}

func TestLoginRejectsSuspendedUser(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	u, _ := f.svc.InviteUser(ctx, audit.JobActor("test"), InviteInput{
		Email: "sus@city.example", Role: domain.RoleAgent,
	})
	f.db.Model(u).Updates(map[string]any{"c2_sub": "staff-sus", "status": domain.UserSuspended})

	if _, err := f.loginAs(t, "staff-sus", "sus@city.example", ""); !errors.Is(err, ErrSuspended) {
		t.Fatalf("err = %v, want ErrSuspended", err)
	}
}

func TestLoginRejectsReplayedState(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.seedUser(t, "staff-1", "one@city.example", domain.RoleAgent)

	authURL, _ := f.svc.StartLogin(ctx, "", false)
	f.stub.SignIn("staff-1")
	loc, _ := noRedirectGet(authURL)
	u, _ := url.Parse(loc)
	code, state := u.Query().Get("code"), u.Query().Get("state")

	if _, err := f.svc.CompleteLogin(ctx, code, state, "", ""); err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	// State is single-use: replaying the same authorization response must fail.
	if _, err := f.svc.CompleteLogin(ctx, code, state, "", ""); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("replay err = %v, want ErrInvalidState", err)
	}
}

func TestAuthenticateSession(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.seedUser(t, "staff-2", "two@city.example", domain.RoleSupervisor)
	res, err := f.loginAs(t, "staff-2", "two@city.example", "Two")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	user, _, err := f.svc.Authenticate(ctx, res.SessionToken)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if user.Email != "two@city.example" {
		t.Errorf("email = %q", user.Email)
	}

	if _, _, err := f.svc.Authenticate(ctx, "not-a-real-token"); !errors.Is(err, ErrNoSession) {
		t.Errorf("bogus token err = %v, want ErrNoSession", err)
	}
}

// TestBackchannelLogoutEndsAllSessions is the rule that makes C2's logout
// meaningful. The logout token identifies a *user* and carries no `sid`, so
// there is no way to end one session — every session that subject holds must
// go, or a signed-out user stays signed in on their other device.
func TestBackchannelLogoutEndsAllSessions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.seedUser(t, "staff-3", "three@city.example", domain.RoleAgent)

	var tokens []string
	for range 3 {
		res, err := f.loginAs(t, "staff-3", "three@city.example", "Three")
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		tokens = append(tokens, res.SessionToken)
	}

	// A session belonging to a different subject must survive.
	f.seedUser(t, "staff-4", "four@city.example", domain.RoleAgent)
	other, err := f.loginAs(t, "staff-4", "four@city.example", "Four")
	if err != nil {
		t.Fatalf("other login: %v", err)
	}

	logoutToken, err := f.stub.LogoutToken("staff-3")
	if err != nil {
		t.Fatalf("mint logout token: %v", err)
	}
	if err := f.svc.BackchannelLogout(ctx, logoutToken); err != nil {
		t.Fatalf("backchannel logout: %v", err)
	}

	for i, tok := range tokens {
		if _, _, err := f.svc.Authenticate(ctx, tok); !errors.Is(err, ErrNoSession) {
			t.Errorf("session %d still valid after back-channel logout (err=%v)", i, err)
		}
	}
	if _, _, err := f.svc.Authenticate(ctx, other.SessionToken); err != nil {
		t.Errorf("an unrelated subject's session was revoked: %v", err)
	}
}

func TestBackchannelLogoutRejectsBadToken(t *testing.T) {
	f := newFixture(t)

	// An id_token is not a logout token: it carries a nonce and no logout
	// event. Accepting one would let a replayed login token sign a user out.
	idToken, err := f.stub.IDToken("staff-3", "a-nonce", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.BackchannelLogout(context.Background(), idToken); err == nil {
		t.Fatal("an id_token must not be accepted as a logout token")
	}
}

// TestBootstrapCreatesFirstAdmin covers the day-one blocker: with C2 SSO as
// the only login there is nobody who can grant the first role, so a fresh
// deployment is locked out unless the bootstrap list creates one.
func TestBootstrapCreatesFirstAdmin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.cfg.BootstrapAdminSubs = []string{"staff-boss"}
	if err := f.svc.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	var u domain.User
	if err := f.db.Where("c2_sub = ?", "staff-boss").First(&u).Error; err != nil {
		t.Fatalf("bootstrap admin not created: %v", err)
	}
	if u.Role != domain.RoleAdmin {
		t.Errorf("role = %q, want admin", u.Role)
	}

	// Re-running must not duplicate or downgrade.
	if err := f.svc.Bootstrap(ctx); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	var n int64
	f.db.Model(&domain.User{}).Where("c2_sub = ?", "staff-boss").Count(&n)
	if n != 1 {
		t.Errorf("bootstrap created %d rows, want 1", n)
	}
}

func TestBootstrapPromotesExistingUser(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	u := f.seedUser(t, "staff-promote", "p@city.example", domain.RoleAgent)
	f.cfg.BootstrapAdminSubs = []string{"staff-promote"}

	if err := f.svc.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	var got domain.User
	f.db.First(&got, "id = ?", u.ID)
	if got.Role != domain.RoleAdmin {
		t.Errorf("role = %q, want admin after bootstrap promotion", got.Role)
	}
}

// TestGuardLastAdmin prevents locking the deployment out through the UI.
// Recovery would mean shell access on the server to run ccadm.
func TestGuardLastAdmin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	actor := audit.JobActor("test")

	admin := f.seedUser(t, "staff-admin", "admin@city.example", domain.RoleAdmin)

	agentRole := domain.RoleAgent
	_, err := f.svc.UpdateUser(ctx, actor, admin.ID, UpdateUserInput{Role: &agentRole})
	if !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("err = %v, want ErrLastAdmin", err)
	}

	suspended := domain.UserSuspended
	_, err = f.svc.UpdateUser(ctx, actor, admin.ID, UpdateUserInput{Status: &suspended})
	if !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("suspend err = %v, want ErrLastAdmin", err)
	}

	// With a second admin present, the demotion is allowed.
	f.seedUser(t, "staff-admin2", "admin2@city.example", domain.RoleAdmin)
	if _, err := f.svc.UpdateUser(ctx, actor, admin.ID, UpdateUserInput{Role: &agentRole}); err != nil {
		t.Fatalf("demotion with a spare admin should succeed: %v", err)
	}
}

func TestSuspendRevokesSessions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.seedUser(t, "staff-admin", "admin@city.example", domain.RoleAdmin)
	u := f.seedUser(t, "staff-5", "five@city.example", domain.RoleAgent)

	res, err := f.loginAs(t, "staff-5", "five@city.example", "Five")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	suspended := domain.UserSuspended
	if _, err := f.svc.UpdateUser(ctx, audit.JobActor("test"), u.ID, UpdateUserInput{Status: &suspended}); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	// Suspension must bite immediately, not when the session expires.
	if _, _, err := f.svc.Authenticate(ctx, res.SessionToken); err == nil {
		t.Fatal("a suspended user's session is still usable")
	}
}
