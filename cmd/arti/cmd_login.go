package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// LoginCmd runs the PKCE-pair login flow:
//
//  1. CLI generates a one-time `cli_code` (random 24 bytes, base64url).
//  2. CLI opens the browser to
//     `<base>/auth/login?cli_code=<code>`.
//  3. User authenticates via Google and explicitly confirms the displayed
//     code. The server then associates it with the verified email.
//  4. CLI polls `POST <base>/auth/cli/exchange {code}` every 2 s.
//  5. On hit, server returns access + refresh tokens; CLI stores them.
type LoginCmd struct {
	Email string `help:"skip browser, use test-mode (requires server ARTI_TEST_MODE=true)"`
}

func (l *LoginCmd) Run(cli *CLI) error {
	base := cli.BaseURL

	// Shortcut for local dev: hit /auth/test directly.
	if l.Email != "" {
		c := newPublicClient(base)
		body := map[string]string{"email": l.Email}
		var resp Token
		if err := c.DoJSON("POST", "/auth/test", body, &resp); err != nil {
			return err
		}
		// test-mode endpoint returns access_token + email, no refresh.
		resp.ExpiresAt = time.Now().Add(24 * time.Hour).Unix()
		if err := saveToken(resp); err != nil {
			return err
		}
		fmt.Println("logged in as", resp.Email)
		return nil
	}

	// Real PKCE-pair flow.
	codeBytes := make([]byte, 24)
	_, _ = rand.Read(codeBytes)
	cliCode := base64.RawURLEncoding.EncodeToString(codeBytes)

	authURL := fmt.Sprintf("%s/auth/login?cli_code=%s&return_to=/", base, cliCode)
	fmt.Fprintln(stderr(), "Opening browser to:", authURL)
	if err := openBrowser(authURL); err != nil {
		fmt.Fprintln(stderr(), "(failed to open browser; copy the URL manually)")
	}
	fmt.Fprintln(stderr(), "Waiting for completion (up to 5 minutes)…")

	c := newPublicClient(base)
	deadline := time.Now().Add(5 * time.Minute)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			var tok Token
			err := c.DoJSON("POST", "/auth/cli/exchange", map[string]string{"code": cliCode}, &tok)
			if err == nil && tok.AccessToken != "" {
				if err := saveToken(tok); err != nil {
					return err
				}
				fmt.Fprintln(stderr(), "Signed in as", tok.Email)
				return nil
			}
			// 404 → still waiting; keep polling. Other errors → return.
			var he HTTPError
			if errors.As(err, &he) && he.Status == http.StatusNotFound {
				if time.Now().After(deadline) {
					return errors.New("login timed out")
				}
				continue
			}
			if err != nil {
				return err
			}
		}
	}
}

func openBrowser(url string) error {
	cmd := ""
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "linux":
		cmd = "xdg-open"
	case "windows":
		cmd = "rundll32"
		return exec.Command(cmd, "url.dll,FileProtocolHandler", url).Start()
	}
	if cmd == "" {
		return errors.New("unknown platform")
	}
	return exec.Command(cmd, url).Start()
}

// LogoutCmd / WhoamiCmd.
type LogoutCmd struct{}

func (LogoutCmd) Run(*CLI) error {
	return clearTokenFile()
}

type WhoamiCmd struct{}

func (WhoamiCmd) Run(*CLI) error {
	t, err := loadToken()
	if err != nil {
		return err
	}
	email := t.Email
	if email == "" {
		// fall back to decoding email from access token payload
		email = peekEmailFromJWT(t.AccessToken)
	}
	if email == "" {
		return errors.New("no email in token")
	}
	fmt.Println(email)
	return nil
}

// peekEmailFromJWT decodes the payload (without verifying signature) to surface
// the email claim. We never trust this for auth, but for `arti whoami` it's fine.
func peekEmailFromJWT(tok string) string {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return ""
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		return ""
	}
	return c.Email
}

// TokenCmd is hidden — prints the cached bearer for debugging.
type TokenCmd struct{}

func (TokenCmd) Run(*CLI) error {
	t, err := loadToken()
	if err != nil {
		return err
	}
	fmt.Println(t.AccessToken)
	return nil
}
