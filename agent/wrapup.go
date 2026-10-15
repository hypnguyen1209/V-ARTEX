package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// Wrap-up / settlement prompt: when an agent reaches its turn budget (MaxTurns)
// or timeout (run_seconds/MaxDuration), the SDK injects this prompt during settlement
// so it persists identified results before summarizing, avoiding unfinished write-back.
//
// Each agent's wrap-up prompt can be overridden in agents.wrapup_prompt; empty uses
// the built-in default here. Only the prompt body is editable; disabled tools and settlement budgets are fixed in code.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// Built-in wrap-up prompts indexed by agent key. Workers reuse settleWrapUpPrompt
// from worker.go; planner/mainagent have dedicated versions, and custom agents use the generic fallback.
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults provides each agent's settlement turn budget; a positive DB value overrides it.
// All default to 10 turns to allow sufficient write-back; unknown keys use genericWrapupTurns.
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "Your planning round is nearly out of turns. Only this round is ending: the system will wake you again as the situation changes, so you do not need to conclude the entire task. Persist conclusions already reached without inventing intents just to wrap up (zero intents is a normal result): (1) Submit directions that should be dispatched now in one batch with add_intent; (2) Mark objectives already proven by findings/facts as met with prove_goal; (3) Record serial exploitation chains in TodoWrite so later rounds can continue step by step. Then end this round without a summary."

const mainAgentWrapUpDefault = "You are nearly out of turns and this interaction is ending. Start no new exploration or operations. In a separate single plain-text sentence, summarize current progress, key conclusions, and the recommended next step for the user."

const genericWrapUpDefault = "Your run is about to end because its budget is exhausted. First persist completed results that have not been saved, then summarize your actions and key conclusions in a separate single plain-text sentence (displayed as this run's result)."

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride != nil {
		if t, ok := WrapupOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride != nil {
		if v, ok := WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        resolveWrapup(agentKey),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
}

// ---------- Task-timeout wrap-up prompts ----------
//
// Task-timeout text differs from per-run text: per-run means this run's budget is exhausted;
// task timeout means the entire task is ending. Their instructions may be opposite, especially for planner:
// per-run says keep planning, while task timeout says stop planning and make a final assessment. Worker/planner only.

// WrapupTaskTimeoutOverride / ...TurnsOverride supply DB overrides for task-timeout text and turns
// (agents.task_timeout_wrapup_prompt / _max_turns; worker/planner only).
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**The entire task has reached its timeout and is about to end** (this is the whole exploration deadline, not just your run budget). This is your last chance: (1) Persist all identified but unsaved results: new assets with insert_assets, exploration conclusions/facts with record_fact, and confirmed vulnerabilities with report_finding; (2) Start no new commands or probes; (3) Finish with a separate single plain-text sentence summarizing your key conclusions for this intent."

const plannerTaskTimeoutDefault = "**The entire task has reached its timeout and is about to end** (the whole task is terminating, not just this round). Make one final objective assessment using all current facts and findings: call prove_goal to mark every objective supported by evidence as met. **Generate no new intents**; they would no longer be executed. End after the assessment without a summary."

// TaskTimeoutWrapupDefault returns an agent's built-in task-timeout text for UI placeholders or resetting defaults.
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // Unconfigured agents (mainagent/chat) return an empty string.
}

// resolveTaskTimeoutWrapup: nonempty DB override takes precedence over the built-in default. Empty means
// the agent has no task-timeout text (not worker/planner), so callers should fall back to per-run text.
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride != nil {
		if t, ok := WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride != nil {
		if v, ok := WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // Default to the per-run turn budget.
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true: the task deadline limits this run. Timeout settlement uses task-timeout text;
//     MaxTurns means turns ran out first while task time remains, so use per-run text.
//   - clamped=false: both reasons use per-run text, equivalent to wrapupSettlement.
//
// harness selects PromptByReason using the actual settlement reason to avoid mismatches at build time.
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := resolveWrapup(agentKey)
	st := &harness.Settlement{
		Prompt:        perRun, // Fallback, also used for both reasons when not clamped.
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  tt,     // Task deadline reached.
				harness.ReasonMaxTurns: perRun, // Turns exhausted while task time remains.
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}

// EnglishWrapupDefaults exposes current wrap-up replacements by migration key.
func EnglishWrapupDefaults() map[string]string {
	return map[string]string{
		"wrapup:generic":       genericWrapUpDefault,
		"wrapup:planner":       WrapupDefault("planner"),
		"wrapup:mainagent":     WrapupDefault("mainagent"),
		"wrapup:worker":        WrapupDefault("worker"),
		"task-timeout:worker":  TaskTimeoutWrapupDefault("worker"),
		"task-timeout:planner": TaskTimeoutWrapupDefault("planner"),
	}
}
