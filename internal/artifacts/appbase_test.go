package artifacts

import (
	"strings"
	"testing"
)

// An APP's entry HTML is served at a /app/… document URL, so relative asset
// refs must be rebased to the package-files API via an injected <base>.
func TestInjectBaseHref(t *testing.T) {
	href := "/api/artifacts/ID/files/"

	out := string(injectBaseHref([]byte(`<html><head><title>t</title></head><body><script src="a.js"></script></body></html>`), "text/html", href))
	if !strings.Contains(out, `<base href="`+href+`">`) {
		t.Fatalf("base not injected: %s", out)
	}
	if strings.Index(out, "<base") > strings.Index(out, "<title>") {
		t.Fatal("base should precede other <head> content")
	}

	// attributed opening head (<head lang="en">) — base must land *inside* the
	// head, right after the tag, not get prepended before the doctype.
	attr := string(injectBaseHref([]byte(`<!doctype html><html><head lang="en"><title>t</title></head><body></body></html>`), "text/html", href))
	if !strings.Contains(attr, `<head lang="en"><base href="`+href+`">`) {
		t.Fatalf("base not injected just after attributed <head>: %s", attr)
	}
	if strings.HasPrefix(attr, "<base") {
		t.Fatal("base must not be prepended before the doctype when a head exists")
	}

	// <header> must not be mistaken for <head>: with no real head, base is
	// prepended, never spliced into the <header> element.
	noHead := string(injectBaseHref([]byte(`<html><body><header>hi</header></body></html>`), "text/html", href))
	if !strings.HasPrefix(noHead, `<base href="`+href+`">`) {
		t.Fatalf("with no <head>, base should be prepended (not inside <header>): %s", noHead)
	}

	// respects an author-provided <base>
	withBase := []byte(`<head><base href="/custom/"></head>`)
	if string(injectBaseHref(withBase, "text/html", href)) != string(withBase) {
		t.Fatal("must not inject when a <base> already exists")
	}

	// non-HTML untouched
	if string(injectBaseHref([]byte("plain"), "text/plain", href)) != "plain" {
		t.Fatal("non-HTML must be untouched")
	}
}
