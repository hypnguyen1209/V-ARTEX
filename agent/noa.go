package agent

import (
	"log"
	"path/filepath"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa is the model-driven compaction mechanism introduced in norma v0.4.0, exposed as an experimental
// system setting. It is mutually exclusive with built-in compaction: noaadapter.Enable is the sole entry
// point, attaching the Compactor, Compress tool, and three persistent prompt blocks. Without Enable,
// built-in compaction runs normally. Each agent reads noaEnabledFn once per run, so toggling affects
// only subsequently started runs without rebuilding agents.

// enableNoa attaches noa to opts when enabled. archiveRoot is the persistent base for source archives
// (global workDir; all agents use <workDir>/noa rather than separate task/intent directories). sessionID
// names the archive subdirectory and is globally unique, preventing collisions within the shared base.
//
// As an experimental feature, noa failures must not interrupt real tasks; report through onWarn and fall back.
// On success, clear opts.Compaction to avoid agentcore warnings about two context managers.
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err := noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}); err != nil {
		if onWarn != nil {
			onWarn("Failed to enable noa compaction; falling back to built-in compaction: " + err.Error())
		}
		return
	}
	// Compactor supersedes Compaction, but agentcore warns on every call if both are set; clear it explicitly.
	opts.Compaction = nil
}
