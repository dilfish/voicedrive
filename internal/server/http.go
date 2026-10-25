package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/dilfish/voicedrive/internal/model"
	"github.com/dilfish/voicedrive/internal/store"
)

type Handler struct {
	store *store.Postgres
	hub   *deliveryHub
}

func NewHandler(s *store.Postgres) http.Handler {
	h := &Handler{store: s, hub: newDeliveryHub(s)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/devices", h.device)
	mux.HandleFunc("POST /v1/groups", h.group)
	mux.HandleFunc("POST /v1/groups/{id}/members", h.member)
	mux.HandleFunc("POST /v1/groups/{id}/switch", h.switchGroup)
	mux.HandleFunc("POST /v1/groups/switch", h.switchGroupByName)
	mux.HandleFunc("POST /v1/groups/{id}/messages", h.message)
	mux.HandleFunc("GET /v1/device", h.hub.serveHTTP)
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

func (h *Handler) switchGroup(w http.ResponseWriter, r *http.Request) {
	mac := strings.TrimSpace(r.Header.Get("X-VD-MAC"))
	if mac == "" {
		failStatus(w, http.StatusBadRequest, errors.New("X-VD-MAC is required"))
		return
	}
	group, err := h.store.SwitchActiveGroup(r.Context(), mac, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if err := h.hub.notifyGroupChanged(r.Context(), mac, group); err != nil {
		fail(w, err)
		return
	}
	writeSwitchResult(w, group)
}

func (h *Handler) switchGroupByName(w http.ResponseWriter, r *http.Request) {
	mac := strings.TrimSpace(r.Header.Get("X-VD-MAC"))
	if mac == "" {
		failStatus(w, http.StatusBadRequest, errors.New("X-VD-MAC is required"))
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		failStatus(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	group, err := h.store.SwitchActiveGroupByName(r.Context(), mac, in.Name)
	if err != nil {
		fail(w, err)
		return
	}
	if err := h.hub.notifyGroupChanged(r.Context(), mac, group); err != nil {
		fail(w, err)
		return
	}
	writeSwitchResult(w, group)
}

func writeSwitchResult(w http.ResponseWriter, group model.Group) {
	write(w, http.StatusOK, struct {
		Group  model.Group `json:"group"`
		Prompt string      `json:"prompt"`
	}{Group: group, Prompt: "现在只听" + group.Name})
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
	if err := h.hub.broadcast(r.Context(), m); err != nil {
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
