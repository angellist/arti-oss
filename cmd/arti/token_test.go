package main

import (
	"testing"
)

func TestLoadTokenPrefersEnv(t *testing.T) {
	t.Setenv("ARTI_TOKEN", "env-bearer-xyz")
	tok, err := loadToken()
	if err != nil || tok.AccessToken != "env-bearer-xyz" {
		t.Fatalf("env token not honored: %q err=%v", tok.AccessToken, err)
	}
}
