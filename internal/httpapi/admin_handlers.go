package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jjamieson1/CityConnect/internal/agents"
	"github.com/jjamieson1/CityConnect/internal/catalog"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/notifications"
	"github.com/jjamieson1/CityConnect/internal/reports"
	"github.com/jjamieson1/CityConnect/internal/routing"
	"github.com/jjamieson1/CityConnect/internal/store"
	"github.com/jjamieson1/CityConnect/internal/webhooks"
)

// ---------------------------------------------------------------------------
// Catalogue
// ---------------------------------------------------------------------------

func (s *Server) mountCatalog(r chi.Router) {
	read := require(agents.PermConfigRead)
	write := require(agents.PermConfigWrite)

	r.Route("/service-types", func(c chi.Router) {
		c.With(read).Get("/", s.handleListServiceTypes)
		c.With(write).Post("/", s.handleSaveServiceType)
		c.With(read).Get("/{id}", s.handleGetServiceType)
		c.With(write).Patch("/{id}", s.handleSaveServiceType)
		c.With(write).Delete("/{id}", s.handleDeleteServiceType)
	})

	r.Route("/sla-policies", func(c chi.Router) {
		c.With(read).Get("/", s.handleListSLAPolicies)
		c.With(write).Post("/", s.handleSaveSLAPolicy)
		c.With(write).Patch("/{id}", s.handleSaveSLAPolicy)
	})

	r.Route("/business-calendars", func(c chi.Router) {
		c.With(read).Get("/", s.handleListCalendars)
		c.With(write).Post("/", s.handleSaveCalendar)
		c.With(write).Patch("/{id}", s.handleSaveCalendar)
	})

	r.Route("/macros", func(c chi.Router) {
		c.With(require(agents.PermRequestRead)).Get("/", s.handleListMacros)
		c.With(write).Post("/", s.handleSaveMacro)
		c.With(write).Patch("/{id}", s.handleSaveMacro)
		c.With(write).Delete("/{id}", s.handleDeleteMacro)
	})

	r.Route("/notification-templates", func(c chi.Router) {
		c.With(read).Get("/", s.handleListTemplates)
		c.With(write).Post("/", s.handleSaveTemplate)
		c.With(write).Patch("/{id}", s.handleSaveTemplate)
		c.With(write).Delete("/{id}", s.handleDeleteTemplate)
	})
}

