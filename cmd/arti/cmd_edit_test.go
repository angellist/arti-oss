package main

import (
	"encoding/json"
	"testing"
)

// editMeta is what the fake server returns for both the slug resolution and
// the PATCH reply — enough for `arti edit` to address the artifact and print
// the after-state.
var editMeta = map[string]any{
	"artifact_id":      "11111111-2222-3333-4444-555555555555",
	"named_slug":       "bare-doc",
	"version":          2,
	"title":            "Bare doc",
	"labels":           []string{"report"},
	"scopes":           []string{},
	"comments_enabled": true,
}

// The headline gap this command closes: title, description, labels and scopes
// are editable in place, and each field arrives in the PATCH exactly once.
func TestEditCmdPatchesMetadata(t *testing.T) {
	srv, patched := fakeArti(t, editMeta)
	title, desc := "Q3 treasury review", "what changed and why"
	cmd := &EditCmd{
		Ident:       "bare-doc",
		Title:       &title,
		Description: &desc,
		Label:       []string{"report", "treasury"},
		Scope:       []string{"a:bt-auto-route"},
	}
	if err := cmd.Run(&CLI{BaseURL: srv.URL}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	body := patched()
	if body == nil {
		t.Fatal("no PATCH reached the server")
	}
	var gotTitle, gotDesc string
	if err := json.Unmarshal(body["title"], &gotTitle); err != nil || gotTitle != title {
		t.Fatalf("title = %q (err=%v), want %q", gotTitle, err, title)
	}
	if err := json.Unmarshal(body["description"], &gotDesc); err != nil || gotDesc != desc {
		t.Fatalf("description = %q (err=%v), want %q", gotDesc, err, desc)
	}
	var labels, scopes []string
	if err := json.Unmarshal(body["labels"], &labels); err != nil || len(labels) != 2 {
		t.Fatalf("labels = %v (err=%v), want 2", labels, err)
	}
	if err := json.Unmarshal(body["scopes"], &scopes); err != nil || len(scopes) != 1 {
		t.Fatalf("scopes = %v (err=%v), want 1", scopes, err)
	}
	if _, ok := body["comments_enabled"]; ok {
		t.Fatal("comments_enabled must be absent when --comments wasn't passed")
	}
}

// A field the caller didn't set must be ABSENT from the PATCH — arti reads
// absent as "leave untouched", so a title-only edit must not blank a curated
// label set or a description.
func TestEditCmdOmitsUntouchedFields(t *testing.T) {
	srv, patched := fakeArti(t, editMeta)
	title := "Renamed"
	if err := (&EditCmd{Ident: "bare-doc", Title: &title}).Run(&CLI{BaseURL: srv.URL}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	body := patched()
	for _, k := range []string{"description", "labels", "scopes", "comments_enabled"} {
		if _, ok := body[k]; ok {
			t.Fatalf("%s must be absent from a title-only PATCH, got %s", k, body[k])
		}
	}
}

// `--description ”` clears the description; that is the distinction a plain
// string flag can't make, and it is why the field is a pointer.
func TestEditCmdClearsDescription(t *testing.T) {
	srv, patched := fakeArti(t, editMeta)
	empty := ""
	if err := (&EditCmd{Ident: "bare-doc", Description: &empty}).Run(&CLI{BaseURL: srv.URL}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	raw, ok := patched()["description"]
	if !ok {
		t.Fatal("description must be sent (explicit clear), not omitted")
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil || got != "" {
		t.Fatalf("description = %q (err=%v), want an explicit empty string", got, err)
	}
}

// --clear-labels / --clear-scopes send explicit empty arrays — the shape a
// repeatable flag can't express, mirroring `arti access --private`.
func TestEditCmdClearSets(t *testing.T) {
	srv, patched := fakeArti(t, editMeta)
	cmd := &EditCmd{Ident: "bare-doc", ClearLabels: true, ClearScopes: true}
	if err := cmd.Run(&CLI{BaseURL: srv.URL}); err != nil {
		t.Fatalf("edit --clear-labels --clear-scopes: %v", err)
	}
	body := patched()
	for _, k := range []string{"labels", "scopes"} {
		var got []string
		if err := json.Unmarshal(body[k], &got); err != nil || got == nil || len(got) != 0 {
			t.Fatalf("%s = %v (err=%v), want explicit []", k, got, err)
		}
	}
}

// --no-comments sends the per-document switch as false (and --comments as
// true); a bool flag can't be omitted, so this too rides on a pointer.
func TestEditCmdCommentsSwitch(t *testing.T) {
	for _, tc := range []struct{ want bool }{{true}, {false}} {
		srv, patched := fakeArti(t, editMeta)
		v := tc.want
		if err := (&EditCmd{Ident: "bare-doc", Comments: &v}).Run(&CLI{BaseURL: srv.URL}); err != nil {
			t.Fatalf("edit --comments=%v: %v", tc.want, err)
		}
		var got bool
		if err := json.Unmarshal(patched()["comments_enabled"], &got); err != nil || got != tc.want {
			t.Fatalf("comments_enabled = %v (err=%v), want %v", got, err, tc.want)
		}
	}
}

// With no flags the command is read-only: it prints the editable fields and
// writes nothing, the same contract as flag-less `arti access`.
func TestEditCmdNoFlagsIsReadOnly(t *testing.T) {
	srv, patched := fakeArti(t, editMeta)
	if err := (&EditCmd{Ident: "bare-doc"}).Run(&CLI{BaseURL: srv.URL}); err != nil {
		t.Fatalf("edit (show): %v", err)
	}
	if patched() != nil {
		t.Fatal("flag-less `arti edit` must not write")
	}
}

// A UUID addresses that exact version directly; resolution must read the
// /meta route, never the bare /api/artifacts/{id} route, which streams the
// artifact's CONTENT and would fail to decode as JSON.
func TestEditCmdByUUID(t *testing.T) {
	srv, patched := fakeArti(t, editMeta)
	title := "Renamed"
	cmd := &EditCmd{Ident: "11111111-2222-3333-4444-555555555555", Title: &title}
	if err := cmd.Run(&CLI{BaseURL: srv.URL}); err != nil {
		t.Fatalf("edit by uuid: %v", err)
	}
	if patched() == nil {
		t.Fatal("no PATCH reached the server")
	}
}

func TestEditCmdRejectsBadFlagCombos(t *testing.T) {
	srv, _ := fakeArti(t, editMeta)
	cli := &CLI{BaseURL: srv.URL}
	empty := ""
	if err := (&EditCmd{Ident: "bare-doc", Title: &empty}).Run(cli); err == nil {
		t.Fatal("--title '' must error (a title cannot be cleared)")
	}
	if err := (&EditCmd{Ident: "bare-doc", ClearLabels: true, Label: []string{"x"}}).Run(cli); err == nil {
		t.Fatal("--clear-labels with --label must error")
	}
	if err := (&EditCmd{Ident: "bare-doc", ClearScopes: true, Scope: []string{"x"}}).Run(cli); err == nil {
		t.Fatal("--clear-scopes with --scope must error")
	}
}
