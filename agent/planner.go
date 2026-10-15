package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn      func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Your planning todos (preserved across wake-ups, written in your previous turn)]:\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("Proceed from this list: dispatch an intent for a next step only when its prerequisites are complete or its required facts already exist. Update the list with TodoWrite, marking steps satisfied by facts as completed. Do not dispatch duplicates of pending/in_progress steps.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done" - a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" - a worker reported a finding on intent IntentID (Detail = summary).
//	"goal" - the human (via the main Agent's set_goals) added one or more goals in a
//	    single call (Goals = newly added goal text; set_goals supports batches).
//	"goal_deleted" - the human deleted a goal in overview goal management (Detail = deleted text).
//	"goal_edited" - the human edited a goal in overview goal management (OldGoal -> NewGoal text).
//	"cancelled" - the human deleted intent IntentID (Detail = deletion reason). The intent is
//	    stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled": intent summary captured before deletion; hard deletion removes the queryable node.
	Goals    []string // Kind=="goal": one or more goals added in this set_goals call.
	OldGoal  string   // Kind=="goal_edited": previous goal text.
	NewGoal  string   // Kind=="goal_edited": updated goal text.
	Hints    []string // Kind=="hint": one or more hints added in this add_hint call.
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Actual changes that triggered this turn (read before deciding to add directions)]:")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human (main Agent) added one goal: %s. This is a new goal to achieve; add exploration directions if no corresponding intent exists.", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human (main Agent) added %d goals: %s. Add exploration directions for each new goal that has no corresponding intent.", len(ev.Goals), strings.Join(ev.Goals, "; ")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human (main Agent) added one strategic hint: %s. It is now in the exploration graph; adjust/add directions accordingly if no corresponding intent exists.", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human (main Agent) added %d strategic hints: %s. All are now in the exploration graph; adjust/add directions for each.", len(ev.Hints), strings.Join(ev.Hints, "; ")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- The human deleted this goal: %s. Reassess remaining goals/directions; no further intents are needed for the removed goal.", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- The human changed a goal from '%s' to '%s'. Adjust exploration for the new goal and stop dispatching obsolete directions.", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- The worker for intent #%d (%s) reported a finding: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// Prefer Summary captured at deletion; hard deletion removes the node, so intentSummary cannot retrieve it.
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- The user deleted intent #%d. Summary: %s. Deletion reason: %s. It will no longer execute; replan accordingly.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- The worker for intent #%d (%s) finished with this conclusion: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("; new fact ids produced by this intent: %s ", fids))
			}
		}
	}
	b.WriteString("\n(Use node_detail / get_worker_output / list_findings for full details.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12, #15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, ", ")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(Failed to retrieve output)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(This work has no output record yet)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " ... (truncated; full text in get_worker_output)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n[Current overview (prefetched graph_overview, equivalent to calling the tool; use node_detail/list_facts as needed for details)]:\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (section [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the intermediate artifact output rules
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `You are the planner in an authorized penetration testing system, awakened frequently when the graph changes. Read the situation → assess objectives → add exploration intents **only for genuinely new, uncovered directions**. Your deliverables are intents or objective assessments; do not execute worker work during planning.

Task objective: {{.Goal}}

**How many intents should this round produce?**
- **Mandatory minimum, highest priority**: if objectives remain unmet and there are **no open or running intents** (frontier_open=0 and running_intents is empty), you **must** produce at least one intent advancing the objective. Nothing is running or queued to wait for, so zero intents would stall the task. Even when known directions appear only in recent_done, open or continue a direction using the done/exhausted/blocked rules below.
- Otherwise, zero intents is valid only with a reason: (1) all considered directions are already covered by open/running intents; duplicate intents with different wording are a serious error; or (2) the next step depends on an actual result from running work, so dispatch must wait for the next graph update.
- Dispatch a new direction when it is uncovered and independent of running work, or when unmet objectives still have untested in-scope surfaces. Do not default to zero as a shortcut.

**Decision process on every wake-up:**

1. **The full situation is attached below**, as graph_overview output; do not call it again. It includes task (original title + objective/root node), asset counts, goals/statuses, open/running/recent_done intents, sites_without_endpoints (possible unexplored directions), facts (exploration facts, distinct from vulnerabilities), and recent_facts ({id,summary,confidence?}).
   - **Scope**: goals/intents/facts/findings belong to this task. **Assets are shared globally**; counts describe all in-scope assets, not exclusively this task. Ignore irrelevant assets.
   - **Lineage**: intents have parents (upstream facts/intents) and yields (downstream facts/findings); each recent_facts entry has from_intent. Use these relationships to understand evidence origins and combine new directions.
   - **Negative/uncertain observations**, such as "port closed" or "not injectable", are worker observations, not final judgments. Read node_detail(id) evidence first. Treat a direction as temporarily closed only when evidence is strong, confidence=observed, and reasonable techniques were exhausted. Missing evidence, a single probe, speculation, or confidence=inferred means unresolved. If in scope and uncovered, normally dispatch one verification intent to confirm or refute it. **Recheck the same negative direction at most once**; respect a repeated, reasonably evidenced negative conclusion.
   - Fetch deeper details only as needed: list_facts (paginated, newest first, default 20, q filtering, before pagination, total/has_more), list_findings (all findings), node_detail(id) (complete evidence/details), list_assets (q search, type/company_id/task_id filters, pagination, or direct id/ids retrieval), asset_neighbors. Do not retrieve every globally shared asset by default.

2. **Assess objectives, your core responsibility.** The goals field includes objectives and status. For an unmet objective proved by a finding/fact, call prove_goal(goal_id, evidence_id, reason) to mark met. **Marking the last unmet objective automatically completes the whole task**; completion is driven only by individual prove_goal calls.
   - **Verify quantitative acceptance criteria before approval.** For required coverage, flag counts, or privileges, compare actual graph_overview values (coverage.pct, findings_total, etc.) before prove_goal. If the threshold is unmet, do not mark met; dispatch work for the gap. "Mostly done" or "the core is done" is not sufficient. Example: required coverage 100%, measured coverage.pct=40% → unmet, continue testing.

3. **Optional, minimal startup probing for understanding.** Only when the graph has almost no facts (recent_facts effectively empty, task just started) and the situation cannot support a concrete initial intent, use very few read-only probes, such as 1–2 curl requests for a homepage/fingerprint. **The only permitted deliverable is a more precise intent**, never vulnerability discovery/verification/exploitation or endpoint/directory/parameter enumeration; those belong to workers. Three hard limits:
   - Once worker facts exist (facts>0 / recent_facts nonempty), **do not probe yourself**. Base judgments on facts and only dispatch intents or finish. Assign deeper investigation to a worker.
   - Even at startup, stop after at most 3 probes. If you begin detailed verification rather than quick direction setting (endpoint/directory/ID enumeration, decoding chains, repeated requests to one interface, injection/access control/vulnerability tests), stop immediately and express it as an intent.
   - If existing facts/situation suffice, do not probe.

4. **Choose new directions.** Restraint means avoiding duplicate intents, not minimizing useful work. With unmet objectives, ask which deeper, stronger, uncovered approaches could advance them. Intents are open exploration directions, not a fixed menu. Compare each proposed direction against open + running + recent_done:
   - Covered by open/running → do not regenerate.
   - Appears in recent_done → inspect its state before deciding:
     · **done**: already covered; do not resend unchanged. Judge dead ends from yielded facts, not state. Redispatch only for a materially new mechanism (new fact/asset/parameter/substantially different technique), explaining the difference in summary. Rewording or hoping a retry succeeds does not count.
     · **exhausted** (budget ended mid-investigation with partial write-back) / **blocked** (model/network failure, little investigation): inspect get_worker_trace / get_worker_output for actual progress and blockers. Continue from saved progress if a breakthrough was close; redispatch for a purely external failure; change approach/direction for repeated identical blockers. Use real trace progress, not the state alone.
   - Entirely new, uncovered direction → dispatch.
   - All known directions covered by still-open/running work → finish without new intents and await progress. If only recent_done remains and objectives are unmet, the mandatory minimum above applies.
   - **Depth before coverage.** Coverage is an acceptance minimum, not the exploration objective. Prioritize exploiting promising RCE/privilege escalation/data disclosure entry points thoroughly over shallow checks across every asset.
   - **Maintain route diversity.** If current work crowds one route/entry point and a fundamentally different uncovered surface/asset class/chain exists, add that route before adding synonyms along the same one. Keep ideally 2–3 routes with distinct mechanisms (e.g. upload and authentication bypass), concentrating only after evidence of objective progress. Do not duplicate an already covered alternative. **Diversity always obeys operating constraints**: excluded entry points/ports/hosts/operations must never become intents.

   **Dispatch serial exploitation chains step by step.** In ①→②→③, later steps depend on earlier actual output; do not dispatch them all in parallel before prerequisites exist. Record the full chain with TodoWrite, one item per step. Dispatch only steps whose prerequisites are met (usually the first), then after facts arrive dispatch the next step on a later wake-up and mark satisfied items completed. Do not split the same action into "confirm trigger" and "trigger it". Parallel intents are only for independent dimensions, such as unrelated endpoints.

5. **Submit** the chosen directions with **one** add_intent call (intents array, at most 4 highest-value directions; no repeated individual calls):
   - **summary**: one natural-language sentence with the target's full address, action, and reason. Avoid fixed categories; this is the main deduplication description.
   - **asset_ids**: target asset IDs (0/1/multiple, from list_assets). Include them whenever a direction targets a specific site/endpoint/parameter/host, for coverage deduplication and graph linkage. Include all assets for cross-asset work; leave empty only for global reconnaissance with no specific asset.
   - **parent_ids**: optional upstream node IDs (0/1/multiple) supporting this direction. Include every fact synthesized into it, or its source intent/finding; leave empty for entirely new top-level directions.

Do not duplicate or fabricate work, but dispatch uncovered, deeper approaches when objectives remain unmet. Be concise, focused, and efficient.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// Domain tools plus SDK defaults (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash).
	// Omit add_task_scope/list_untested_assets from tools and prompt when asset coverage is disabled.
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// Put dynamic context (recently finished intents and the prefetched graph) in this turn's user input,
	// leaving only static planning instructions in the system prompt for better cache reuse.
	// A long turn may compact that context, but planner turns are usually short. Append situational to input below.
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// Task deadline / finalization mode comes through ctx (taskclock.go). During finalization, append the
	// planner's task-timeout wrap-up as this turn's instruction in user input alongside situational context,
	// so it only assesses final goal completion and creates no new intents.
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n[Task finalization (special instructions for this turn, overriding normal planning above)]:" + resolveTaskTimeoutWrapup("planner")
	}
	// Pre-create this task's directory at <workDir>/tasks/<taskID>.
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // Inject operating constraints into the system prompt to define exploration boundaries.
	}
	system, boundary := deferredSystem(sysBody, def)
	// The planner has no own wall-clock budget. Clamp MaxDuration to task time remaining, allowing
	// settlement at the deadline: timeout uses task-timeout text, turn limit uses per-run text.
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // Use the recording proxy; load its CA to verify HTTPS certificates re-signed by MITM.
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// Optional web search: ddgs needs no key; brave-free needs BraveKey; tavily needs TavilyKey.
		// WebSearchProxy is a separate outbound http/https/socks5 proxy, independent of the recording MITM proxy; empty means direct.
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Bash subprocesses use the proxy and trust its CA by default.
		WorkingDir:            taskDir,                              // This task's directory: <workDir>/tasks/<taskID>.
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0 means unlimited; with a deadline, time remaining.
		Compaction:            compactionConfig(p.compactionWindow()),
		// Planning todos survive wake-ups, preserving sequential chains across fresh sessions with a shared store.
		Todos: p.todoFor(ts.ID()),
		// At this turn's budget, the SDK settles by persisting conclusions (add_intent, prove_goal,
		// and TodoWrite for sequential plans), not ending planning altogether; the planner will wake again.
		// When clamped by the task deadline, use PromptByReason (wrapupSettlementForTask).
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // Use Provider.Complete when this profile selects non-streaming mode.
		MaxTokens:    p.maxTokens(),    // 0 omits the output cap, using the server default.
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// Experimental: noa manages compaction when enabled; persistent archives live under <workDir>/noa/<SessionID>.
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// Append dynamic context (finished intents and full graph) to this turn's user input, alongside
	// instructions and persistent todos; model-authored todos are regenerable planning notes.
	// With actual changes, point the opening to the changes block; otherwise (heartbeat/hint/resume),
	// do not claim the graph changed, and prompt review of running intents instead.
	lead := "Specific changes just occurred (see [Actual changes that triggered this turn] below). Plan the next step accordingly:"
	if len(triggers) == 0 {
		lead = "This is a **scheduled heartbeat / wake-up without a specific change signal**; the graph may be unchanged. Review running intents: use steer_work for stalled or drifting work and kill_work when the whole direction is wrong. Then assess goals and decide whether more directions are needed:"
		// If heartbeat/no-change wakes with no open or running intents, exploration is stalled:
		// no workers and no queued directions. Explicitly require new directions instead of an empty review turn.
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "This is a **scheduled heartbeat**, and there are **no open or running intents**: no workers are active or queued, so exploration has stalled. You **must** create one or more new, goal-directed intents **distinct from existing graph intents** this turn (zero intents is prohibited). First assess whether goals are met from the overview below; if not, add directions immediately:"
		}
	}
	input := lead + situational + "\n\nAssess goals from the overview above. Use prove_goal for each goal truly achieved (the required outcome obtained or vulnerability confirmed). **Hard requirement: if goals remain unmet and there are no open or running intents (frontier_open=0 and running_intents empty), create at least one goal-directed intent this turn. There is no running work to await or queued direction; zero intents means the task stalls. You may omit new intents only while open/running intents are making progress or goals are already achieved.**" +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration interrupts running tools at the deadline and settles immediately on a live ctx, so a
	// stuck turn cannot bypass settlement and needs no external hard timeout. ctx handles only pause/kill/shutdown.
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
