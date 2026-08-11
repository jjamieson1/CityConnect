package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jjamieson1/CityConnect/internal/agents"
	"github.com/jjamieson1/CityConnect/internal/contacts"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/interactions"
)

func (s *Server) mountContacts(r chi.Router) {
	r.Route("/contacts", func(c chi.Router) {
		c.With(require(agents.PermContactRead)).Get("/", s.handleListContacts)
		c.With(require(agents.PermContactWrite)).Post("/", s.handleCreateContact)

		c.Route("/{id}", func(one chi.Router) {
			one.With(require(agents.PermContactRead)).Get("/", s.handleGetContact)
			one.With(require(agents.PermContactWrite)).Patch("/", s.handleUpdateContact)
			one.With(require(agents.PermContactWrite)).Delete("/", s.handleDeleteContact)

			one.With(require(agents.PermContactRead)).Get("/timeline", s.handleContactTimeline)
			one.With(require(agents.PermContactRead)).Get("/duplicates", s.handleFindDuplicates)
			one.With(require(agents.PermContactMerge)).Post("/merge", s.handleMerge)
			one.With(require(agents.PermContactRead)).Get("/merges", s.handleMergeHistory)

			one.With(require(agents.PermContactWrite)).Post("/identities", s.handleAddIdentity)
			one.With(require(agents.PermContactWrite)).Delete("/identities/{identityId}", s.handleRemoveIdentity)

			one.With(require(agents.PermContactWrite)).Post("/channels", s.handleSaveChannel)
			one.With(require(agents.PermContactWrite)).Delete("/channels/{channelId}", s.handleDeleteChannel)

			one.With(require(agents.PermContactRead)).Get("/consents", s.handleListConsents)
			one.With(require(agents.PermContactWrite)).Post("/consents", s.handleSetConsent)
		})
	})

	r.With(require(agents.PermContactMerge)).Post("/contact-merges/{mergeId}/undo", s.handleUnmerge)

	r.Route("/contact-groups", func(g chi.Router) {
		g.With(require(agents.PermContactRead)).Get("/", s.handleListGroups)
		g.With(require(agents.PermContactWrite)).Post("/", s.handleSaveGroup)
		g.With(require(agents.PermContactWrite)).Patch("/{id}", s.handleSaveGroup)
		g.With(require(agents.PermContactWrite)).Delete("/{id}", s.handleDeleteGroup)
		g.With(require(agents.PermContactWrite)).Post("/{id}/members", s.handleAddGroupMembers)
		g.With(require(agents.PermContactWrite)).Delete("/{id}/members", s.handleRemoveGroupMembers)
	})
}

