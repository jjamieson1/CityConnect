package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jjamieson1/CityConnect/internal/agents"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/requests"
)

func (s *Server) mountRequests(r chi.Router) {
	r.Route("/requests", func(rt chi.Router) {
		rt.With(require(agents.PermRequestRead)).Get("/", s.handleListRequests)
		rt.With(require(agents.PermRequestRead)).Get("/count", s.handleCountRequests)
		rt.With(require(agents.PermRequestWrite)).Post("/", s.handleCreateRequest)
		rt.With(require(agents.PermRequestWrite)).Post("/bulk", s.handleBulkRequests)

		rt.Route("/{id}", func(one chi.Router) {
			one.With(require(agents.PermRequestRead)).Get("/", s.handleGetRequest)
			one.With(require(agents.PermRequestWrite)).Patch("/", s.handleUpdateRequest)
			one.With(require(agents.PermRequestWrite)).Post("/transition", s.handleTransition)
			one.With(require(agents.PermRequestAssign)).Post("/assign", s.handleAssign)
			one.With(require(agents.PermRequestTransfer)).Post("/transfer", s.handleTransfer)
			one.With(require(agents.PermRequestWrite)).Post("/macros/{macroId}", s.handleApplyMacro)

			one.With(require(agents.PermRequestRead)).Get("/events", s.handleRequestEvents)
			one.With(require(agents.PermRequestRead)).Get("/comments", s.handleListComments)
			one.With(require(agents.PermRequestWrite)).Post("/comments", s.handleAddComment)

			one.With(require(agents.PermRequestRead)).Get("/links", s.handleListLinks)
			one.With(require(agents.PermRequestWrite)).Post("/links", s.handleAddLink)
			one.With(require(agents.PermRequestWrite)).Delete("/links/{linkId}", s.handleUnlink)

			one.With(require(agents.PermRequestRead)).Get("/attachments", s.handleListAttachments)
			one.With(require(agents.PermRequestWrite)).Post("/attachments", s.handleUpload)
			one.With(require(agents.PermRequestRead)).Get("/attachments/{attachmentId}", s.handleDownload)
			one.With(require(agents.PermRequestWrite)).Delete("/attachments/{attachmentId}", s.handleDeleteAttachment)
		})
	})

	// A citizen quotes a reference, never a UUID, so agents can look one up
	// directly without a search.
	r.With(require(agents.PermRequestRead)).Get("/requests-by-reference/{reference}", s.handleGetByReference)
}

