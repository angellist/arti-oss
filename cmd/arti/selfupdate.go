package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// version and commit are injected at build time via
//
//	-ldflags "-X main.version=… -X main.commit=…"
//
// (see the Makefile `build-cli` target). For a `go install …@latest` build the
// Makefile isn't involved, so they stay empty/"dev" and we recover the commit
// from the embedded VCS build info instead. arti has no release tags, so the
// "latest" we compare against is origin/main's HEAD commit.
var (
	version = "dev"
	commit  = ""
)

const (
	artiRepoSSH     = "git@github.com:angellist/arti-oss.git"
	artiModuleRoot  = "github.com/angellist/arti-oss"
	artiModule      = artiModuleRoot + "/cmd/arti"
	updateCheckTTL  = 24 * time.Hour
	disableCheckEnv = "ARTI_DISABLE_UPDATE_CHECK"
)

// displayVersion is the human-facing version string: ldflags value, else the
// module version embedded by `go install`, else "dev".
func displayVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// builtCommit is the commit this binary was built from: ldflags value, else the
// VCS revision embedded by `go install`. Empty when unknown — in which case the
// staleness check is skipped rather than nagging an un-stamped/dev build.
func builtCommit() string {
	if commit != "" {
		return strings.TrimSuffix(commit, "-dirty")
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return ""
}

// latestMainCommit returns origin/main's HEAD SHA via `git ls-remote` — no clone
// or fetch, just one network round-trip.
func latestMainCommit() (string, error) {
	// Bounded so the post-command background check can never hang the CLI on a
	// slow/unreachable network (it's also throttled to once/24h and TTY-gated).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "ls-remote", artiRepoSSH, "refs/heads/main").Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote %s: %w", artiRepoSSH, err)
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return "", fmt.Errorf("no refs/heads/main returned")
	}
	return f[0], nil
}

// stale reports whether the build differs from latest main. builtSHA may be
// abbreviated (git describe --always), so a prefix match counts as up-to-date.
// Unknown built or latest SHA ⇒ not stale (never nag on missing info).
func stale(builtSHA, latestSHA string) bool {
	if builtSHA == "" || latestSHA == "" {
		return false
	}
	return !strings.HasPrefix(latestSHA, builtSHA)
}

func printUpdateNotice() {
	// "differs", not "out of date": ls-remote gives only main's HEAD (no ancestry),
	// so we can't distinguish behind from ahead — don't imply the build is stale.
	fmt.Fprintf(stderr(), "\n! arti differs from origin/main — `arti update` installs the latest (or `go install %s@latest`)\n", artiModule)
}

// ---- throttle cache (~/.cache/arti/update_cache.json, 24h TTL) -------------

type updateCache struct {
	LastCheck    time.Time `json:"last_check"`
	LatestCommit string    `json:"latest_commit"`
}

func cachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(dir, "arti")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(d, "update_cache.json"), nil
}

func readCache() updateCache {
	var c updateCache
	p, err := cachePath()
	if err != nil {
		return c
	}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func writeCache(latest string) {
	p, err := cachePath()
	if err != nil {
		return
	}
	b, _ := json.Marshal(updateCache{LastCheck: time.Now(), LatestCommit: latest})
	_ = os.WriteFile(p, b, 0o644)
}

func checkDue() bool { return time.Since(readCache().LastCheck) >= updateCheckTTL }

func isInteractive() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// maybeNotifyUpdate is the best-effort, throttled check wired in after every
// command. No-op when non-interactive, disabled via env, recently checked, run
// under the version/update commands themselves, or when the build commit is
// unknown. Network/IO failures are swallowed — this must never break a command.
func maybeNotifyUpdate(cmdName string) {
	switch cmdName {
	case "version", "update":
		return
	}
	if strings.HasSuffix(version, "-dirty") {
		return // local build with uncommitted changes (a developer) — don't nag
	}
	if os.Getenv(disableCheckEnv) != "" || !isInteractive() || !checkDue() {
		return
	}
	latest, err := latestMainCommit()
	if err != nil {
		return
	}
	writeCache(latest)
	if stale(builtCommit(), latest) {
		printUpdateNotice()
	}
}
