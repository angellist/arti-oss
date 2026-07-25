package main

import "fmt"

// VersionCmd prints the build version/commit and checks origin/main for a newer
// commit (always checks, ignoring the throttle cache — the user asked).
type VersionCmd struct{}

func (VersionCmd) Run(*CLI) error {
	fmt.Printf("arti %s\n", displayVersion())
	if c := builtCommit(); c != "" {
		fmt.Printf("commit %s\n", c)
	}
	fmt.Fprintln(stderr(), "checking for updates…")
	latest, err := latestMainCommit()
	if err != nil {
		fmt.Fprintf(stderr(), "! could not check for updates: %v\n", err)
		return nil
	}
	writeCache(latest)
	if stale(builtCommit(), latest) {
		printUpdateNotice()
	} else if builtCommit() == "" {
		fmt.Fprintln(stderr(), "(unstamped build — can't compare to main; build via `make build-cli`)")
	} else {
		fmt.Fprintln(stderr(), "arti is up to date with main.")
	}
	return nil
}
