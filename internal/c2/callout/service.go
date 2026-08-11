// Package callout implements the inbound Service Card callout: C2 asks us,
// server to server, what one citizen's live status is, and renders whatever we
// return on their Service Card.
//
// Three constraints shape the code. C2 does not cache, calling on every render
// and on a timer while the card is on screen, so this must be fast. It fails
// safe, showing static fallback content on any error, so a clean 4xx beats a
// malformed 200. And it is consent-gated on C2's side, so being called at all
// already means the citizen consented.
package callout

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/c2/oidc"
	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/contacts"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/requests"
)

// Errors returned to the HTTP layer.
var (
	ErrUnauthorized = errors.New("callout: unauthorized")
	ErrUnavailable  = errors.New("callout: unavailable")
)

// Bundle is the response shape C2 renders. Every field is optional; C2 falls
// back to the admin-configured card content for anything omitted.
//
// The CTA field is capitalised exactly as shown — that is C2's contract, not a
// typo, and lower-casing it silently loses the primary link.
type Bundle struct {
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	CTA         string   `json:"CTA,omitempty"`
	Contact     *Contact `json:"contact,omitempty"`
	Tasks       []Task   `json:"tasks,omitempty"`
}

// Contact renders the "Contact us" block.
type Contact struct {
	Address1   string `json:"address1,omitempty"`
	Address2   string `json:"address2,omitempty"`
	City       string `json:"city,omitempty"`
	State      string `json:"state,omitempty"`
	PostalCode string `json:"postalCode,omitempty"`
	Email      string `json:"email,omitempty"`
	Phone      string `json:"phone,omitempty"`
}

// Task is one actionable row on the card.
type Task struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
}

// AuthMode records how a callout authenticated.
const (
	AuthSignedJWT = "signed_jwt"
	AuthAppKey    = "app_key"
)

// Service builds status bundles.
type Service struct {
	db       *gorm.DB
	cfg      *config.Config
	oidc     *oidc.Provider
	contacts *contacts.Service
	requests *requests.Service
	log      *slog.Logger

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	bundle  *Bundle
	expires time.Time
}

// NewService builds the callout service.
func NewService(
	db *gorm.DB, cfg *config.Config, provider *oidc.Provider,
	cont *contacts.Service, reqs *requests.Service, log *slog.Logger,
) *Service {
	return &Service{
		db: db, cfg: cfg, oidc: provider, contacts: cont, requests: reqs,
		log: log.With("component", "callout"), cache: map[string]cacheEntry{},
	}
}

// Authenticate verifies an inbound callout and returns the subject it is
// about, plus the mode used.
//
// Signed JWT is the primary path: the assertion is verified against the same
// JWKS as id_tokens, so there is no extra secret to manage. The legacy app_key
// mode stays behind a config flag and is only consulted when no bearer token
// was presented. Everything fails closed.
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (string, string, error) {
	authz := r.Header.Get("Authorization")

	if token, ok := strings.CutPrefix(authz, "Bearer "); ok {
		claims, err := s.oidc.VerifyCalloutAssertion(ctx, strings.TrimSpace(token))
		if err != nil {
			s.log.WarnContext(ctx, "callout assertion rejected", "error", err)
			return "", AuthSignedJWT, ErrUnauthorized
		}
		return claims.Subject, AuthSignedJWT, nil
	}

	if !s.cfg.C2.CalloutAllowAppKey {
		return "", "", ErrUnauthorized
	}

	key, secret := r.Header.Get("X-App-Key"), r.Header.Get("X-App-Secret")
	if key == "" || secret == "" {
		return "", "", ErrUnauthorized
	}
	// Constant-time comparison: these are long-lived shared secrets, and a
	// timing oracle on them is worth more to an attacker than on a session id.
	keyOK := subtle.ConstantTimeCompare([]byte(key), []byte(s.cfg.C2.CalloutAppKey)) == 1
	secretOK := subtle.ConstantTimeCompare([]byte(secret), []byte(s.cfg.C2.CalloutAppSecret)) == 1
	if !keyOK || !secretOK {
		return "", AuthAppKey, ErrUnauthorized
	}
	// The app_key mode carries no subject, so it must come from the URL.
	return "", AuthAppKey, nil
}

