package pkgzip_test

import (
	"archive/zip"
	"bytes"
	"reflect"
	"sort"
	"testing"

	"github.com/angellist/arti-oss/internal/pkgzip"
)

// entryPaths returns the sorted file paths in a package zip (junk + dir
// entries excluded, via BuildManifest).
func entryPaths(t *testing.T, zb []byte) []string {
	t.Helper()
	m, err := pkgzip.BuildManifest(zb)
	if err != nil {
		t.Fatal(err)
	}
	ps := make([]string, 0, len(m.Entries))
	for _, e := range m.Entries {
		ps = append(ps, e.Path)
	}
	sort.Strings(ps)
	return ps
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A package wrapped in one top-level dir ("zip -r app.zip app/") must be
// flattened so detection + the file rail see root paths; layouts that aren't
// a single wrapper are left byte-for-byte alone.
func TestFlattenSingleRoot(t *testing.T) {
	// Single wrapping dir → stripped flat.
	wrapped := makeZip(t, map[string]string{
		"app/index.html":    "<h1>hi</h1>",
		"app/arti-app.json": `{"entry":"index.html"}`,
		"app/css/main.css":  "body{}",
	})
	out, err := pkgzip.FlattenSingleRoot(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := entryPaths(t, out), []string{"arti-app.json", "css/main.css", "index.html"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("wrapped: got %v, want %v", got, want)
	}
	// The APP machinery reads arti-app.json at the root — it must resolve now.
	if _, _, e := pkgzip.ReadEntry(out, "arti-app.json"); e != nil {
		t.Fatalf("arti-app.json should be at root after flatten: %v", e)
	}

	// Already flat → returned unchanged.
	flat := makeZip(t, map[string]string{"index.html": "x", "style.css": "y"})
	if got, _ := pkgzip.FlattenSingleRoot(flat); !bytes.Equal(got, flat) {
		t.Error("already-flat zip should be returned unchanged")
	}

	// Two top-level dirs → unchanged (src/ + dist/ is intentional).
	multi := makeZip(t, map[string]string{"src/a.js": "1", "dist/b.js": "2"})
	if got, _ := pkgzip.FlattenSingleRoot(multi); !bytes.Equal(got, multi) {
		t.Error("multi-root zip should be unchanged")
	}

	// A file at the root alongside a dir → unchanged.
	mixed := makeZip(t, map[string]string{"readme.md": "r", "app/index.html": "h"})
	if got, _ := pkgzip.FlattenSingleRoot(mixed); !bytes.Equal(got, mixed) {
		t.Error("root-file-plus-dir zip should be unchanged")
	}

	// macOS junk must not count as a second top-level dir; it's dropped.
	junky := makeZip(t, map[string]string{
		"app/index.html":            "<h1>hi</h1>",
		"__MACOSX/app/._index.html": "junk",
	})
	jout, err := pkgzip.FlattenSingleRoot(junky)
	if err != nil {
		t.Fatal(err)
	}
	if got := entryPaths(t, jout); !reflect.DeepEqual(got, []string{"index.html"}) {
		t.Fatalf("junky: got %v, want [index.html]", got)
	}
}

func TestBuildManifest(t *testing.T) {
	z := makeZip(t, map[string]string{
		"skill.md": "# hi",
		"run.sh":   "echo ok",
	})
	m, err := pkgzip.BuildManifest(z)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 2 {
		t.Fatalf("entries=%d", len(m.Entries))
	}
	if m.EntryPoint != "skill.md" {
		t.Fatalf("entry_point=%q", m.EntryPoint)
	}
}

// A static-site package should auto-open its landing page wherever it lives —
// at the root, under a build dir, or nested — without the uploader having to
// set entry_point. Error/partial pages must never be chosen.
func TestBuildManifest_HTMLLanding(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"root index wins", map[string]string{
			"index.html": "x", "about.html": "x", "404.html": "x",
		}, "index.html"},
		{"nested index when no root", map[string]string{
			"out/index.html": "x", "out/about.html": "x",
		}, "out/index.html"},
		{"shallower index beats deeper", map[string]string{
			"index.html": "x", "out/index.html": "x",
		}, "index.html"},
		{"named index over plain page", map[string]string{
			"page.html": "x", "wallet-index.html": "x",
		}, "wallet-index.html"},
		{"skips error/partial pages", map[string]string{
			"404.html": "x", "_not-found.html": "x", "home.html": "x",
		}, "home.html"},
		{"html beats a source README", map[string]string{
			"README.md": "# src", "dist/index.html": "x",
		}, "dist/index.html"},
		{"falls back to readme when no html", map[string]string{
			"README.md": "# hi", "src/app.tsx": "x",
		}, "README.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := pkgzip.BuildManifest(makeZip(t, tc.files))
			if err != nil {
				t.Fatal(err)
			}
			if m.EntryPoint != tc.want {
				t.Fatalf("entry_point = %q, want %q", m.EntryPoint, tc.want)
			}
		})
	}
}

