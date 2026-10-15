package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// smokeEnv makes the smoke-test subprocess skip Bootstrap. Although its artex.new
// executable derives separate .new-prefixed paths, explicitly skipping is clearer
// and avoids relying on that coincidence or probing the disk unnecessarily.
const smokeEnv = "ARTEX_SELFUPDATE_SMOKE"

// Action tells main how to proceed after Bootstrap.
type Action int

const (
	// Continue starts the server normally.
	Continue Action = iota
	// Restart exits immediately with ExitRestart so the supervisor starts the process again.
	Restart
)

// State describes startup update status so /api/update/check can report whether
// the previous update succeeded or was rolled back.
type State struct {
	Pending     bool   // Installed but not yet confirmed stable
	RolledBack  bool   // Automatically rolled back during this startup
	FailedStage bool   // Staged binary failed verification/smoke testing and was discarded
	Detail      string // Brief user-facing explanation
}

// Bootstrap runs at the start of main, before listening on ports or opening the database.
// If artex.new exists, verify and smoke-test it, then install and restart, or discard it on failure.
// If only the marker remains, count a new-version startup attempt and roll back after repeated failures.
// With neither present, start normally.
func Bootstrap() (Action, State) {
	if os.Getenv(smokeEnv) != "" {
		return Continue, State{}
	}
	p, err := ResolvePaths()
	if err != nil {
		log.Printf("[update] skipping bootstrap: %v", err)
		return Continue, State{}
	}

	if _, err := os.Stat(p.New); err == nil {
		return applyStaged(p)
	}

	m, ok := readMarker(p.Marker)
	if !ok {
		return Continue, State{}
	}
	return confirmOrRollback(p, m)
}

// applyStaged installs a staged binary only after verification. This is the sole
// executable replacement point and final gate. Smoke tests reject corrupt downloads,
// wrong architectures and missing libraries. A binary that cannot execute cannot
// run Go rollback code, leaving the supervisor in an endless restart loop.
func applyStaged(p Paths) (Action, State) {
	m, _ := readMarker(p.Marker)

	if err := verifyStaged(p); err != nil {
		log.Printf("[update] staged version failed verification and was discarded; continuing with current version: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "New version failed verification and was discarded: " + err.Error()}
	}

	if err := swap(p); err != nil {
		log.Printf("[update] installation failed; continuing with current version: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "Installation failed: " + err.Error()}
	}

	// Keep the marker after installation; the next start runs the new version and confirms stability.
	m.Attempts = 0
	if m.StagedAt == 0 {
		m.StagedAt = time.Now().Unix()
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] could not write update marker (automatic rollback unavailable): %v", err)
	}
	log.Printf("[update] installed %s; exiting to restart (exit %d)", orUnknown(m.To), ExitRestart)
	return Restart, State{Pending: true}
}

// confirmOrRollback counts starts after installation and restores the old version
// when the limit is exceeded. It catches executable binaries that crash during
// initialization (configuration, port, or database migration errors). Smoke tests
// cover binaries that cannot execute at all; both checks are necessary.
func confirmOrRollback(p Paths, m marker) (Action, State) {
	m.Attempts++
	if m.Attempts > maxAttempts {
		if err := rollback(p); err != nil {
			// Do not restart again after rollback failure or this could loop forever. Clear
			// the marker and attempt normal startup so any failure remains visible in logs.
			log.Printf("[update] new version failed to start %d consecutive times and rollback failed: %v", maxAttempts, err)
			_ = os.Remove(p.Marker)
			return Continue, State{Detail: "New version startup and rollback failed: " + err.Error()}
		}
		log.Printf("[update] new version failed to start %d consecutive times; rolled back to %s, exiting to restart (exit %d)",
			maxAttempts, orUnknown(m.From), ExitRestart)
		_ = os.Remove(p.Marker)
		return Restart, State{RolledBack: true, Detail: fmt.Sprintf("New version failed to start; rolled back to %s", orUnknown(m.From))}
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] could not update upgrade marker: %v", err)
	}
	log.Printf("[update] starting new version (attempt %d/%d); update will be confirmed once stable",
		m.Attempts, maxAttempts)
	return Continue, State{Pending: true}
}

