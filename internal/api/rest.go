package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/giantswarm/skill-manager/internal/catalog"
)

// Prefix is the REST API's path prefix.
const Prefix = "/v1"

// maxBodyBytes bounds a request body.
const maxBodyBytes = 1 << 20

// REST is the catalog over HTTP, for agent-manager and the portal: the same
// semantics as the MCP tools.
type REST struct {
	svc *catalog.Service
	log *slog.Logger
}

// NewREST builds the REST handlers.
func NewREST(svc *catalog.Service, log *slog.Logger) *REST {
	if log == nil {
		log = slog.Default()
	}
	return &REST{svc: svc, log: log}
}

// Patterns are the routes' ServeMux patterns.
var Patterns = []string{"GET " + Prefix + "/sources", "GET " + Prefix + "/skills", "POST " + Prefix + "/resolve",
	"GET " + Prefix + "/skills/content", "GET " + Prefix + "/skills/content/file"}

// Register mounts the routes on mux, in the order of Patterns.
func (h *REST) Register(mux *http.ServeMux) {
	for i, f := range []http.HandlerFunc{h.sources, h.skills, h.resolve, h.skillContent, h.skillFile} {
		mux.HandleFunc(Patterns[i], f)
	}
}

func (h *REST) sources(w http.ResponseWriter, r *http.Request) {
	list, withheld, err := h.svc.Sources(r.Context())
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SourcesResult{Sources: list, Withheld: withheld})
}

func (h *REST) skills(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := h.svc.List(r.Context(), q.Get("source"), strings.EqualFold(q.Get("refresh"), "true"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *REST) skillContent(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := h.svc.Skill(r.Context(), q.Get("url"), q.Get("commit"), q.Get("path"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *REST) skillFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := h.svc.SkillFile(r.Context(), q.Get("url"), q.Get("commit"), q.Get("path"), q.Get("file"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *REST) resolve(w http.ResponseWriter, r *http.Request) {
	var req catalog.Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid request body: " + err.Error()})
		return
	}
	res, err := h.svc.Resolve(r.Context(), req)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// errorBody is every refusal's body; Item and List name the refused item of
// a resolve.
type errorBody struct {
	Error string `json:"error"`
	Item  *int   `json:"item,omitempty"`
	List  string `json:"list,omitempty"`
	// Code, Apps and Message are set only for a missing grant: code
	// grant_required, the provider Apps (muster's names) the person must
	// grant, and why.
	Code    string   `json:"code,omitempty"`
	Apps    []string `json:"apps,omitempty"`
	Message string   `json:"message,omitempty"`
}

func (h *REST) writeError(w http.ResponseWriter, err error) {
	body := errorBody{Error: err.Error()}
	var item *catalog.ItemError
	if errors.As(err, &item) {
		// item and list name it: the message is the cause alone.
		body.Error, body.Item, body.List = item.Err.Error(), &item.Index, item.List
	}
	if grant, ok := errors.AsType[*catalog.ErrGrantRequired](err); ok {
		body.Code, body.Apps, body.Message = statusGrantRequired, []string{grant.App}, grant.Reason
	}
	status := statusOf(err)
	if status >= http.StatusInternalServerError {
		h.log.Warn("request failed upstream", "error", err)
	}
	writeJSON(w, status, body)
}

// statusOf is the HTTP status of a catalog error. A missing grant is 403:
// the bearer is valid, the person's GitHub grant is what is missing.
func statusOf(err error) int {
	if _, ok := errors.AsType[*catalog.ErrGrantRequired](err); ok {
		return http.StatusForbidden
	}
	switch {
	case errors.Is(err, catalog.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, catalog.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, catalog.ErrUpstream):
		return http.StatusBadGateway
	case errors.Is(err, catalog.ErrInvalid):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
