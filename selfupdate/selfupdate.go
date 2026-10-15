// Package selfupdate implements one-click ARTEX updates: download a GitHub Release
// binary, verify it, stage it, and atomically install it on the next startup.
//
// The startup scripts (start.sh/start.bat) only restart according to exit status.
// This package handles download, SHA256 verification, smoke tests, installation,
// and rollback. Keeping these checks in Go avoids separate sh/bat implementations
// and prevents the supervisor from repeatedly starting an unusable binary.
//
// A full update uses three process starts:
// 1. Old server handles /api/update/apply, downloads and verifies artex.new, exits 75.
// 2. Supervisor restarts old server; Bootstrap verifies, smoke-tests and installs it, exits 75.
// 3. Supervisor starts new server; Bootstrap counts the attempt and clears the marker once stable.
//
// Verification failure discards the staged file and keeps the old version. After
// three unsuccessful new-version starts, artex.old is restored automatically.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart asks the supervisor to restart immediately (EX_TEMPFAIL), without
// crash backoff. Exit 0 stops the supervisor; other exits count as crashes.
const ExitRestart = 75

// maxAttempts limits startup attempts after installation. Each start increments
// the counter; surviving settleDelay clears the marker. Repeated crashes trigger rollback.
const maxAttempts = 3

// Paths lists all update files, located beside the executable. The working
// directory may be / or another service directory, so it cannot locate staged files.
type Paths struct {
	Dir     string // Executable directory
	Current string // Current executable        artex      / artex.exe
	New     string // Staged new version            artex.new  / artex.new.exe
	Sum     string // New version SHA256 (hex)  artex.new.sha256 / artex.new.exe.sha256
	Old     string // Previous version backup      artex.old  / artex.old.exe
	Marker  string // Update state marker            artex.upgrade.json
}

// ResolvePaths derives update paths from the current executable.
// Windows .new/.old files retain .exe so smoke tests and execution work on both platforms.
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("Locate executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	ext := filepath.Ext(name) // Usually .exe on Windows, empty on Unix
	stem := strings.TrimSuffix(name, ext)

	join := func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir:     dir,
		Current: exe,
		New:     join(".new"),
		Sum:     join(".new") + ".sha256",
		Old:     join(".old"),
		Marker:  filepath.Join(dir, stem+".upgrade.json"),
	}, nil
}

// marker tracks installation progress to trigger rollback if the new version cannot start.
type marker struct {
	From     string `json:"from"`     // Version before update
	To       string `json:"to"`       // Target version
	Attempts int    `json:"attempts"` // Startup attempts after installation
	StagedAt int64  `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged removes staged files after installation, verification failure, or
// cancellation so a leftover artex.new is not retried on the next start.
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions returns -1/0/1 for a<b / a==b / a>b. ok=false means at least one
// version is not comparable, such as dev or 0.3.7-2-gabc1234-dirty from git describe.
// Callers must disable one-click updates in that case to avoid overwriting development builds.
func CompareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range 3 {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion parses v0.3.7 / 0.3.7 into [3]int. Only clean three-component versions
// are comparable. Suffixed git describe builds must not be treated as 0.3.7, which
// could incorrectly mark development builds current or overwrite them with a release.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InDocker reports whether the process runs in a container. Updates modify its
// writable layer; docker compose up -d recreates the container using the image's
// version. The frontend explains this expected behavior when pulling a new image.
func InDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}
