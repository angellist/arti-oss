package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// UpdateCmd updates arti to the latest origin/main build via `go install`.
// arti publishes no release binaries, so this needs a Go toolchain + repo
// access. `go install` writes to GOBIN; if the running binary lives elsewhere
// (e.g. ~/.local/bin), we copy the fresh build over it so the in-use arti is
// the one that gets updated.
type UpdateCmd struct{}

func (UpdateCmd) Run(*CLI) error {
	fmt.Fprintf(stderr(), "current: arti %s\n", displayVersion())
	fmt.Fprintf(stderr(), "installing %s@latest …\n", artiModule)

	inst := exec.Command("go", "install", artiModule+"@latest")
	// Fetch the module directly from its VCS (skipping the Go proxy) so a
	// private deployment's repo resolves; harmless when the module is public.
	inst.Env = append(os.Environ(), "GOPRIVATE="+artiModuleRoot)
	inst.Stdout, inst.Stderr = stderr(), stderr()
	if err := inst.Run(); err != nil {
		return fmt.Errorf("go install failed (needs a Go toolchain + arti repo access): %w", err)
	}

	installed := goBinArti()
	self, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if installed != "" && self != "" && installed != self {
		if err := replaceBinary(installed, self); err != nil {
			fmt.Fprintf(stderr(), "installed to %s — copy it onto your arti:\n  cp %s %s\n", installed, installed, self)
			return nil
		}
		fmt.Fprintf(stderr(), "updated %s\n", self)
	}
	fmt.Fprintln(stderr(), "✓ updated to latest main")
	return nil
}

// goBinArti resolves where `go install` placed the binary (GOBIN, else GOPATH/bin).
func goBinArti() string {
	if out, err := exec.Command("go", "env", "GOBIN").Output(); err == nil {
		if p := strings.TrimSpace(string(out)); p != "" {
			return filepath.Join(p, "arti")
		}
	}
	if out, err := exec.Command("go", "env", "GOPATH").Output(); err == nil {
		if p := strings.TrimSpace(string(out)); p != "" {
			return filepath.Join(p, "bin", "arti")
		}
	}
	return ""
}

// replaceBinary copies src over dst atomically (write a sibling temp, then
// rename within the same directory). Fails if dst's directory isn't writable.
func replaceBinary(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