func TestReadEntry(t *testing.T) {
	z := makeZip(t, map[string]string{"skill.md": "# hi"})
	body, ct, err := pkgzip.ReadEntry(z, "skill.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "# hi" {
		t.Fatalf("body=%q", string(body))
	}
	if ct != "text/markdown" {
		t.Fatalf("ct=%q", ct)
	}
}

func TestReadEntry_Missing(t *testing.T) {
	z := makeZip(t, map[string]string{"a.txt": "x"})
	if _, _, err := pkgzip.ReadEntry(z, "missing.md"); err == nil {
		t.Fatal("expected error")
	}
}

// Static-site exports (Next.js `next export`, Vercel/Netlify hosting)
// link to clean, extensionless URLs — `href="/wallet-ia-v1"` — while
// writing the actual file as `wallet-ia-v1.html`. Without clean-URL
// resolution, every internal nav link in such a package 500s with
// "entry not found". ReadEntry must map the request the way a static
// host does: exact → <path>.html → <path>/index.html.
func TestReadEntry_CleanURL_HTMLExtension(t *testing.T) {
	z := makeZip(t, map[string]string{"wallet-ia-v1.html": "<h1>v1</h1>"})
	body, ct, err := pkgzip.ReadEntry(z, "wallet-ia-v1")
	if err != nil {
		t.Fatalf("clean URL should resolve to .html file: %v", err)
	}
	if string(body) != "<h1>v1</h1>" {
		t.Fatalf("body=%q", string(body))
	}
	if ct != "text/html" {
		t.Fatalf("ct=%q", ct)
	}
}

// A real file must always win over a synthesized fallback, so packages
// that legitimately contain an extensionless file keep serving it.
func TestReadEntry_ExactMatchWinsOverFallback(t *testing.T) {
	z := makeZip(t, map[string]string{
		"docs":      "exact",
		"docs.html": "fallback",
	})
	body, _, err := pkgzip.ReadEntry(z, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "exact" {
		t.Fatalf("expected exact file to win, got %q", string(body))
	}
}

// A clean URL pointing at a directory resolves to its index.html, with
// or without a trailing slash — matching static-host directory serving.
func TestReadEntry_DirectoryIndex(t *testing.T) {
	z := makeZip(t, map[string]string{"guide/index.html": "<h1>guide</h1>"})
	for _, req := range []string{"guide", "guide/"} {
		body, ct, err := pkgzip.ReadEntry(z, req)
		if err != nil {
			t.Fatalf("%q should resolve to guide/index.html: %v", req, err)
		}
		if string(body) != "<h1>guide</h1>" || ct != "text/html" {
			t.Fatalf("%q: body=%q ct=%q", req, string(body), ct)
		}
	}
}

// An empty path (request to `/files` or `/files/`) serves the root index.
func TestReadEntry_RootIndex(t *testing.T) {
	z := makeZip(t, map[string]string{"index.html": "<h1>root</h1>"})
	for _, req := range []string{"", "/"} {
		if _, _, err := pkgzip.ReadEntry(z, req); err != nil {
			t.Fatalf("%q should resolve to index.html: %v", req, err)
		}
	}
}
