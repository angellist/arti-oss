package auth

import (
	"context"
	"log"
	"net/http"
	"strings"
)

// AccessRequester records and reads requests for an account. Status is ""
// when the email has never asked, else PENDING, APPROVED or DENIED.
type AccessRequester interface {
	AccessRequestStatus(ctx context.Context, email string) (string, error)
	RequestAccess(ctx context.Context, email, name string) error
}

const maxRequestNameLen = 200

// serveAccessRequest is the login page for someone whose domain is accepted
// but who has no account yet. The email is the proxy-verified identity; the
// form supplies only a display name.
func serveAccessRequest(w http.ResponseWriter, r *http.Request, email string, requests AccessRequester) {
	status, err := requests.AccessRequestStatus(r.Context(), email)
	if err != nil {
		http.Error(w, "could not read access request", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodPost && status == "" {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "cross-site access request rejected", http.StatusForbidden)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" || len(name) > maxRequestNameLen {
			accessRequestForm(email, r.URL.RequestURI(), "Enter your name (up to 200 characters).").render(w)
			return
		}
		if err := requests.RequestAccess(r.Context(), email, name); err != nil {
			log.Printf("access request for %s failed: %v", email, err)
			http.Error(w, "could not record access request", http.StatusInternalServerError)
			return
		}
		status = "PENDING"
	}
	switch status {
	case "":
		accessRequestForm(email, r.URL.RequestURI(), "").render(w)
	case "DENIED":
		brandPage{
			Title:   "arti — request declined",
			Heading: "Request declined",
			Intro:   "Your request for an arti account was declined. Ask in #ai-support if you think this is a mistake.",
			Email:   email,
		}.render(w)
	default:
		brandPage{
			Title:   "arti — request sent",
			Heading: "Request sent",
			Intro:   "An admin will approve it. Reload this page after you hear back.",
			Email:   email,
			Done:    true,
		}.render(w)
	}
}

func accessRequestForm(email, action, note string) brandPage {
	return brandPage{
		Title:   "arti — request access",
		Heading: "Request access to arti",
		Intro:   "You don't have an arti account yet. Ask for one and an admin will review it.",
		Email:   email,
		Note:    note,
		Action: &brandAction{
			Method: "POST",
			URL:    action,
			Input:  &brandInput{Name: "name", Label: "Your name", MaxLen: maxRequestNameLen},
			Submit: "Request access",
		},
	}
}
