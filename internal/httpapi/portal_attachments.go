package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/requests"
)

// Limits on what one public report may carry.
//
// These bound a *legitimate* rate of submission. The form token already caps
// how fast reports can be filed at all; this caps how much one of them can
// weigh, which is the part uploads make expensive — disk, and scanner time.
const (
	// maxPublicAttachments per request. Enough for a resident to show the
	// pothole, the street sign beside it and the puddle it becomes in the rain;
	// not enough to be a file host.
	maxPublicAttachments = 5

	// maxPublicAttachmentBytes across all of them, checked in addition to the
	// per-file limit. Five files each a byte under the per-file cap is the
	// shape a per-file limit alone misses.
	maxPublicAttachmentBytes = 40 << 20
)

// handlePortalUpload attaches a photo to a request from the citizen portal.
//
// Authorised two ways, because the two kinds of reporter are genuinely
// different. A signed-in resident is authorised by their session and by owning
// the request. Somebody who reported anonymously has no session and never will,
// so they present the short-lived grant issued when the report was filed —
// bound to that one request id, so it cannot be turned on anyone else's.
func (s *Server) handlePortalUpload(w http.ResponseWriter, r *http.Request) {
	reference := chi.URLParam(r, "reference")

	req, err := s.Requests.GetByReference(r.Context(), reference)
	if err != nil {
		// Same answer as an unauthorised one below: whether a reference exists
		// is not something to confirm to a caller who cannot use it.
		writeProblem(w, r, http.StatusNotFound, "not_found", "We could not find that report.")
		return
	}

	if !s.mayAttachTo(r, req) {
		writeProblem(w, r, http.StatusNotFound, "not_found", "We could not find that report.")
		return
	}

	// The per-address limit applies to everyone: an upload costs disk and
	// scanner time whoever sends it.
	if !s.submitter.allow("upload|" + clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeProblem(w, r, http.StatusTooManyRequests, "rate_limited",
			"Too many uploads from this connection just now. Wait a minute and try again.")
		return
	}

	existing, err := s.Requests.Attachments(r.Context(), req.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(existing) >= maxPublicAttachments {
		writeProblem(w, r, http.StatusBadRequest, "too_many_attachments",
			"That report already has the maximum number of files.")
		return
	}
	var used int64
	for i := range existing {
		used += existing[i].SizeBytes
	}

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

	if used+header.Size > maxPublicAttachmentBytes {
		writeProblem(w, r, http.StatusBadRequest, "attachments_too_large",
			"The files on that report would be too large in total.")
		return
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Citizen-visible: the resident who sent it must be able to see it back,
	// and a photo they took is not an internal note.
	att, err := s.Requests.Attach(r.Context(), audit.SystemActor("", "citizen upload", clientIP(r)),
		s.Attachments, req.ID, requests.AttachInput{
			Filename: header.Filename, ContentType: contentType,
			Visibility: domain.VisibilityCitizen,
			Reader:     file, Size: header.Size,
		})
	if err != nil {
		failPortal(w, r, err)
		return
	}

	// Deliberately narrow: a resident is told their photo arrived and whether
	// it is through the scanner yet. Storage paths, checksums and scan notes
	// are operational detail and none of their business.
	writeJSON(w, http.StatusCreated, map[string]any{
		"filename": att.Filename,
		"scanned":  att.Servable(),
	})
}

// mayAttachTo reports whether this caller may add a file to this request.
func (s *Server) mayAttachTo(r *http.Request, req *domain.Request) bool {
	if grant := r.Header.Get("X-Upload-Grant"); grant != "" {
		if err := s.forms.verifyUpload(grant, req.ID, time.Now()); err == nil {
			return true
		}
		// A bad grant is not a reason to fall through to the session check —
		// but a resident who is *also* signed in should not be punished for a
		// stale one, so keep going rather than refusing here.
	}

	// An anonymous report has no owner, so a session cannot authorise it. Only
	// the grant can, and it has already been tried.
	if req.Anonymous() {
		return false
	}

	contact := s.optionalCitizen(r)
	return contact != nil && contact.ID == req.ContactID
}