// Settle clears the update marker after the new version has run stably.
// main calls it after a delay once HTTP is listening. A crash before then leaves
// the marker so the next startup counts another attempt and may roll back.
func Settle() {
	p, err := ResolvePaths()
	if err != nil {
		return
	}
	settle(p)
}

func settle(p Paths) {
	if _, ok := readMarker(p.Marker); !ok {
		return // Normal startup; nothing to settle.
	}
	if err := os.Remove(p.Marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[update] could not clear update marker: %v", err)
		return
	}
	log.Printf("[update] new version is stable; update complete (previous version retained as %s)", p.Old)
}

// SettleDelay is the required runtime before the new version is considered stable.
const SettleDelay = 30 * time.Second

// verifyStaged checks SHA256, then starts the binary once.
func verifyStaged(p Paths) error {
	want, err := os.ReadFile(p.Sum)
	if err != nil {
		return fmt.Errorf("Read checksum: %w", err)
	}
	got, err := fileSHA256(p.New)
	if err != nil {
		return fmt.Errorf("Calculate checksum: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(want)), got) {
		return errors.New("SHA256 mismatch (download corrupt or tampered with)")
	}
	return smokeTest(p.New)
}

// smokeTest runs the new binary with -h to confirm it executes on this system,
// catching truncated downloads, wrong architectures and missing dependencies.
func smokeTest(bin string) error {
	if err := os.Chmod(bin, 0o755); err != nil {
		return fmt.Errorf("Set executable permissions: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-h")
	cmd.Env = append(os.Environ(), smokeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("Smoke test timed out (new binary unresponsive)")
	}
	if err != nil {
		snippet := strings.TrimSpace(string(out))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		return fmt.Errorf("Smoke test failed: %v: %s", err, snippet)
	}
	return nil
}

// swap replaces the current binary with the staged version. Unix and Windows
// allow renaming running executables; Windows prohibits deleting or overwriting
// them, not renaming. No platform-specific shutdown is needed.
func swap(p Paths) error {
	// Windows rename cannot overwrite an existing destination; remove the previous .old first.
	if err := os.Remove(p.Old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Remove old backup %s: %w", p.Old, err)
	}
	if err := os.Rename(p.Current, p.Old); err != nil {
		return fmt.Errorf("Back up current version: %w", err)
	}
	if err := os.Rename(p.New, p.Current); err != nil {
		// If installation fails after moving the current binary, restore it or the next startup has no executable.
		if rerr := os.Rename(p.Old, p.Current); rerr != nil {
			return fmt.Errorf("Installing new version failed (%v), and restoring current version failed: %w", err, rerr)
		}
		return fmt.Errorf("Install new version: %w", err)
	}
	_ = os.Remove(p.Sum)
	return nil
}

// rollback restores the old version backed up by swap.
func rollback(p Paths) error {
	if _, err := os.Stat(p.Old); err != nil {
		return fmt.Errorf("No rollback backup %s: %w", p.Old, err)
	}
	// Keep the failed new version as .failed for diagnosis rather than deleting it.
	failed := p.Current + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(p.Current, failed); err != nil {
		return fmt.Errorf("Move failed version: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		return fmt.Errorf("Restore old version: %w", err)
	}
	return nil
}

// Rollback implements /api/update/rollback by restoring the previous version.
// It only swaps files; the caller exits with ExitRestart and the supervisor restarts.
func Rollback() error {
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.Old); err != nil {
		return errors.New("No previous version for rollback (" + p.Old + " does not exist)")
	}
	cleanStaged(p)
	if err := smokeTest(p.Old); err != nil {
		return fmt.Errorf("Previous version cannot execute; refusing rollback: %w", err)
	}
	// Swap current and backup so this rollback can itself be reversed.
	tmp := p.Current + ".swap"
	_ = os.Remove(tmp)
	if err := os.Rename(p.Current, tmp); err != nil {
		return fmt.Errorf("Move current version: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		_ = os.Rename(tmp, p.Current)
		return fmt.Errorf("Install previous version: %w", err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		log.Printf("[update] backup cleanup after rollback failed (execution unaffected): %v", err)
	}
	_ = os.Remove(p.Marker)
	return nil
}

// HasBackup reports whether a previous version exists, controlling the frontend rollback button.
func HasBackup() bool {
	p, err := ResolvePaths()
	if err != nil {
		return false
	}
	_, err = os.Stat(p.Old)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown version"
	}
	return s
}
