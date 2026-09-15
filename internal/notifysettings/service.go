package notifysettings

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/auth"
)

// Service is the admin HTTP surface for the switches.
type Service struct {
	settings  *Settings
	canManage func(ctx context.Context, email string) (bool, error)
	// slackConfigured reports whether a bot token exists at all. Without one,
	// turning a switch on changes nothing, and the page has to say so rather
	// than leave an admin wondering why their DMs never arrive.
	slackConfigured bool
}

func NewService(s *Settings, canManage func(ctx context.Context, email string) (bool, error), slackConfigured bool) *Service {
	return &Service{settings: s, canManage: canManage, slackConfigured: slackConfigured}
}

func (s *Service) Mount(r chi.Router) {
	// A person's own choices: no permission needed, because the settings are
	// about messages sent to them.
	r.Get("/api/notifications", s.getMine)
	r.Put("/api/notifications", s.putMine)
	// The deployment switch, which stops everything for everyone.
	r.Get("/api/admin/notifications", s.getMaster)
	r.Put("/api/admin/notifications", s.putMaster)
}

type categoryView struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

type settingsView struct {
	Categories []categoryView `json:"categories"`
	// SlackConfigured is false when no bot token is set, in which case every
	// switch is inert.
	SlackConfigured bool `json:"slack_configured"`
	// DeploymentEnabled is the master switch. When false nothing is sent to
	// anyone, so a person's own page has to say why their choices do nothing.
	DeploymentEnabled bool `json:"deployment_enabled"`
	// CanManageDeployment reports whether the caller may change the master.
	CanManageDeployment bool `json:"can_manage_deployment"`
}

type putRequest struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

// getMine answers with the caller's own choices.
func (s *Service) getMine(w http.ResponseWriter, r *http.Request) {
	email := auth.EmailFromContext(r.Context())
	if email == "" {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	s.writeMine(w, r, email)
}

// putMine sets one of the caller's own choices. A caller can only ever write
// their own row: the identity comes from the request context, never the body.
func (s *Service) putMine(w http.ResponseWriter, r *http.Request) {
	email := auth.EmailFromContext(r.Context())
	if email == "" {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var body putRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	c, ok := personalCategory(body.Key)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown setting: "+body.Key)
		return
	}
	if err := s.settings.SetFor(r.Context(), email, c, body.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to write setting")
		return
	}
	s.writeMine(w, r, email)
}

func (s *Service) writeMine(w http.ResponseWriter, r *http.Request, email string) {
	mine, err := s.settings.CurrentFor(r.Context(), email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to read settings")
		return
	}
	master, err := s.settings.MasterEnabled(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to read settings")
		return
	}
	canManage, err := s.canManage(r.Context(), email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "permission check failed")
		return
	}
	out := settingsView{
		Categories:          make([]categoryView, 0, len(Personal)),
		SlackConfigured:     s.slackConfigured,
		DeploymentEnabled:   master,
		CanManageDeployment: canManage,
	}
	for _, c := range Personal {
		out.Categories = append(out.Categories, categoryView{
			Key: string(c), Label: c.Label(), Description: c.Description(), Enabled: mine[c],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// getMaster and putMaster carry the deployment switch. Admin only, answering
// 404 rather than 403 so the route is not discoverable, as the rest of the
// admin surface does.
func (s *Service) getMaster(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	s.writeMaster(w, r)
}

func (s *Service) putMaster(w http.ResponseWriter, r *http.Request) {
	email, ok := s.authorizeAdmin(w, r)
	if !ok {
		return
	}
	var body putRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := s.settings.SetMaster(r.Context(), body.Enabled, email); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to write setting")
		return
	}
	s.writeMaster(w, r)
}

func (s *Service) writeMaster(w http.ResponseWriter, r *http.Request) {
	master, err := s.settings.MasterEnabled(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to read settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key": string(Master), "label": Master.Label(),
		"enabled": master, "slack_configured": s.slackConfigured,
	})
}

func (s *Service) authorizeAdmin(w http.ResponseWriter, r *http.Request) (string, bool) {
	email := auth.EmailFromContext(r.Context())
	if email == "" {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return "", false
	}
	ok, err := s.canManage(r.Context(), email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "permission check failed")
		return "", false
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return "", false
	}
	return email, true
}

// personalCategory resolves a key a person is allowed to set for themselves.
func personalCategory(key string) (Category, bool) {
	for _, c := range Personal {
		if string(c) == key {
			return c, true
		}
	}
	return "", false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}