// Build assembles the status bundle for one citizen.
//
// An unrecognised subject returns an empty but valid bundle rather than a
// guess or an error: C2 renders the static fallback, which is the right
// outcome for a citizen the CRM has simply never met.
func (s *Service) Build(ctx context.Context, sub string) (*Bundle, error) {
	if sub == "" {
		return &Bundle{}, nil
	}

	if cached := s.cached(sub); cached != nil {
		return cached, nil
	}

	contact, err := s.contacts.FindByC2Sub(ctx, sub)
	if errors.Is(err, contacts.ErrNotFound) {
		empty := &Bundle{}
		s.store(sub, empty)
		return empty, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	open, err := s.requests.OpenForContact(ctx, contact.ID, s.cfg.C2.CalloutMaxTasks*2)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	bundle := s.render(ctx, contact, open)
	s.store(sub, bundle)
	return bundle, nil
}

func (s *Service) render(ctx context.Context, contact *domain.Contact, open []domain.Request) *Bundle {
	b := &Bundle{
		Title: "Your service requests",
		// The citizen portal's own origin. Not the staff console — a citizen
		// following that link has no staff account and would be bounced to
		// sign-in and then refused — and not a /portal path, since the portal
		// is now its own application at the root of its host.
		CTA: s.cfg.PortalPublicURL,
	}

	if len(open) == 0 {
		b.Description = "You have no open service requests with the City."
		b.Contact = s.defaultContact(ctx)
		return b
	}

	max := s.cfg.C2.CalloutMaxTasks
	if max <= 0 {
		max = 10
	}

	shown := open
	if len(shown) > max {
		shown = shown[:max]
	}

	for _, r := range shown {
		b.Tasks = append(b.Tasks, Task{
			Name:        fmt.Sprintf("%s — %s", r.Reference, r.Subject),
			Description: s.describe(ctx, &r),
			// Absolute URLs only; C2 opens these in a new tab.
			//
			// The citizen portal's own origin, never the staff console's. A
			// citizen following this link has no console account, so a link
			// there ends at a sign-in page that refuses them — and the portal
			// is a separate host at its own root, so the console's base path
			// does not apply either.
			//
			// Keyed on the reference, which is what the portal routes on and what
			// the citizen sees quoted everywhere else.
			URL: fmt.Sprintf("%s/requests/%s", s.cfg.PortalPublicURL, r.Reference),
		})
	}

	newest := open[0]
	plural := "requests"
	if len(open) == 1 {
		plural = "request"
	}
	b.Description = fmt.Sprintf("You have %d open %s. Most recent update: %s is %s (%s).",
		len(open), plural, newest.Reference,
		strings.ToLower(catalogLabel(newest.Status)),
		newest.LastActivityA.Format("2 January"))
	if len(open) > len(shown) {
		b.Description += fmt.Sprintf(" Showing the %d most recently updated.", len(shown))
	}

	// Prefer the department that owns the newest request, so the contact block
	// points at the people actually working it.
	b.Contact = s.contactFor(ctx, newest.DepartmentID)
	return b
}

// describe renders the per-request line: status, when it last moved, and the
// most recent citizen-visible note. That last part is what turns a status code
// into "a crew is scheduled for Tuesday".
func (s *Service) describe(ctx context.Context, r *domain.Request) string {
	parts := []string{catalogLabel(r.Status)}
	parts = append(parts, "updated "+r.LastActivityA.Format("2 January"))

	var comment domain.RequestComment
	err := s.db.WithContext(ctx).
		Where("request_id = ? AND visibility = ?", r.ID, domain.VisibilityCitizen).
		Order("created_at DESC").First(&comment).Error
	if err == nil && strings.TrimSpace(comment.Body) != "" {
		parts = append(parts, truncate(oneLine(comment.Body), 160))
	} else if r.DueAt != nil && r.Status.Open() {
		parts = append(parts, "expected by "+r.DueAt.Format("2 January"))
	}

	return strings.Join(parts, " · ")
}

func (s *Service) contactFor(ctx context.Context, departmentID string) *Contact {
	if departmentID == "" {
		return s.defaultContact(ctx)
	}
	var d domain.Department
	if err := s.db.WithContext(ctx).First(&d, "id = ?", departmentID).Error; err != nil {
		return s.defaultContact(ctx)
	}
	c := &Contact{
		Address1: d.Address1, Address2: d.Address2, City: d.City,
		State: d.State, PostalCode: d.PostalCode,
		Email: d.ContactEmail, Phone: d.ContactPhone,
	}
	if c.Email == "" && c.Phone == "" {
		return s.defaultContact(ctx)
	}
	return c
}

func (s *Service) defaultContact(ctx context.Context) *Contact {
	var d domain.Department
	err := s.db.WithContext(ctx).
		Where("active = ? AND (contact_email <> '' OR contact_phone <> '')", true).
		Order("sort_order ASC").First(&d).Error
	if err != nil {
		return nil
	}
	return &Contact{
		Address1: d.Address1, City: d.City, State: d.State,
		PostalCode: d.PostalCode, Email: d.ContactEmail, Phone: d.ContactPhone,
	}
}

// ---------------------------------------------------------------------------
// Caching and logging
// ---------------------------------------------------------------------------

func (s *Service) cached(sub string) *Bundle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.cache[sub]
	if !ok || time.Now().After(entry.expires) {
		return nil
	}
	return entry.bundle
}

