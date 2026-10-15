package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const findingIDGuidance = "\n\n**Finding ID convention**: finding_id is the standalone finding record ID; finding_node_id is the exploration node ID. The id in list_findings / list_task_findings / node_detail / get_task_node_detail remains an exploration node ID; read the standalone ID from finding_id in the same response. get_finding_traffic / bind_finding_traffic use the standalone finding_id. The legacy finding_id parameter of update_finding_report still takes finding_node_id. Do not use the number on the first line of report_finding with evidence tools, or guess other numbers after an ID error."

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled != nil && FindingTrafficBindingEnabled()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled() {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = "\nBy default, the report Agent verifies and binds traffic before writing the report. The reporter of a finding should preserve verification commands, key output, known real traffic IDs, and their purposes in evidence so the report Agent can check them against execution records; no extra traffic lookup is required just for binding. Explicit immediate binding remains supported: traffic_refs or evidence_hint_id may supply verified references; the latter reads structured references from the specified hint in this task. Any invalid reference fails the entire submission. These optional parameters are unnecessary for TCP or findings without captures. Returned finding_id and finding_node_id identify the standalone record and exploration node, respectively."
		case "add_hint", "add_task_hint":
			note = "\nWhen handing off a confirmed finding, retain verified traffic IDs, roles, notes, and order in traffic_refs of the corresponding hint (top-level for a single hint, or in the matching hints element for a batch), and describe the specific finding it proves in text. Do not hand off only prose and discard existing traffic references. Unverified candidates must not be passed as evidence."
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n**Traffic evidence handoff (optional)**: By default, the report Agent binds traffic after the finding is stored and before writing the report. Preserve verification commands, key output, known real traffic IDs, and their purposes in evidence; include intent_id in a task for traceability. No extra traffic lookup is required just for binding. Auto / Planner must retain the executor's existing references when submitting on their behalf. add_hint / add_task_hint can hand off traffic_refs; report_finding still supports explicit immediate binding through traffic_refs / evidence_hint_id. Record TCP findings or findings without captures normally; never guess IDs or repeat probing solely to obtain a capture."
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\nIn a platform conversation without task context, do not call report_finding directly. Hand off to an existing relevant task through add_task_hint so its Agent records the finding, then verify the result with list_task_findings."
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\nBefore declaring the goal complete, finish submitting or handing off the evidence already available. Do not end the task or cancel Workers merely because the prose finding was recorded while evidence handoff remains incomplete. Findings without captures do not require waiting or forcing a capture."
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**Automatic traffic association before reporting (enabled)**: Verify and bind traffic for the finding that triggered this run, then write the report. First obtain explicit finding_id and finding_node_id from the report_finding JSON response or get_task_node_detail / list_task_findings. Read the finding details, its intent's execution records, and existing evidence list; prefer real IDs handed off by the reporting Agent. If verification used HTTP and traffic tools are available, filter candidates with traffic_search, then inspect each request/response with traffic_get to confirm it supports this finding. Domain and time only filter candidates; they do not prove association. Bind confirmed evidence in reproduction order with bind_finding_traffic(finding_id, traffic_refs), choosing baseline / proof / verification / supporting and explaining its purpose. Operate only on this finding; do not create duplicates or probe the target again. After successful binding, call get_finding_traffic again for the latest version, read the required bodies, and pass the version you actually read as evidence_version to update_finding_report (its finding_id parameter still takes finding_node_id). Do not append existing bindings again. For TCP, missing captures, unavailable tools, or no exact match, skip automatic binding and write the report using prose/command evidence, explaining why. Never guess to fill out traffic evidence. If binding fails, do not claim success; preserve existing evidence and explain the failure in the report."
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "Optional: verified traffic references for the specific finding in this hint, in order. After handoff, report_finding can carry these references via evidence_hint_id.", "items": obj(map[string]any{"traffic_id": str("Real traffic ID"), "role": str("baseline / proof / verification / supporting"), "note": str("Conclusion supported by this traffic")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d must identify a hint node in this task (inherited hints cannot be used directly for binding)", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
