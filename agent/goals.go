package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// goalsDefaultTmpl is the built-in EDITABLE body (section [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `You decompose penetration testing objectives. Identify the **final outcomes the user wants**, rather than planning attack steps.

**First, before decomposing objectives: extract operating constraints.**
Identify explicit rules in the task objective/description about which operations are permitted or prohibited, and register them individually with set_constraints. Skip this extraction when no operating constraints are stated:
- type=deny: prohibited operations, such as "no port scanning", "no production writes/deletes", "no brute force", or "exclude a particular subdomain".
- type=allow: explicitly permitted or limited scope, such as "passive reconnaissance only" or "only this domain".
- Constraints define operating boundaries; they are neither objectives nor attack steps.
- **Make each constraint self-contained with a specific target.** Replace references such as "current target/port/IP/domain/this site" with concrete values from the task objective/description. Constraints are injected independently into execution prompts, where such references would lose their meaning.
  Example: for https://abc.example.net, write "Test only abc.example.net", not "Test only the current target"; write "Test only target port 443; do not scan other ports", not "Test only the current port". If the text refers to the current target and the address is known, include that address.
- **Register only explicitly stated or emphasized constraints. Never invent them.** When the type is uncertain, use deny as the conservative choice.
- If the objective/description contains no operating constraints, **do not** call set_constraints.
After registering any constraints, decompose the objectives below.

**An objective is a final deliverable or verifiable outcome.**

**Do not list these as subgoals:**
- Information gathering, reconnaissance, or endpoint scanning.
- Vulnerability analysis or verification processes.
- Attack steps or exploitation methods.
- Result verification steps.

**Decomposition principles:**
- One final outcome described by the user → one objective.
- Multiple **independent** final deliverables → separate objectives.
- Set vulnclass for objectives corresponding to a specific vulnerability class; leave it empty for information gathering/business logic objectives.
- Never invent objectives the user did not mention.

Submit the result with set_goals.`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**Additional responsibility: register test asset scope**
In addition to decomposing goals, identify the **explicit test asset scope** in the task goal/description and register it with add_task_scope (the task's authorization boundary and asset-coverage denominator). **Minimum-scope rule: register only the precise target explicitly named by the user; never expand it on your own.**
- For a URL or hostname-bearing address (e.g. https://xxx.example.com/path, app.example.com), use its **complete hostname**, kind=subdomain, value=complete hostname.
  Example: https://a1b2c3.lab.example.net/path -> kind=subdomain, value=a1b2c3.lab.example.net (**not** example.net).
  **Never** reduce a hostname containing subdomains to its root domain. Registering all of example.com for xxx.example.com exceeds the user's target and violates the minimum-scope rule.
- Use kind=root_domain, value=example.com only if the user supplies a **bare root domain without any subdomain** (e.g. example.com), or explicitly requests the "entire site / all subdomains / whole domain".
- An IP or network -> kind=ip / cidr, value=IP or CIDR.
- **Do not** register company scope (company): at task creation the company usually does not yet exist in the asset system. Leave company-level scope for later planning.
Other rules:
- Register only scope **explicitly stated in the goal/description**; never invent or infer unmentioned domains/IPs.
- In reason, briefly cite the statement supporting the scope for auditability.
- If the goal/description specifies no asset scope, **do not** call add_task_scope.
First register scope with add_task_scope (if any), then submit goals with set_goals.`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc is the task's free-text description (target scope, flag count, rules of engagement, etc.).
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// Goal decomposition is a one-shot call without a transcript store, so agentcore does not attach a
	// session ID to ctx (it does so only with a writer; see agentcore.Prompt). Gateways using session
	// headers for caching or sticky routing read that value; opencode zen returns 400 MissingSessionID
	// without x-opencode-session, making decomposition fail even while chat works.
	// Attach a stable ID shared by decomposition requests for the same exploration to support caching,
	// without colliding with planner/worker IDs, so llmrec.parseSession attributes it correctly.
	if ts != nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	// set_constraints is always available, independent of the asset store. The editable prompt already
	// instructs extracting operating constraints before goals; wire only the tool here.
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += goalsScopeTail
	}
	userMsg := "Task goal:\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\nTask description (background that may include target scope, flag count, or rules of engagement; for reference only, do not invent unmentioned details):\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// Each step (extract constraints, register scope, decompose goals) needs a tool call; allow enough turns for set_goals.
		MaxTurns:     8,
		NonStreaming: nonStreaming, // Use Provider.Complete when this profile selects non-streaming mode.
		MaxTokens:    maxTokens,    // 0 omits the output cap, using the server default.
	}, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _ := ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n := range nodes {
		var p struct {
			Text      string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text) != "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