// store caches a bundle briefly. C2 calls on every render and on a refresh
// timer, so a card left open on screen would otherwise hammer the database for
// an answer that changes a few times a week.
func (s *Service) store(sub string, b *Bundle) {
	ttl := s.cfg.C2.CalloutCacheTTL
	if ttl <= 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Opportunistic sweep so the map cannot grow without bound.
	if len(s.cache) > 5000 {
		now := time.Now()
		for k, v := range s.cache {
			if now.After(v.expires) {
				delete(s.cache, k)
			}
		}
	}
	s.cache[sub] = cacheEntry{bundle: b, expires: time.Now().Add(ttl)}
}

// Invalidate drops a citizen's cached bundle, called when their request
// activity changes so the card reflects it on the next render.
func (s *Service) Invalidate(sub string) {
	s.mu.Lock()
	delete(s.cache, sub)
	s.mu.Unlock()
}

// Log records a callout for the admin console.
//
// Writes are sampled: recording every call would make this the busiest table
// in the database and flood the timeline of anyone who leaves a Service Card
// open.
func (s *Service) Log(ctx context.Context, entry domain.CalloutLog) {
	if entry.Outcome == "ok" && entry.DurationMS < 500 {
		var recent int64
		s.db.WithContext(ctx).Model(&domain.CalloutLog{}).
			Where("c2_sub = ? AND created_at > ?", entry.C2Sub, time.Now().UTC().Add(-10*time.Minute)).
			Count(&recent)
		if recent > 0 {
			return
		}
	}
	if err := s.db.WithContext(ctx).Create(&entry).Error; err != nil {
		s.log.WarnContext(ctx, "could not record callout", "error", err)
	}
}

func catalogLabel(s domain.RequestStatus) string {
	switch s {
	case domain.StatusNew:
		return "Received"
	case domain.StatusTriaged:
		return "Under review"
	case domain.StatusAssigned:
		return "Assigned to a crew"
	case domain.StatusInProgress:
		return "In progress"
	case domain.StatusWaitingCitizen:
		return "Waiting for your reply"
	case domain.StatusWaitingThirdParty:
		return "Waiting on a third party"
	case domain.StatusResolved:
		return "Resolved"
	case domain.StatusClosed:
		return "Closed"
	case domain.StatusCancelled:
		return "Cancelled"
	}
	return strings.ReplaceAll(string(s), "_", " ")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
