// Package dictate proxies one recorded clip from the comment box's mic to the
// provider's transcription endpoint and returns {text}, so the provider key
// never reaches the browser.
package dictate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/angellist/arti-oss/internal/auth"
)

// MaxBytes is the provider's own upload limit.
const MaxBytes = 25 << 20

const (
	model    = "gpt-4o-transcribe"
	endpoint = "https://api.openai.com/v1/audio/transcriptions"
)

// Handler serves POST /api/dictate. An empty key answers 501, which is how a
// deployment without a key (OSS, local) keeps the endpoint dead. language
// empty lets the model detect the language per clip.
type Handler struct {
	Key      string
	Language string
	Endpoint string // tests override; empty = the provider
	Client   *http.Client
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Key == "" {
		writeErr(w, http.StatusNotImplemented, "not_configured", "dictation is not configured")
		return
	}
	// Each call spends the provider key, so only a person at a browser gets it;
	// a CLI, MCP or service-principal bearer authenticates as someone but is not.
	if cred := auth.CredentialFromContext(r.Context()); !cred.FromBrowser() {
		msg := "dictation is only available in the arti web UI"
		if cred.PredatesSessionMarker() {
			msg = "this browser session predates dictation; sign out and sign in again"
		}
		writeErr(w, http.StatusForbidden, "browser_only", msg)
		return
	}
	ct := r.Header.Get("Content-Type")
	mt, _, _ := mime.ParseMediaType(ct)
	if !strings.HasPrefix(mt, "audio/") {
		writeErr(w, http.StatusUnsupportedMediaType, "bad_media_type", "body must be audio/*")
		return
	}
	audio, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBytes))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "recording exceeds 25 MB")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_body", "could not read recording")
		return
	}
	if len(audio) == 0 {
		writeErr(w, http.StatusBadRequest, "empty", "empty recording")
		return
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("model", model)
	if h.Language != "" {
		_ = mw.WriteField("language", h.Language)
	}
	fw, _ := mw.CreateFormFile("file", "clip."+AudioExt(mt))
	_, _ = fw.Write(audio)
	_ = mw.Close()

	url := h.Endpoint
	if url == "" {
		url = endpoint
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, url, &body)
	req.Header.Set("Authorization", "Bearer "+h.Key)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "upstream", "transcription request failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		// The provider's body stays in the log: an invalid-key reply echoes part of the key.
		slog.WarnContext(r.Context(), "dictate: provider error", "status", resp.StatusCode, "body", string(detail))
		// 502, never the provider's own status: a provider 401 means OUR key is
		// bad, and passing it through would read to the browser as its session.
		writeErr(w, http.StatusBadGateway, "upstream", fmt.Sprintf("transcription failed (provider status %d)", resp.StatusCode))
		return
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		writeErr(w, http.StatusBadGateway, "upstream", "unreadable transcription response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"text": strings.TrimSpace(out.Text)})
}

// AudioExt picks the upload's file extension from the recorder's MIME type.
// The provider chooses its decoder by extension, so Safari's mp4 sent as .webm
// fails the whole request.
func AudioExt(mt string) string {
	m := strings.ToLower(mt)
	switch {
	case strings.Contains(m, "mp4"), strings.Contains(m, "m4a"), strings.Contains(m, "aac"):
		return "mp4"
	case strings.Contains(m, "ogg"):
		return "ogg"
	case strings.Contains(m, "wav"):
		return "wav"
	case strings.Contains(m, "mpeg"), strings.Contains(m, "mp3"):
		return "mp3"
	}
	return "webm"
}

func writeErr(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "detail": detail})
}
