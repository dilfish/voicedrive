package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/dilfish/voicedrive/internal/store"
)

type Handler struct{ store *store.Postgres }

func NewHandler(s *store.Postgres) http.Handler {
	h := &Handler{store: s}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/devices", h.device)
	mux.HandleFunc("POST /v1/groups", h.group)
	mux.HandleFunc("POST /v1/groups/{id}/members", h.member)
	mux.HandleFunc("POST /v1/groups/{id}/messages", h.message)
	return mux
}

func (h *Handler) device(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MAC     string `json:"mac"`
		Name    string `json:"name"`
		AgentID string `json:"agent_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.MAC) == "" {
		failStatus(w, http.StatusBadRequest, errors.New("mac is required"))
		return
	}
	d, err := h.store.EnsureDevice(r.Context(), in.MAC, in.Name, in.AgentID)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, http.StatusOK, d)
}
func (h *Handler) group(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MAC        string `json:"mac"`
		Name       string `json:"name"`
		InviteCode string `json:"invite_code"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.MAC) == "" || strings.TrimSpace(in.Name) == "" {
		failStatus(w, http.StatusBadRequest, errors.New("mac and name are required"))
		return
	}
	g, err := h.store.CreateGroup(r.Context(), in.MAC, in.Name, in.InviteCode)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, http.StatusCreated, g)
}
func (h *Handler) member(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MAC   string `json:"mac"`
		Alias string `json:"alias"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.MAC) == "" || strings.TrimSpace(in.Alias) == "" {
		failStatus(w, http.StatusBadRequest, errors.New("mac and alias are required"))
		return
	}
	m, err := h.store.AddMember(r.Context(), r.PathValue("id"), in.MAC, in.Alias)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, http.StatusCreated, m)
}
func (h *Handler) message(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MAC        string `json:"mac"`
		Text       string `json:"text"`
		AudioURL   string `json:"audio_url"`
		DurationMS int    `json:"duration_ms"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.MAC) == "" || strings.TrimSpace(in.Text) == "" {
		failStatus(w, http.StatusBadRequest, errors.New("mac and text are required"))
		return
	}
	m, err := h.store.CreateMessage(r.Context(), r.PathValue("id"), in.MAC, in.Text, in.AudioURL, in.DurationMS, r.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, err)
		return
	}
	write(w, http.StatusCreated, m)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		failStatus(w, http.StatusBadRequest, err)
		return false
	}
	return true
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}
func fail(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, store.ErrConflict) {
		status = http.StatusConflict
	}
	failStatus(w, status, err)
}
func failStatus(w http.ResponseWriter, status int, err error) {
	http.Error(w, err.Error(), status)
}