func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	res, err := s.Contacts.List(r.Context(), contacts.Filter{
		Query:        r.URL.Query().Get("q"),
		Tag:          r.URL.Query().Get("tag"),
		GroupID:      r.URL.Query().Get("groupId"),
		Ward:         r.URL.Query().Get("ward"),
		PostalCode:   r.URL.Query().Get("postalCode"),
		Status:       r.URL.Query().Get("status"),
		C2Reachable:  queryBoolPtr(r, "c2Reachable"),
		DoNotContact: queryBoolPtr(r, "doNotContact"),
		HasC2Link:    queryBoolPtr(r, "hasC2Link"),
	}, pageFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGetContact(w http.ResponseWriter, r *http.Request) {
	c, err := s.Contacts.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleCreateContact(w http.ResponseWriter, r *http.Request) {
	var c domain.Contact
	if !decode(w, r, &c) {
		return
	}
	created, err := s.Contacts.Create(r.Context(), principalFrom(r.Context()).Actor(), &c)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateContact(w http.ResponseWriter, r *http.Request) {
	var c domain.Contact
	if !decode(w, r, &c) {
		return
	}
	updated, err := s.Contacts.Update(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), c.Version, &c)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteContact(w http.ResponseWriter, r *http.Request) {
	if err := s.Contacts.Delete(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id")); err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) handleContactTimeline(w http.ResponseWriter, r *http.Request) {
	items, err := s.Interactions.Timeline(r.Context(), chi.URLParam(r, "id"), queryInt(r, "limit", 100))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleFindDuplicates(w http.ResponseWriter, r *http.Request) {
	items, err := s.Contacts.FindDuplicates(r.Context(), chi.URLParam(r, "id"), queryInt(r, "limit", 10))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type mergeBody struct {
	MergedID     string            `json:"mergedId"`
	FieldChoices map[string]string `json:"fieldChoices,omitempty"`
	Note         string            `json:"note,omitempty"`
}

func (s *Server) handleMerge(w http.ResponseWriter, r *http.Request) {
	var body mergeBody
	if !decode(w, r, &body) {
		return
	}
	survivor, err := s.Contacts.Merge(r.Context(), principalFrom(r.Context()).Actor(), contacts.MergeInput{
		SurvivorID: chi.URLParam(r, "id"), MergedID: body.MergedID,
		FieldChoices: body.FieldChoices, Note: body.Note,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, survivor)
}

func (s *Server) handleUnmerge(w http.ResponseWriter, r *http.Request) {
	restored, err := s.Contacts.Unmerge(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "mergeId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, restored)
}

func (s *Server) handleMergeHistory(w http.ResponseWriter, r *http.Request) {
	items, err := s.Contacts.MergeHistory(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleAddIdentity(w http.ResponseWriter, r *http.Request) {
	var ident domain.ContactIdentity
	if !decode(w, r, &ident) {
		return
	}
	created, err := s.Contacts.AddIdentity(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), &ident)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleRemoveIdentity(w http.ResponseWriter, r *http.Request) {
	err := s.Contacts.RemoveIdentity(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), chi.URLParam(r, "identityId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) handleSaveChannel(w http.ResponseWriter, r *http.Request) {
	var ch domain.ContactChannel
	if !decode(w, r, &ch) {
		return
	}
	saved, err := s.Contacts.SaveChannel(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), &ch)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	err := s.Contacts.DeleteChannel(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), chi.URLParam(r, "channelId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) handleListConsents(w http.ResponseWriter, r *http.Request) {
	items, err := s.Contacts.Consents(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleSetConsent(w http.ResponseWriter, r *http.Request) {
	var pref domain.ConsentPreference
	if !decode(w, r, &pref) {
		return
	}
	err := s.Contacts.SetConsent(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), &pref)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pref)
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	items, err := s.Contacts.ListGroups(r.Context(), queryBool(r, "includeInactive"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleSaveGroup(w http.ResponseWriter, r *http.Request) {
	var g domain.ContactGroup
	if !decode(w, r, &g) {
		return
	}
	if id := chi.URLParam(r, "id"); id != "" {
		g.ID = id
	}
	saved, err := s.Contacts.SaveGroup(r.Context(), principalFrom(r.Context()).Actor(), &g)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if err := s.Contacts.DeleteGroup(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id")); err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}

type groupMembersBody struct {
	ContactIDs []string `json:"contactIds"`
}

func (s *Server) handleAddGroupMembers(w http.ResponseWriter, r *http.Request) {
	var body groupMembersBody
	if !decode(w, r, &body) {
		return
	}
	n, err := s.Contacts.AddToGroup(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), body.ContactIDs)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": n})
}

func (s *Server) handleRemoveGroupMembers(w http.ResponseWriter, r *http.Request) {
	var body groupMembersBody
	if !decode(w, r, &body) {
		return
	}
	n, err := s.Contacts.RemoveFromGroup(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), body.ContactIDs)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"removed": n})
}

// ---------------------------------------------------------------------------
// Interactions
// ---------------------------------------------------------------------------

func (s *Server) mountInteractions(r chi.Router) {
	r.Route("/interactions", func(i chi.Router) {
		i.With(require(agents.PermContactRead)).Get("/", s.handleListInteractions)
		i.With(require(agents.PermContactWrite)).Post("/", s.handleCreateInteraction)
		i.With(require(agents.PermContactRead)).Get("/{id}", s.handleGetInteraction)
		i.With(require(agents.PermContactWrite)).Patch("/{id}", s.handleUpdateInteraction)
		i.With(require(agents.PermContactWrite)).Delete("/{id}", s.handleDeleteInteraction)
	})
}

func (s *Server) handleListInteractions(w http.ResponseWriter, r *http.Request) {
	res, err := s.Interactions.List(r.Context(), interactions.Filter{
		ContactID:    r.URL.Query().Get("contactId"),
		RequestID:    r.URL.Query().Get("requestId"),
		UserID:       r.URL.Query().Get("userId"),
		DepartmentID: r.URL.Query().Get("departmentId"),
		Kind:         r.URL.Query().Get("kind"),
		Direction:    r.URL.Query().Get("direction"),
		Since:        queryTime(r, "since"),
		Until:        queryTime(r, "until"),
		Query:        r.URL.Query().Get("q"),
	}, pageFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGetInteraction(w http.ResponseWriter, r *http.Request) {
	it, err := s.Interactions.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) handleCreateInteraction(w http.ResponseWriter, r *http.Request) {
	var it domain.Interaction
	if !decode(w, r, &it) {
		return
	}
	created, err := s.Interactions.Create(r.Context(), principalFrom(r.Context()).Actor(), &it)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateInteraction(w http.ResponseWriter, r *http.Request) {
	var it domain.Interaction
	if !decode(w, r, &it) {
		return
	}
	updated, err := s.Interactions.Update(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), &it)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteInteraction(w http.ResponseWriter, r *http.Request) {
	if err := s.Interactions.Delete(r.Context(), principalFrom(r.Context()).Actor(), chi.URLParam(r, "id")); err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}
