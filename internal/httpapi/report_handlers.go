package httpapi

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jjamieson1/CityConnect/internal/agents"
	"github.com/jjamieson1/CityConnect/internal/contacts"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/requests"
	"github.com/jjamieson1/CityConnect/internal/store"
)

func (s *Server) handleAgentReport(w http.ResponseWriter, r *http.Request) {
	res, err := s.Reports.Agents(r.Context(), rangeFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCSATReport(w http.ResponseWriter, r *http.Request) {
	res, err := s.Reports.CSAT(r.Context(), rangeFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGeoReport(w http.ResponseWriter, r *http.Request) {
	res, err := s.Reports.Geo(r.Context(), rangeFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleExportCSV streams a report as CSV.
//
// A council report or a FOI response is assembled in a spreadsheet, so an
// export that a person can open is not a nice-to-have. Rows stream rather than
// buffer, because "export everything" is exactly when the dataset is large.
func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	filename := name + "-" + time.Now().UTC().Format("2006-01-02") + ".csv"

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)

	cw := csv.NewWriter(w)
	defer cw.Flush()

	switch name {
	case "requests":
		s.exportRequests(w, r, cw)
	case "sla":
		s.exportSLA(w, r, cw)
	case "agents":
		s.exportAgents(w, r, cw)
	case "volume":
		s.exportVolume(w, r, cw)
	default:
		writeProblem(w, r, http.StatusNotFound, "unknown_report",
			"Available exports: requests, sla, agents, volume.")
	}
}

func (s *Server) exportRequests(w http.ResponseWriter, r *http.Request, cw *csv.Writer) {
	_ = cw.Write([]string{
		"Reference", "Subject", "Service type", "Status", "Priority",
		"Department", "Queue", "Assignee", "Contact", "Ward", "Postal code",
		"Source", "Opened", "First response", "Due", "Resolved", "Closed",
		"SLA breached", "Reopened", "CSAT",
	})

	filter := filterFrom(r)
	page := store.Page{Limit: 500, SortBy: "openedAt", Desc: true}

	// Paged rather than one enormous query, so a five-year export does not
	// hold the whole result set in memory.
	for range 200 {
		res, err := s.Requests.List(r.Context(), filter, page)
		if err != nil {
			s.log.ErrorContext(r.Context(), "csv export failed", "error", err)
			return
		}
		for i := range res.Items {
			req := &res.Items[i]
			_ = cw.Write([]string{
				req.Reference, req.Subject, nameOfType(req), string(req.Status), req.Priority,
				nameOfDept(req), nameOfQueue(req), nameOfAssignee(req), nameOfContact(req),
				req.Ward, req.PostalCode, req.Source,
				formatTime(&req.OpenedAt), formatTime(req.FirstResponseAt),
				formatTime(req.DueAt), formatTime(req.ResolvedAt), formatTime(req.ClosedAt),
				strconv.FormatBool(req.SLABreached), strconv.Itoa(req.ReopenCount),
				formatScore(req.CSATScore),
			})
		}
		cw.Flush()

		if !res.HasMore {
			return
		}
		page.Offset += page.Limit
	}
}

func (s *Server) exportSLA(w http.ResponseWriter, r *http.Request, cw *csv.Writer) {
	rep, err := s.Reports.SLA(r.Context(), rangeFrom(r))
	if err != nil {
		s.log.ErrorContext(r.Context(), "csv export failed", "error", err)
		return
	}
	_ = cw.Write([]string{"Service type", "Completed", "Breached", "Compliance %"})
	for _, row := range rep.ByType {
		_ = cw.Write([]string{
			row.Label, strconv.FormatInt(row.Total, 10),
			strconv.FormatInt(row.Breached, 10),
			strconv.FormatFloat(row.CompliancePct, 'f', 1, 64),
		})
	}
	_ = cw.Write([]string{})
	_ = cw.Write([]string{"Overall", strconv.FormatInt(rep.Total, 10),
		strconv.FormatInt(rep.Breached, 10),
		strconv.FormatFloat(rep.CompliancePct, 'f', 1, 64)})
}

func (s *Server) exportAgents(w http.ResponseWriter, r *http.Request, cw *csv.Writer) {
	rep, err := s.Reports.Agents(r.Context(), rangeFrom(r))
	if err != nil {
		s.log.ErrorContext(r.Context(), "csv export failed", "error", err)
		return
	}
	// The caveat travels with the data: a spreadsheet detached from the
	// console is exactly where these numbers get misread as a ranking.
	_ = cw.Write([]string{"# " + rep.Note})
	_ = cw.Write([]string{"Agent", "Assigned", "Closed", "Open now", "Breached",
		"Avg resolution (hours)", "CSAT average", "CSAT responses"})
	for _, row := range rep.Rows {
		_ = cw.Write([]string{
			row.Name, strconv.FormatInt(row.Assigned, 10), strconv.FormatInt(row.Closed, 10),
			strconv.FormatInt(row.OpenNow, 10), strconv.FormatInt(row.Breached, 10),
			strconv.FormatFloat(row.AvgHours, 'f', 1, 64),
			strconv.FormatFloat(row.CSATAvg, 'f', 1, 64),
			strconv.FormatInt(row.CSATResponses, 10),
		})
	}
}

func (s *Server) exportVolume(w http.ResponseWriter, r *http.Request, cw *csv.Writer) {
	rep, err := s.Reports.Volume(r.Context(), rangeFrom(r))
	if err != nil {
		s.log.ErrorContext(r.Context(), "csv export failed", "error", err)
		return
	}
	_ = cw.Write([]string{"Day", "Opened", "Closed"})
	for _, p := range rep.Series {
		_ = cw.Write([]string{p.Day,
			strconv.FormatInt(p.Opened, 10), strconv.FormatInt(p.Closed, 10)})
	}
}

func nameOfType(r *domain.Request) string {
	if r.ServiceType != nil {
		return r.ServiceType.Name
	}
	return ""
}

func nameOfDept(r *domain.Request) string {
	if r.Department != nil {
		return r.Department.Name
	}
	return ""
}

func nameOfQueue(r *domain.Request) string {
	if r.Queue != nil {
		return r.Queue.Name
	}
	return ""
}

func nameOfAssignee(r *domain.Request) string {
	switch {
	case r.AssigneeUser != nil:
		return r.AssigneeUser.Name
	case r.AssigneeSystem != nil:
		return r.AssigneeSystem.Name
	}
	return ""
}

func nameOfContact(r *domain.Request) string {
	if r.Contact != nil {
		return r.Contact.DisplayName
	}
	return ""
}

func formatTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func formatScore(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}

// ---------------------------------------------------------------------------
// Search and saved views
// ---------------------------------------------------------------------------

func (s *Server) mountSearch(r chi.Router) {
	r.With(require(agents.PermRequestRead)).Get("/search", s.handleSearch)

	r.Route("/saved-views", func(v chi.Router) {
		v.With(require(agents.PermRequestRead)).Get("/", s.handleListSavedViews)
		v.With(require(agents.PermRequestRead)).Post("/", s.handleSaveSavedView)
		v.With(require(agents.PermRequestRead)).Patch("/{id}", s.handleSaveSavedView)
		v.With(require(agents.PermRequestRead)).Delete("/{id}", s.handleDeleteSavedView)
	})
}

// searchResult is one hit in the global search.
type searchResult struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle,omitempty"`
	Reference string `json:"reference,omitempty"`
	Status    string `json:"status,omitempty"`
}

// handleSearch is the omnibox behind the console's search bar.
//
// A reference typed in full short-circuits everything else: an agent on a call
// with a citizen reading out "SR-7K4M-2QX9" wants that record, not a ranked
// list that happens to contain it.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if len(q) < 2 {
		writeJSON(w, http.StatusOK, map[string]any{"items": []searchResult{}})
		return
	}
	limit := queryInt(r, "limit", 20)
	kind := r.URL.Query().Get("type")

	out := []searchResult{}

	if req, err := s.Requests.GetByReference(r.Context(), q); err == nil {
		out = append(out, searchResult{
			Type: "request", ID: req.ID, Title: req.Subject,
			Reference: req.Reference, Status: string(req.Status),
			Subtitle: nameOfContact(req),
		})
	}

	if kind == "" || kind == "request" {
		res, err := s.Requests.List(r.Context(), requests.Filter{Query: q, ExcludeMerged: true},
			store.Page{Limit: limit, SortBy: "updatedAt", Desc: true})
		if err == nil {
			for i := range res.Items {
				req := &res.Items[i]
				if len(out) > 0 && out[0].ID == req.ID {
					continue
				}
				out = append(out, searchResult{
					Type: "request", ID: req.ID, Title: req.Subject,
					Reference: req.Reference, Status: string(req.Status),
					Subtitle: nameOfContact(req),
				})
			}
		}
	}

	if kind == "" || kind == "contact" {
		if p := principalFrom(r.Context()); p.Can(agents.PermContactRead) {
			res, err := s.Contacts.List(r.Context(),
				contacts.Filter{Query: q}, store.Page{Limit: limit, SortBy: "name"})
			if err == nil {
				for i := range res.Items {
					c := &res.Items[i]
					out = append(out, searchResult{
						Type: "contact", ID: c.ID, Title: c.DisplayName,
						Subtitle: firstNonBlank(c.PrimaryEmail, c.PrimaryPhone),
					})
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, listing(out))
}

func (s *Server) handleListSavedViews(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())

	q := s.DB.WithContext(r.Context()).Model(&domain.SavedView{}).
		Where("shared = ? OR owner_id = ?", true, p.ID())
	if entity := r.URL.Query().Get("entity"); entity != "" {
		q = q.Where("entity = ?", entity)
	}

	var items []domain.SavedView
	if err := q.Order("is_default DESC, name ASC").Find(&items).Error; err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listing(items))
}

func (s *Server) handleSaveSavedView(w http.ResponseWriter, r *http.Request) {
	var v domain.SavedView
	if !decode(w, r, &v) {
		return
	}
	p := principalFrom(r.Context())

	if id := chi.URLParam(r, "id"); id != "" {
		// Only the owner may edit a view; a shared view is readable by all but
		// not editable by all.
		var existing domain.SavedView
		if err := s.DB.WithContext(r.Context()).First(&existing, "id = ?", id).Error; err != nil {
			fail(w, r, store.ErrNotFound)
			return
		}
		if existing.OwnerID != p.ID() && !p.Can(agents.PermConfigWrite) {
			writeProblem(w, r, http.StatusForbidden, "forbidden", "That view belongs to somebody else.")
			return
		}
		v.ID = id
	}
	if v.OwnerID == "" {
		v.OwnerID = p.ID()
	}
	if v.Entity == "" {
		v.Entity = "request"
	}

	if err := s.DB.WithContext(r.Context()).Save(&v).Error; err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleDeleteSavedView(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	res := s.DB.WithContext(r.Context()).
		Where("id = ? AND (owner_id = ? OR ?)", chi.URLParam(r, "id"), p.ID(), p.Can(agents.PermConfigWrite)).
		Delete(&domain.SavedView{})
	if res.Error != nil {
		fail(w, r, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		fail(w, r, store.ErrNotFound)
		return
	}
	writeNoContent(w)
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