func filterFrom(r *http.Request) requests.Filter {
	return requests.Filter{
		Query:          r.URL.Query().Get("q"),
		Status:         queryList(r, "status"),
		Priority:       queryList(r, "priority"),
		QueueID:        r.URL.Query().Get("queueId"),
		DepartmentID:   r.URL.Query().Get("departmentId"),
		ServiceTypeID:  r.URL.Query().Get("serviceTypeId"),
		AssigneeUserID: r.URL.Query().Get("assigneeUserId"),
		AssigneeSysID:  r.URL.Query().Get("assigneeSystemId"),
		ContactID:      r.URL.Query().Get("contactId"),
		Ward:           r.URL.Query().Get("ward"),
		Tag:            r.URL.Query().Get("tag"),
		Source:         r.URL.Query().Get("source"),
		ExternalRef:    r.URL.Query().Get("externalRef"),
		OpenOnly:       queryBool(r, "openOnly"),
		Unassigned:     queryBool(r, "unassigned"),
		Breached:       queryBool(r, "breached"),
		DueBefore:      queryTime(r, "dueBefore"),
		OpenedAfter:    queryTime(r, "openedAfter"),
		OpenedBefore:   queryTime(r, "openedBefore"),
		ExcludeMerged:  !queryBool(r, "includeMerged"),
	}
}

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	res, err := s.Requests.List(r.Context(), filterFrom(r), pageFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCountRequests(w http.ResponseWriter, r *http.Request) {
	n, err := s.Requests.Count(r.Context(), filterFrom(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"count": n})
}

func (s *Server) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	req, err := s.Requests.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (s *Server) handleGetByReference(w http.ResponseWriter, r *http.Request) {
	req, err := s.Requests.GetByReference(r.Context(), chi.URLParam(r, "reference"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

type createRequestBody struct {
	ContactID       string         `json:"contactId"`
	ServiceTypeID   string         `json:"serviceTypeId,omitempty"`
	ServiceTypeCode string         `json:"serviceTypeCode,omitempty"`
	Subject         string         `json:"subject,omitempty"`
	Description     string         `json:"description,omitempty"`
	Priority        string         `json:"priority,omitempty"`
	Source          string         `json:"source,omitempty"`
	OriginSystem    string         `json:"originSystem,omitempty"`
	ExternalRef     string         `json:"externalRef,omitempty"`
	Address1        string         `json:"address1,omitempty"`
	Address2        string         `json:"address2,omitempty"`
	City            string         `json:"city,omitempty"`
	State           string         `json:"state,omitempty"`
	PostalCode      string         `json:"postalCode,omitempty"`
	Ward            string         `json:"ward,omitempty"`
	ParcelID        string         `json:"parcelId,omitempty"`
	Latitude        float64        `json:"latitude,omitempty"`
	Longitude       float64        `json:"longitude,omitempty"`
	FormData        domain.JSONMap `json:"formData,omitempty"`
	Tags            []string       `json:"tags,omitempty"`
	QueueID         string         `json:"queueId,omitempty"`
}

func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	var body createRequestBody
	if !decode(w, r, &body) {
		return
	}
	p := principalFrom(r.Context())

	// A connected system's requests are attributed to it automatically, so an
	// integration cannot claim a different origin.
	source := body.Source
	origin := body.OriginSystem
	if p.IsSystem() {
		source = domain.SourceAPI
		origin = p.System.Code
	}

	req, err := s.Requests.Create(r.Context(), p.Actor(), requests.CreateInput{
		ContactID: body.ContactID, ServiceTypeID: body.ServiceTypeID,
		ServiceTypeCode: body.ServiceTypeCode, Subject: body.Subject,
		Description: body.Description, Priority: body.Priority,
		Source: source, OriginSystem: origin, ExternalRef: body.ExternalRef,
		Address1: body.Address1, Address2: body.Address2, City: body.City,
		State: body.State, PostalCode: body.PostalCode, Ward: body.Ward,
		ParcelID: body.ParcelID, Latitude: body.Latitude, Longitude: body.Longitude,
		FormData: body.FormData, Tags: body.Tags, QueueID: body.QueueID,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	s.invalidateCallout(r, req.ContactID)
	writeJSON(w, http.StatusCreated, req)
}

type updateRequestBody struct {
	Subject     *string        `json:"subject,omitempty"`
	Description *string        `json:"description,omitempty"`
	Priority    *string        `json:"priority,omitempty"`
	Address1    *string        `json:"address1,omitempty"`
	Address2    *string        `json:"address2,omitempty"`
	City        *string        `json:"city,omitempty"`
	State       *string        `json:"state,omitempty"`
	PostalCode  *string        `json:"postalCode,omitempty"`
	Ward        *string        `json:"ward,omitempty"`
	ParcelID    *string        `json:"parcelId,omitempty"`
	Latitude    *float64       `json:"latitude,omitempty"`
	Longitude   *float64       `json:"longitude,omitempty"`
	Tags        *[]string      `json:"tags,omitempty"`
	FormData    domain.JSONMap `json:"formData,omitempty"`
	Version     uint           `json:"version,omitempty"`
}

func (s *Server) handleUpdateRequest(w http.ResponseWriter, r *http.Request) {
	var body updateRequestBody
	if !decode(w, r, &body) {
		return
	}
	req, err := s.Requests.Update(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), requests.UpdateInput{
			Subject: body.Subject, Description: body.Description, Priority: body.Priority,
			Address1: body.Address1, Address2: body.Address2, City: body.City,
			State: body.State, PostalCode: body.PostalCode, Ward: body.Ward,
			ParcelID: body.ParcelID, Latitude: body.Latitude, Longitude: body.Longitude,
			Tags: body.Tags, FormData: body.FormData, ExpectedVersion: body.Version,
		})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

type transitionBody struct {
	To             string `json:"to"`
	Note           string `json:"note,omitempty"`
	ResolutionCode string `json:"resolutionCode,omitempty"`
	NotifyCitizen  bool   `json:"notifyCitizen,omitempty"`
	Version        uint   `json:"version,omitempty"`
}

func (s *Server) handleTransition(w http.ResponseWriter, r *http.Request) {
	var body transitionBody
	if !decode(w, r, &body) {
		return
	}
	req, err := s.Requests.Transition(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), requests.TransitionInput{
			To: domain.RequestStatus(body.To), Note: body.Note,
			ResolutionCode: body.ResolutionCode, NotifyCitizen: body.NotifyCitizen,
			ExpectedVersion: body.Version,
		})
	if err != nil {
		fail(w, r, err)
		return
	}
	s.invalidateCallout(r, req.ContactID)
	writeJSON(w, http.StatusOK, req)
}

type assignBody struct {
	UserID   string `json:"userId,omitempty"`
	SystemID string `json:"systemId,omitempty"`
	Version  uint   `json:"version,omitempty"`
}

func (s *Server) handleAssign(w http.ResponseWriter, r *http.Request) {
	var body assignBody
	if !decode(w, r, &body) {
		return
	}
	req, err := s.Requests.Assign(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), requests.AssignInput{
			UserID: body.UserID, SystemID: body.SystemID, ExpectedVersion: body.Version,
		})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

type transferBody struct {
	DepartmentID string `json:"departmentId,omitempty"`
	QueueID      string `json:"queueId,omitempty"`
	Note         string `json:"note,omitempty"`
	Reassign     bool   `json:"reassign,omitempty"`
}

func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	var body transferBody
	if !decode(w, r, &body) {
		return
	}
	req, err := s.Requests.Transfer(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), requests.TransferInput{
			DepartmentID: body.DepartmentID, QueueID: body.QueueID,
			Note: body.Note, Reassign: body.Reassign,
		})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (s *Server) handleApplyMacro(w http.ResponseWriter, r *http.Request) {
	req, err := s.Requests.ApplyMacro(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), chi.URLParam(r, "macroId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

type bulkBody struct {
	RequestIDs []string `json:"requestIds"`
	Operation  string   `json:"operation"`
	UserID     string   `json:"userId,omitempty"`
	SystemID   string   `json:"systemId,omitempty"`
	Status     string   `json:"status,omitempty"`
	Priority   string   `json:"priority,omitempty"`
	QueueID    string   `json:"queueId,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Note       string   `json:"note,omitempty"`
}

func (s *Server) handleBulkRequests(w http.ResponseWriter, r *http.Request) {
	var body bulkBody
	if !decode(w, r, &body) {
		return
	}
	res, err := s.Requests.Bulk(r.Context(), principalFrom(r.Context()).Actor(), requests.BulkAction{
		RequestIDs: body.RequestIDs, Operation: body.Operation,
		UserID: body.UserID, SystemID: body.SystemID,
		Status: domain.RequestStatus(body.Status), Priority: body.Priority,
		QueueID: body.QueueID, Tags: body.Tags, Note: body.Note,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	// 207 rather than 200: a bulk action that partly failed is not a success,
	// and the client must look at the per-item results.
	status := http.StatusOK
	if len(res.Failed) > 0 {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, res)
}

func (s *Server) handleRequestEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.Requests.Events(r.Context(), chi.URLParam(r, "id"), queryInt(r, "limit", 200))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listing(events))
}

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	comments, err := s.Requests.Comments(r.Context(), chi.URLParam(r, "id"), queryBool(r, "citizenOnly"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listing(comments))
}

type commentBody struct {
	Body          string `json:"body"`
	Visibility    string `json:"visibility,omitempty"`
	NotifyCitizen bool   `json:"notifyCitizen,omitempty"`
	MacroID       string `json:"macroId,omitempty"`
}

func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request) {
	var body commentBody
	if !decode(w, r, &body) {
		return
	}
	id := chi.URLParam(r, "id")

	comment, err := s.Requests.AddComment(r.Context(), principalFrom(r.Context()).Actor(), id,
		requests.CommentInput{
			Body: body.Body, Visibility: domain.CommentVisibility(body.Visibility),
			NotifyCitizen: body.NotifyCitizen, MacroID: body.MacroID,
		})
	if err != nil {
		fail(w, r, err)
		return
	}
	if req, err := s.Requests.Get(r.Context(), id); err == nil {
		s.invalidateCallout(r, req.ContactID)
	}
	writeJSON(w, http.StatusCreated, comment)
}

func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.Requests.Links(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listing(links))
}

type linkBody struct {
	TargetID  string `json:"targetId,omitempty"`
	TargetRef string `json:"targetReference,omitempty"`
	Kind      string `json:"kind"`
	Note      string `json:"note,omitempty"`
}

func (s *Server) handleAddLink(w http.ResponseWriter, r *http.Request) {
	var body linkBody
	if !decode(w, r, &body) {
		return
	}
	link, err := s.Requests.Link(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), requests.LinkInput{
			TargetID: body.TargetID, TargetRef: body.TargetRef,
			Kind: body.Kind, Note: body.Note,
		})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

func (s *Server) handleUnlink(w http.ResponseWriter, r *http.Request) {
	err := s.Requests.Unlink(r.Context(), principalFrom(r.Context()).Actor(),
		chi.URLParam(r, "id"), chi.URLParam(r, "linkId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}

// invalidateCallout drops the citizen's cached Service Card bundle so their
// next render shows the change rather than a stale snapshot.
func (s *Server) invalidateCallout(r *http.Request, contactID string) {
	if s.Callout == nil || contactID == "" {
		return
	}
	contact, err := s.Contacts.Get(r.Context(), contactID)
	if err != nil {
		return
	}
	for _, ident := range contact.Identities {
		if ident.Provider == domain.ProviderC2 {
			s.Callout.Invalidate(ident.ExternalID)
		}
	}
}

func (s *Server) handleListAttachments(w http.ResponseWriter, r *http.Request) {
	items, err := s.Requests.Attachments(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listing(items))
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	maxBytes := s.cfg.AttachmentMaxMB << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))

	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_upload",
			"Could not read the uploaded file.")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "missing_file", "A file field is required.")
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	att, err := s.Requests.Attach(r.Context(), principalFrom(r.Context()).Actor(), s.Attachments,
		chi.URLParam(r, "id"), requests.AttachInput{
			Filename: header.Filename, ContentType: contentType,
			Visibility: domain.CommentVisibility(r.FormValue("visibility")),
			Reader:     file, Size: header.Size,
		})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, att)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	att, err := s.Requests.GetAttachment(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "attachmentId"))
	if err != nil {
		fail(w, r, err)
		return
	}

	// Force a download rather than inline rendering: a stored SVG or HTML file
	// served inline from this origin would execute against the session.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+strings.ReplaceAll(att.Filename, `"`, "")+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, s.Attachments.Path(att.StoragePath))
}

func (s *Server) handleDeleteAttachment(w http.ResponseWriter, r *http.Request) {
	err := s.Requests.DeleteAttachment(r.Context(), principalFrom(r.Context()).Actor(),
		s.Attachments, chi.URLParam(r, "id"), chi.URLParam(r, "attachmentId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeNoContent(w)
}
