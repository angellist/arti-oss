package main

import (
	"strings"

	"github.com/alecthomas/kong"
)

func main() {
	cli := &CLI{}
	ctx := kong.Parse(cli,
		kong.Name("arti"),
		kong.Description("artifact CLI"),
		kong.UsageOnError(),
		kong.Vars{"default_base_url": defaultBaseURL},
	)
	err := ctx.Run(cli)
	// Best-effort, throttled "you may be out of date" nudge (see selfupdate.go).
	maybeNotifyUpdate(firstWord(ctx.Command()))
	ctx.FatalIfErrorf(err)
}

// firstWord returns the leading command token of kong's command path
// (e.g. "add <file>" → "add"), or "" if empty.
func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

type CLI struct {
	BaseURL string `env:"ARTI_BASE_URL" default:"${default_base_url}" help:"server base URL"`

	Login    LoginCmd    `cmd:"" help:"OAuth login (PKCE pair flow)"`
	Logout   LogoutCmd   `cmd:"" help:"clear local token"`
	Whoami   WhoamiCmd   `cmd:"" help:"print logged-in email"`
	Add      AddCmd      `cmd:"" help:"create or version an artifact"`
	Append   AppendCmd   `cmd:"" help:"append text to a slug (atomic; auto-creates v1 if absent)"`
	Edit     EditCmd     `cmd:"" help:"edit metadata in place (title/description/labels/scopes/comments; no new version)"`
	Access   AccessCmd   `cmd:"" help:"show or edit access control (applies to all versions of the document)"`
	Get      GetCmd      `cmd:"" help:"fetch by UUID or slug (UUID/path/in/zip for one file)"`
	Rm       RmCmd       `cmd:"" help:"archive by UUID or slug"`
	Ls       LsCmd       `cmd:"" help:"list artifacts (or package entries when given UUID)"`
	Versions VersionsCmd `cmd:"" help:"list versions for a slug"`
	URL      URLCmd      `cmd:"" name:"url" help:"print canonical URL"`
	Search   SearchCmd   `cmd:"" help:"substring search"`
	Version  VersionCmd  `cmd:"" help:"print version + check for a newer main build"`
	Update   UpdateCmd   `cmd:"" help:"update arti to the latest main build (go install)"`
	Token    TokenCmd    `cmd:"" hidden:"" help:"print bearer token (debug)"`
}
