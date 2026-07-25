package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Token is what we cache locally. ExpiresAt is unix seconds.
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
	Email        string `json:"email,omitempty"`
}

func (t Token) ExpiringSoon() bool {
	if t.ExpiresAt == 0 {
		return false
	}
	return time.Now().Unix() > t.ExpiresAt-60
}

func tokenPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "arti", "token.json"), nil
}

func loadToken() (Token, error) {
	// An injected bearer (e.g. a couch sandbox's device-flow upload token)
	// takes precedence over the on-disk login — no `arti login` needed.
	if env := strings.TrimSpace(os.Getenv("ARTI_TOKEN")); env != "" {
		return Token{AccessToken: env}, nil
	}
	p, err := tokenPath()
	if err != nil {
		return Token{}, err
	}
	b, err := os.ReadFile(p) // #nosec G304 — user config dir, intentional
	if err != nil {
		if os.IsNotExist(err) {
			return Token{}, errNotLoggedIn
		}
		return Token{}, err
	}
	var t Token
	if err := json.Unmarshal(b, &t); err != nil {
		return Token{}, fmt.Errorf("parse token: %w", err)
	}
	if t.AccessToken == "" {
		return Token{}, errNotLoggedIn
	}
	return t, nil
}

func saveToken(t Token) error {
	p, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

func clearTokenFile() error {
	p, err := tokenPath()
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

var errNotLoggedIn = errors.New("not logged in — run `arti login`")