func (s *Server) handleListServiceTypes(w http.ResponseWriter, r *http.Request) {
	items, err := s.Catalog.ListServiceTypes(r.Context(), catalogFilterFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetServiceType(w http.ResponseWriter, r *http.Request) {
	st, err := s.Catalog.GetServiceType(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleSaveServiceType(w http.ResponseWriter, r *http.Request) {
	var st domain.ServiceType
	if !decode(w, r, &st) {
		return
	}
	if id := chi.URLParam(r, "id"); id != "" {
		st.ID = id
	}
	saved, err := s.Catalog.SaveServiceType(r.Context(), principalFrom(r.Context()).Actor(), &st)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteServiceType(w http.ResponseWriter, r *http.Request) {
	if err := s.Catalog.DeleteServiceType(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id")); err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) handleListSLAPolicies(w http.ResponseWriter, r *http.Request) {
	items, err := s.Catalog.ListSLAPolicies(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleSaveSLAPolicy(w http.ResponseWriter, r *http.Request) {
	var p domain.SLAPolicy
	if !decode(w, r, &p) {
		return
	}
	if id := chi.URLParam(r, "id"); id != "" {
		p.ID = id
	}
	saved, err := s.Catalog.SaveSLAPolicy(r.Context(), principalFrom(r.Context()).Actor(), &p)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleListCalendars(w http.ResponseWriter, r *http.Request) {
	items, err := s.Catalog.ListCalendars(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleSaveCalendar(w http.ResponseWriter, r *http.Request) {
	var c domain.BusinessCalendar
	if !decode(w, r, &c) {
		return
	}
	if id := chi.URLParam(r, "id"); id != "" {
		c.ID = id
	}
	saved, err := s.Catalog.SaveCalendar(r.Context(), principalFrom(r.Context()).Actor(), &c)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleListMacros(w http.ResponseWriter, r *http.Request) {
	dept := r.URL.Query().Get("departmentId")
	if dept == "" {
		if p := principalFrom(r.Context()); p != nil {
			dept = p.DepartmentID()
		}
	}
	items, err := s.Catalog.ListMacros(r.Context(), dept)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleSaveMacro(w http.ResponseWriter, r *http.Request) {
	var m domain.Macro
	if !decode(w, r, &m) {
		return
	}
	if id := chi.URLParam(r, "id"); id != "" {
		m.ID = id
	}
	saved, err := s.Catalog.SaveMacro(r.Context(), principalFrom(r.Context()).Actor(), &m)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteMacro(w http.ResponseWriter, r *http.Request) {
	if err := s.Catalog.DeleteMacro(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id")); err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	items, err := s.Catalog.ListTemplates(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleSaveTemplate(w http.ResponseWriter, r *http.Request) {
	var t domain.NotificationTemplate
	if !decode(w, r, &t) {
		return
	}
	if id := chi.URLParam(r, "id"); id != "" {
		t.ID = id
	}
	saved, err := s.Catalog.SaveTemplate(r.Context(), principalFrom(r.Context()).Actor(), &t)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.Catalog.DeleteTemplate(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id")); err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

func (s *Server) mountRouting(r chi.Router) {
	read := require(agents.PermConfigRead)
	write := require(agents.PermConfigWrite)

	r.Route("/queues", func(q chi.Router) {
		q.With(require(agents.PermRequestRead)).Get("/", s.handleListQueues)
		q.With(write).Post("/", s.handleSaveQueue)
		q.With(require(agents.PermRequestRead)).Get("/{id}", s.handleGetQueue)
		q.With(write).Patch("/{id}", s.handleSaveQueue)
		q.With(write).Delete("/{id}", s.handleDeleteQueue)
		q.With(write).Put("/{id}/members", s.handleSetQueueMembers)
	})

	r.Route("/routing-rules", func(rr chi.Router) {
		rr.With(read).Get("/", s.handleListRules)
		rr.With(write).Post("/", s.handleSaveRule)
		rr.With(read).Get("/{id}", s.handleGetRule)
		rr.With(write).Patch("/{id}", s.handleSaveRule)
		rr.With(write).Delete("/{id}", s.handleDeleteRule)
		rr.With(read).Post("/simulate", s.handleSimulate)
	})
}

func (s *Server) handleListQueues(w http.ResponseWriter, r *http.Request) {
	items, err := s.Routing.ListQueues(r.Context(),
		r.URL.Query().Get("departmentId"), queryBool(r, "includeInactive"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetQueue(w http.ResponseWriter, r *http.Request) {
	q, err := s.Routing.GetQueue(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, q)
}

func (s *Server) handleSaveQueue(w http.ResponseWriter, r *http.Request) {
	var q domain.Queue
	if !decode(w, r, &q) {
		return
	}
	if id := chi.URLParam(r, "id"); id != "" {
		q.ID = id
	}
	saved, err := s.Routing.SaveQueue(r.Context(), principalFrom(r.Context()).Actor(), &q)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteQueue(w http.ResponseWriter, r *http.Request) {
	if err := s.Routing.DeleteQueue(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id")); err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}

type memberBody struct {
	UserIDs []string `json:"userIds"`
}

func (s *Server) handleSetQueueMembers(w http.ResponseWriter, r *http.Request) {
	var body memberBody
	if !decode(w, r, &body) {
		return
	}
	if err := s.Routing.SetQueueMembers(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), body.UserIDs); err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	items, err := s.Routing.ListRules(r.Context(), queryBool(r, "includeInactive"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetRule(w http.ResponseWriter, r *http.Request) {
	rule, err := s.Routing.GetRule(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) handleSaveRule(w http.ResponseWriter, r *http.Request) {
	var rule domain.RoutingRule
	if !decode(w, r, &rule) {
		return
	}
	if id := chi.URLParam(r, "id"); id != "" {
		rule.ID = id
	}
	saved, err := s.Routing.SaveRule(r.Context(), principalFrom(r.Context()).Actor(), &rule)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	if err := s.Routing.DeleteRule(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id")); err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}

type simulateBody struct {
	Rules  []domain.RoutingRule `json:"rules,omitempty"`
	Sample int                  `json:"sample,omitempty"`
	// UseStored replays the saved rule set instead of a candidate one, which
	// is how an admin checks what the live rules are actually doing.
	UseStored bool `json:"useStored,omitempty"`
}

// handleSimulate dry-runs a rule set against recent history.
func (s *Server) handleSimulate(w http.ResponseWriter, r *http.Request) {
	var body simulateBody
	if !decode(w, r, &body) {
		return
	}

	rules := body.Rules
	if body.UseStored || len(rules) == 0 {
		stored, err := s.Routing.ListRules(r.Context(), false)
		if err != nil {
			fail(w, r, err)
			return
		}
		rules = stored
	} else {
		// Validate candidates before replaying them, so the report is not
		// silently produced from rules that would be rejected on save.
		for i := range rules {
			if err := routing.ValidateRule(&rules[i]); err != nil {
				writeProblem(w, r, http.StatusBadRequest, "invalid_rule", err.Error())
				return
			}
		}
	}

	res, err := s.Routing.Simulate(r.Context(), rules, body.Sample)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------------------------------------------------------------------------
// Users, departments, systems, tokens, audit
// ---------------------------------------------------------------------------

func (s *Server) mountAdmin(r chi.Router) {
	manage := require(agents.PermUserManage)
	write := require(agents.PermConfigWrite)

	r.Route("/departments", func(d chi.Router) {
		d.Get("/", s.handleListDepartments) // every authenticated caller needs these
		d.With(write).Post("/", s.handleSaveDepartment)
		d.With(write).Patch("/{id}", s.handleSaveDepartment)
		d.With(write).Delete("/{id}", s.handleDeleteDepartment)
	})

	r.Route("/users", func(u chi.Router) {
		u.With(require(agents.PermRequestRead)).Get("/", s.handleListUsers)
		u.With(manage).Post("/invite", s.handleInviteUser)
		u.With(require(agents.PermRequestRead)).Get("/{id}", s.handleGetUser)
		u.With(manage).Patch("/{id}", s.handleUpdateUser)
		u.With(manage).Post("/{id}/revoke-sessions", s.handleRevokeSessions)
	})

	r.Route("/connected-systems", func(c chi.Router) {
		c.With(require(agents.PermSystemManage)).Get("/", s.handleListSystems)
		c.With(require(agents.PermSystemManage)).Post("/", s.handleSaveSystem)
		c.With(require(agents.PermSystemManage)).Get("/{id}", s.handleGetSystem)
		c.With(require(agents.PermSystemManage)).Patch("/{id}", s.handleSaveSystem)
		c.With(require(agents.PermSystemManage)).Post("/{id}/rotate-secret", s.handleRotateSecret)
	})

	r.Route("/tokens", func(t chi.Router) {
		t.With(require(agents.PermSystemManage)).Get("/", s.handleListTokens)
		t.With(require(agents.PermSystemManage)).Post("/", s.handleIssueToken)
		t.With(require(agents.PermSystemManage)).Delete("/{id}", s.handleRevokeToken)
	})

	r.Route("/audit", func(a chi.Router) {
		a.With(require(agents.PermAuditRead)).Get("/", s.handleListAudit)
		a.With(require(agents.PermAuditRead)).Get("/verify", s.handleVerifyAudit)
	})

	r.Route("/jobs", func(j chi.Router) {
		j.With(require(agents.PermConfigRead)).Get("/", s.handleJobStatus)
		j.With(require(agents.PermConfigWrite)).Post("/{name}/run", s.handleRunJob)
	})

	r.Route("/webhooks", func(h chi.Router) {
		h.With(require(agents.PermSystemManage)).Get("/", s.handleListWebhooks)
		h.With(require(agents.PermSystemManage)).Post("/{id}/replay", s.handleReplayWebhook)
		h.With(require(agents.PermSystemManage)).Post("/replay-dead", s.handleReplayDeadWebhooks)
	})
}

func (s *Server) handleListDepartments(w http.ResponseWriter, r *http.Request) {
	items, err := s.Agents.ListDepartments(r.Context(), queryBool(r, "includeInactive"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleSaveDepartment(w http.ResponseWriter, r *http.Request) {
	var d domain.Department
	if !decode(w, r, &d) {
		return
	}
	if id := chi.URLParam(r, "id"); id != "" {
		d.ID = id
	}
	saved, err := s.Agents.SaveDepartment(r.Context(), principalFrom(r.Context()).Actor(), &d)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteDepartment(w http.ResponseWriter, r *http.Request) {
	if err := s.Agents.DeleteDepartment(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id")); err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	res, err := s.Agents.ListUsers(r.Context(), agents.UserFilter{
		Query:        r.URL.Query().Get("q"),
		DepartmentID: r.URL.Query().Get("departmentId"),
		Role:         r.URL.Query().Get("role"),
		Status:       r.URL.Query().Get("status"),
		QueueID:      r.URL.Query().Get("queueId"),
	}, pageFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.Agents.GetUser(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

type inviteBody struct {
	Email           string   `json:"email"`
	Name            string   `json:"name,omitempty"`
	Title           string   `json:"title,omitempty"`
	Phone           string   `json:"phone,omitempty"`
	Role            string   `json:"role,omitempty"`
	DepartmentID    string   `json:"departmentId,omitempty"`
	CrossDepartment bool     `json:"crossDepartment,omitempty"`
	QueueIDs        []string `json:"queueIds,omitempty"`
}

func (s *Server) handleInviteUser(w http.ResponseWriter, r *http.Request) {
	var body inviteBody
	if !decode(w, r, &body) {
		return
	}
	u, err := s.Agents.InviteUser(r.Context(), principalFrom(r.Context()).Actor(), agents.InviteInput{
		Email: body.Email, Name: body.Name, Title: body.Title, Phone: body.Phone,
		Role: domain.Role(body.Role), DepartmentID: body.DepartmentID,
		CrossDepartment: body.CrossDepartment, QueueIDs: body.QueueIDs,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	// There is no invitation email to send: the user signs in through C2 and
	// the record binds to their subject on first login.
	writeJSON(w, http.StatusCreated, map[string]any{
		"user": u,
		"note": "This person can now sign in with C2 SSO using this email address.",
	})
}

type updateUserBody struct {
	Name            *string   `json:"name,omitempty"`
	Title           *string   `json:"title,omitempty"`
	Phone           *string   `json:"phone,omitempty"`
	Role            *string   `json:"role,omitempty"`
	DepartmentID    *string   `json:"departmentId,omitempty"`
	CrossDepartment *bool     `json:"crossDepartment,omitempty"`
	Status          *string   `json:"status,omitempty"`
	QueueIDs        *[]string `json:"queueIds,omitempty"`
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var body updateUserBody
	if !decode(w, r, &body) {
		return
	}
	in := agents.UpdateUserInput{
		Name: body.Name, Title: body.Title, Phone: body.Phone,
		DepartmentID: body.DepartmentID, CrossDepartment: body.CrossDepartment,
		QueueIDs: body.QueueIDs,
	}
	if body.Role != nil {
		role := domain.Role(*body.Role)
		in.Role = &role
	}
	if body.Status != nil {
		status := domain.UserStatus(*body.Status)
		in.Status = &status
	}

	u, err := s.Agents.UpdateUser(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id"), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	n, err := s.Agents.RevokeUserSessions(r.Context(), chi.URLParam(r, "id"), "admin_revoked")
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"revoked": n})
}

func (s *Server) handleListSystems(w http.ResponseWriter, r *http.Request) {
	items, err := s.Agents.ListSystems(r.Context(), queryBool(r, "includeInactive"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetSystem(w http.ResponseWriter, r *http.Request) {
	sys, err := s.Agents.GetSystem(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sys)
}

type systemBody struct {
	domain.ConnectedSystem
	QueueIDs []string `json:"queueIds,omitempty"`
}

func (s *Server) handleSaveSystem(w http.ResponseWriter, r *http.Request) {
	var body systemBody
	if !decode(w, r, &body) {
		return
	}
	sys := body.ConnectedSystem
	if id := chi.URLParam(r, "id"); id != "" {
		sys.ID = id
	}
	saved, err := s.Agents.SaveSystem(r.Context(), principalFrom(r.Context()).Actor(), &sys, body.QueueIDs)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleRotateSecret(w http.ResponseWriter, r *http.Request) {
	secret, err := s.Agents.RotateWebhookSecret(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	// Shown exactly once — it is not retrievable afterwards.
	writeJSON(w, http.StatusOK, map[string]string{
		"webhookSecret": secret,
		"note":          "Copy this now; it is not shown again.",
	})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	items, err := s.Agents.ListTokens(r.Context(),
		r.URL.Query().Get("ownerId"), r.URL.Query().Get("systemId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type tokenBody struct {
	Name      string   `json:"name"`
	OwnerID   string   `json:"ownerId,omitempty"`
	SystemID  string   `json:"systemId,omitempty"`
	Scopes    []string `json:"scopes"`
	ReadOnly  bool     `json:"readOnly,omitempty"`
	ExpiresIn string   `json:"expiresIn,omitempty"`
}

func (s *Server) handleIssueToken(w http.ResponseWriter, r *http.Request) {
	var body tokenBody
	if !decode(w, r, &body) {
		return
	}
	var ttl time.Duration
	if body.ExpiresIn != "" {
		parsed, err := time.ParseDuration(body.ExpiresIn)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_input",
				"expiresIn must be a duration such as 720h.")
			return
		}
		ttl = parsed
	}

	issued, err := s.Agents.IssueToken(r.Context(), principalFrom(r.Context()).Actor(), agents.IssueTokenInput{
		Name: body.Name, OwnerID: body.OwnerID, SystemID: body.SystemID,
		Scopes: body.Scopes, ReadOnly: body.ReadOnly, ExpiresIn: ttl,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":  issued.Token,
		"record": issued.Record,
		"note":   "Copy this token now; only its hash is stored.",
	})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	if err := s.Agents.RevokeToken(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id")); err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	q := s.DB.WithContext(r.Context()).Model(&domain.AuditLog{})
	if v := r.URL.Query().Get("action"); v != "" {
		q = q.Where("action = ?", v)
	}
	if v := r.URL.Query().Get("targetId"); v != "" {
		q = q.Where("target_id = ?", v)
	}
	if v := r.URL.Query().Get("actorId"); v != "" {
		q = q.Where("actor_id = ?", v)
	}
	if since := queryTime(r, "since"); since != nil {
		q = q.Where("created_at >= ?", *since)
	}

	var rows []domain.AuditLog
	res, err := store.Paginate(q, pageFrom(r), map[string]string{"createdAt": "created_at"}, "seq", &rows)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleVerifyAudit replays the hash chain. This is what makes the log
// tamper-evident rather than merely append-only.
func (s *Server) handleVerifyAudit(w http.ResponseWriter, r *http.Request) {
	res, err := s.Audit.Verify(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleJobStatus(w http.ResponseWriter, r *http.Request) {
	if s.Jobs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":   s.Jobs.Status(),
		"enabled": s.cfg.Job.Enabled,
	})
}

func (s *Server) handleRunJob(w http.ResponseWriter, r *http.Request) {
	if s.Jobs == nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "jobs_disabled", "Background jobs are not running.")
		return
	}
	res, err := s.Jobs.RunNow(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	res, err := s.Webhooks.List(r.Context(), webhooks.Filter{
		SystemID: r.URL.Query().Get("systemId"),
		State:    r.URL.Query().Get("state"),
		Event:    r.URL.Query().Get("event"),
	}, pageFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleReplayWebhook(w http.ResponseWriter, r *http.Request) {
	replay, err := s.Webhooks.Replay(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, replay)
}

func (s *Server) handleReplayDeadWebhooks(w http.ResponseWriter, r *http.Request) {
	n, err := s.Webhooks.ReplayAllDead(r.Context(), principalFrom(r.Context()).Actor(),
		r.URL.Query().Get("systemId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"requeued": n})
}

// ---------------------------------------------------------------------------
// Notifications
// ---------------------------------------------------------------------------

func (s *Server) mountNotifications(r chi.Router) {
	r.Route("/notifications", func(n chi.Router) {
		n.With(require(agents.PermRequestRead)).Get("/", s.handleListNotifications)
		n.With(require(agents.PermRequestRead)).Get("/stats", s.handleNotificationStats)
		n.With(require(agents.PermNotifySend)).Post("/", s.handleSendNotification)
		n.With(require(agents.PermNotifySend)).Post("/{id}/retry", s.handleRetryNotification)
	})
}

func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	res, err := s.Notifications.List(r.Context(), notifications.Filter{
		ContactID: r.URL.Query().Get("contactId"),
		RequestID: r.URL.Query().Get("requestId"),
		State:     r.URL.Query().Get("state"),
		Event:     r.URL.Query().Get("event"),
		Since:     queryTime(r, "since"),
	}, pageFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleNotificationStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.Notifications.Stats(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

type sendNotificationBody struct {
	ContactID string `json:"contactId"`
	RequestID string `json:"requestId,omitempty"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	ShortBody string `json:"shortBody,omitempty"`
}

func (s *Server) handleSendNotification(w http.ResponseWriter, r *http.Request) {
	var body sendNotificationBody
	if !decode(w, r, &body) {
		return
	}
	err := s.Notifications.SendAdHoc(r.Context(), principalFrom(r.Context()).Actor(),
		body.ContactID, body.RequestID, body.Subject, body.Body, body.ShortBody)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (s *Server) handleRetryNotification(w http.ResponseWriter, r *http.Request) {
	if err := s.Notifications.Retry(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id")); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "requeued"})
}

// ---------------------------------------------------------------------------
// Reports
// ---------------------------------------------------------------------------

func (s *Server) mountReports(r chi.Router) {
	read := require(agents.PermReportRead)
	r.Route("/reports", func(rp chi.Router) {
		rp.With(read).Get("/volume", s.handleVolumeReport)
		rp.With(read).Get("/sla", s.handleSLAReport)
		rp.With(read).Get("/agents", s.handleAgentReport)
		rp.With(read).Get("/csat", s.handleCSATReport)
		rp.With(read).Get("/geo", s.handleGeoReport)
		rp.With(read).Get("/trend/{metric}", s.handleTrend)
		rp.With(read).Get("/{name}/export.csv", s.handleExportCSV)
	})
}

func rangeFrom(r *http.Request) reports.Range {
	rng := reports.Range{
		DepartmentID: r.URL.Query().Get("departmentId"),
		QueueID:      r.URL.Query().Get("queueId"),
	}
	if from := queryTime(r, "from"); from != nil {
		rng.From = *from
	}
	if to := queryTime(r, "to"); to != nil {
		rng.To = *to
	}
	return rng
}

func (s *Server) handleVolumeReport(w http.ResponseWriter, r *http.Request) {
	res, err := s.Reports.Volume(r.Context(), rangeFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleSLAReport(w http.ResponseWriter, r *http.Request) {
	res, err := s.Reports.SLA(r.Context(), rangeFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleTrend(w http.ResponseWriter, r *http.Request) {
	res, err := s.Reports.Trend(r.Context(), chi.URLParam(r, "metric"), rangeFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": res})
}

func catalogFilterFrom(r *http.Request) catalog.ServiceTypeFilter {
	return catalog.ServiceTypeFilter{
		DepartmentID:  r.URL.Query().Get("departmentId"),
		Category:      r.URL.Query().Get("category"),
		Query:         r.URL.Query().Get("q"),
		PublicOnly:    queryBool(r, "publicOnly"),
		IncludeHidden: queryBool(r, "includeInactive"),
	}
}
