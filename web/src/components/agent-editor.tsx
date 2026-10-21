"use client";

import * as React from "react";

import { EyeIcon, GitCompareIcon, PencilIcon, RotateCcwIcon, SaveIcon, Trash2Icon, XIcon } from "lucide-react";
import { toast } from "sonner";

import { Markdown } from "@/components/markdown";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import type {
  Agent,
  AgentDetail,
  AgentTrigger,
  MCPServer,
  PromptVar,
  PromptVersion,
  Settings,
  SkillItem,
  Tool,
} from "@/lib/types";
import { cn } from "@/lib/utils";

// Traffic tools are host tools gated by the global Traffic capture switch: bindable, but
// only usable when capture is on. Keep this list in sync with traffic.SeedToolMetas.
const TRAFFIC_TOOL_KEYS = new Set(["traffic_search", "traffic_get"]);

// AgentEditor is the tabbed editor for one agent, used inside the agents-page
// drawer (and reused full-page for deep links). Tabs: Configuration and prompt / MCP / Skill /
// Tools. Config + prompt save as before; visibility + tool bindings toggle live.
export function AgentEditor({ agentKey, onSaved }: { agentKey: string; onSaved?: () => void }) {
  const [detail, setDetail] = React.useState<AgentDetail | null>(null);
  const [versions, setVersions] = React.useState<PromptVersion[]>([]);
  const [variables, setVariables] = React.useState<PromptVar[]>([]);
  const [mcp, setMcp] = React.useState<MCPServer[]>([]);
  const [skills, setSkills] = React.useState<SkillItem[]>([]);
  const [tools, setTools] = React.useState<Tool[]>([]);
  const [loaded, setLoaded] = React.useState(false);
  const [viewVer, setViewVer] = React.useState<PromptVersion | null>(null);
  const [diffVer, setDiffVer] = React.useState<PromptVersion | null>(null);

  const [prompt, setPrompt] = React.useState("");
  const [mcpVisible, setMcpVisible] = React.useState<number[]>([]);
  const [skillVisible, setSkillVisible] = React.useState<string[]>([]);
  const [preview, setPreview] = React.useState("");
  const [maxTurns, setMaxTurns] = React.useState("0");
  const [runSecs, setRunSecs] = React.useState("600");
  // "" = follow (unbound); otherwise a profile ID string
  const [llmProfileId, setLlmProfileId] = React.useState("");
  const [llmProfiles, setLlmProfiles] = React.useState<NonNullable<AgentDetail["llm_profiles"]>>([]);
  const [webSearch, setWebSearch] = React.useState(false);
  const [interactiveShell, setInteractiveShell] = React.useState(false);
  const [wrapup, setWrapup] = React.useState("");
  const [wrapupDefault, setWrapupDefault] = React.useState("");
  const [wrapupTurns, setWrapupTurns] = React.useState("0");
  const [wrapupTurnsDefault, setWrapupTurnsDefault] = React.useState(5);
  // Task timeout wrap-up prompts (worker/planner only)
  const [ttSupported, setTtSupported] = React.useState(false);
  const [ttWrapup, setTtWrapup] = React.useState("");
  const [ttWrapupDefault, setTtWrapupDefault] = React.useState("");
  const [ttTurns, setTtTurns] = React.useState("0");
  const [ttTurnsDefault, setTtTurnsDefault] = React.useState(5);
  const [settings, setSettings] = React.useState<Settings | null>(null);

  React.useEffect(() => {
    api
      .mcpServers()
      .then(setMcp)
      .catch(() => {
        // The remaining editor tabs do not depend on MCP metadata.
      });
    api
      .skills()
      .then(setSkills)
      .catch(() => {
        // The remaining editor tabs do not depend on skill metadata.
      });
    api
      .tools()
      .then(setTools)
      .catch(() => {
        // The remaining editor tabs do not depend on tool metadata.
      });
    api
      .settings()
      .then(setSettings)
      .catch(() => {
        // Missing global settings leave optional capabilities safely disabled.
      });
  }, []);
  // Global gates: traffic tools require Traffic capture; web search requires the master switch.
  const captureOn = !!settings?.traffic_capture;
  const webSearchGlobalOn = !!settings?.web_search_enabled;

  const reload = React.useCallback(() => {
    api
      .getAgent(agentKey)
      .then((d) => {
        setDetail(d);
        setPrompt(d.prompt ?? "");
        setVariables(d.variables ?? []);
        setVersions(d.versions ?? []);
        setMcpVisible(d.visibility?.mcp ?? []);
        setSkillVisible(d.visibility?.skill ?? []);
        setMaxTurns(String(d.agent?.max_turns ?? 0));
        setRunSecs(String(d.agent?.run_seconds ?? 600));
        setLlmProfileId(d.agent?.llm_profile_id != null ? String(d.agent.llm_profile_id) : "");
        setLlmProfiles(d.llm_profiles ?? []);
        setWebSearch(!!d.agent?.web_search);
        setInteractiveShell(!!d.agent?.interactive_shell);
        setWrapup(d.wrapup_prompt ?? "");
        setWrapupDefault(d.wrapup_default ?? "");
        setWrapupTurns(String(d.wrapup_max_turns ?? 0));
        setWrapupTurnsDefault(d.wrapup_max_turns_default ?? 5);
        setTtSupported(!!d.task_timeout_wrapup_supported);
        setTtWrapup(d.task_timeout_wrapup_prompt ?? "");
        setTtWrapupDefault(d.task_timeout_wrapup_default ?? "");
        setTtTurns(String(d.task_timeout_wrapup_max_turns ?? 0));
        setTtTurnsDefault(d.task_timeout_wrapup_max_turns_default ?? 5);
      })
      .catch(() => setDetail(null))
      .finally(() => setLoaded(true));
  }, [agentKey]);
  React.useEffect(() => {
    reload();
  }, [reload]);

  async function doPreview() {
    try {
      const r = await api.previewAgentPrompt(agentKey, prompt);
      setPreview(r.error ? `Render error: ${r.error}` : r.rendered);
    } catch (e) {
      setPreview(`Preview failed: ${(e as Error).message}`);
    }
  }
  async function savePrompt() {
    try {
      const r = await api.saveAgentPrompt(agentKey, prompt);
      toast.success(`Saved as version v${r.version}`);
      reload();
      onSaved?.();
    } catch (e) {
      toast.error(`Save failed: ${(e as Error).message}`);
    }
  }
  async function resetPrompt() {
    try {
      const r = await api.resetAgentPrompt(agentKey);
      toast.success(`Restored built-in defaults (v${r.version})`);
      reload();
    } catch (e) {
      toast.error(`Restore failed: ${(e as Error).message}`);
    }
  }
  async function saveWrapup() {
    try {
      const turns = Math.max(0, Math.floor(Number(wrapupTurns) || 0));
      await api.saveAgentWrapup(agentKey, wrapup, turns);
      toast.success(
        wrapup.trim() || turns > 0
          ? "Wrap-up settings saved (effective on the next run)"
          : "Cleared; built-in defaults will be used",
      );
      reload();
    } catch (e) {
      toast.error(`Save failed: ${(e as Error).message}`);
    }
  }
  async function resetWrapup() {
    try {
      await api.resetAgentWrapup(agentKey);
      toast.success("Built-in defaults restored");
      reload();
    } catch (e) {
      toast.error(`Restore failed: ${(e as Error).message}`);
    }
  }
  async function saveTaskTimeoutWrapup() {
    try {
      const turns = Math.max(0, Math.floor(Number(ttTurns) || 0));
      await api.saveAgentTaskTimeoutWrapup(agentKey, ttWrapup, turns);
      toast.success("Task timeout wrap-up saved (effective on the next run)");
      reload();
    } catch (e) {
      toast.error(`Save failed: ${(e as Error).message}`);
    }
  }
  async function resetTaskTimeoutWrapup() {
    try {
      await api.resetAgentTaskTimeoutWrapup(agentKey);
      toast.success("Built-in defaults restored");
      reload();
    } catch (e) {
      toast.error(`Restore failed: ${(e as Error).message}`);
    }
  }
  async function saveConfig() {
    try {
      // Submit only fields shown for this agent so hidden fields (such as goals max_turns) are not overwritten with defaults.
      const patch: Parameters<typeof api.saveAgentConfig>[1] = {
        llm_profile_id: llmProfileId === "" ? null : Number(llmProfileId),
      };
      if (showConfig) {
        patch.max_turns = Math.max(0, Math.floor(Number(maxTurns) || 0));
        patch.run_seconds = Math.max(0, Math.floor(Number(runSecs) || 0));
      }
      if (showWebSearch) patch.web_search = webSearch;
      if (showInteractiveShell) patch.interactive_shell = interactiveShell;
      await api.saveAgentConfig(agentKey, patch);
      toast.success("Runtime settings saved (effective immediately)");
      reload();
    } catch (e) {
      toast.error(`Save failed: ${(e as Error).message}`);
    }
  }
  // applyVis optimistically updates, persists, and toasts success/failure. On
  // failure it reverts to the prior selection so the UI never lies about state.
  async function applyVis(nextMcp: number[], nextSkill: string[], okMsg: string) {
    const prevMcp = mcpVisible;
    const prevSkill = skillVisible;
    setMcpVisible(nextMcp);
    setSkillVisible(nextSkill);
    try {
      await api.setAgentVisibility(agentKey, nextMcp, nextSkill);
      toast.success(okMsg);
      onSaved?.(); // refresh the list so the card's MCP/Skill counts stay in sync
    } catch (e) {
      setMcpVisible(prevMcp);
      setSkillVisible(prevSkill);
      toast.error(`Save failed: ${(e as Error).message}`);
    }
  }
  function toggleMcp(id: number) {
    const on = mcpVisible.includes(id);
    const name = mcp.find((m) => m.id === id)?.name ?? String(id);
    void applyVis(
      on ? mcpVisible.filter((x) => x !== id) : [...mcpVisible, id],
      skillVisible,
      `${on ? "Revoked" : "Granted"} visibility for MCP "${name}"`,
    );
  }
  function toggleSkill(name: string) {
    const on = skillVisible.includes(name);
    void applyVis(
      mcpVisible,
      on ? skillVisible.filter((x) => x !== name) : [...skillVisible, name],
      `${on ? "Revoked" : "Granted"} visibility for skill "${name}"`,
    );
  }
  async function toggleTool(t: Tool) {
    const on = t.agents.includes(agentKey);
    const nextAgents = on ? t.agents.filter((k) => k !== agentKey) : [...t.agents, agentKey];
    // optimistic update
    setTools((ts) => ts.map((x) => (x.key === t.key ? { ...x, agents: nextAgents } : x)));
    try {
      await api.saveTool(t.key, {
        description: t.description,
        schema: t.schema,
        agents: nextAgents,
        enabled: t.enabled,
      });
      toast.success(`${on ? "Unbound" : "Bound"} tool "${t.key}"`);
      onSaved?.(); // Refresh the list so the card's tool count stays in sync.
    } catch (e) {
      toast.error(`Failed to save tool bindings: ${(e as Error).message}`);
      reload();
      api
        .tools()
        .then(setTools)
        .catch(() => {
          // Preserve the optimistic rollback state if the recovery refresh also fails.
        });
    }
  }

  if (loaded && !detail) {
    return <div className="p-6 text-center text-muted-foreground text-sm">Agent not found: {agentKey}</div>;
  }
  // config is meaningless for the conversational main agent and the fixed-budget
  // goals decomposer; every other agent (workers, custom assistants) honors it.
  const showConfig = agentKey !== "mainagent" && agentKey !== "goals";
  // web search applies to every conversational/executing agent except the one-shot
  // goals decomposer; it's gated by the global master switch.
  const showWebSearch = agentKey !== "goals";
  // Interactive shell (persistent PTY tool family) is available to all agents except goals, with no global gate.
  const showInteractiveShell = agentKey !== "goals";
  // triggers (P3) only attach to custom agents.
  const isCustom = !!detail && !detail.agent?.builtin;

  return (
    <Tabs defaultValue="prompt" className="flex min-h-0 flex-1 flex-col">
      <TabsList className="mx-4 mt-2 w-fit">
        <TabsTrigger value="prompt">Config &amp; prompt</TabsTrigger>
        <TabsTrigger value="wrapup">Wrap-up</TabsTrigger>
        <TabsTrigger value="mcp">MCP</TabsTrigger>
        <TabsTrigger value="skill">Skill</TabsTrigger>
        <TabsTrigger value="tools">Tools</TabsTrigger>
        {isCustom && <TabsTrigger value="triggers">Triggers</TabsTrigger>}
      </TabsList>

      {/* Configuration and prompt */}
      <TabsContent value="prompt" className="min-h-0 flex-1 overflow-y-auto px-4 pb-4">
        <div className="grid gap-4">
          <div className="grid gap-3 rounded-md border p-3">
            <div className="grid gap-1.5">
              <Label htmlFor="llm-profile" className="text-xs">
                Default model (LLM profile)
              </Label>
              <div className="flex flex-wrap items-center gap-3">
                <Select
                  value={llmProfileId || "__follow__"}
                  onValueChange={(v) => setLlmProfileId(v === "__follow__" ? "" : v)}
                >
                  <SelectTrigger id="llm-profile" className="h-8 w-72">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__follow__">Follow task / globally active profile</SelectItem>
                    {llmProfiles.map((p) => (
                      <SelectItem key={p.id} value={String(p.id)}>
                        {p.name} ({p.model}){p.is_default ? " · Default" : ""}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <span className="max-w-md text-muted-foreground text-xs">
                  Bind this agent to a fixed LLM profile (use Save configuration to apply). Priority: agent binding &gt;
                  task/session assignment &gt; globally active profile.
                </span>
              </div>
            </div>
            <div className="flex flex-wrap items-end gap-3">
              {showConfig && (
                <>
                  <div className="grid gap-1.5">
                    <Label htmlFor="max-turns" className="text-xs">
                      Maximum turns (0 = unlimited)
                    </Label>
                    <Input
                      id="max-turns"
                      type="number"
                      min={0}
                      className="h-8 w-32"
                      value={maxTurns}
                      onChange={(e) => setMaxTurns(e.target.value)}
                    />
                  </div>
                  <div className="grid gap-1.5">
                    <Label htmlFor="run-seconds" className="text-xs">
                      Runtime (seconds, 0 = unlimited)
                    </Label>
                    <Input
                      id="run-seconds"
                      type="number"
                      min={0}
                      className="h-8 w-32"
                      value={runSecs}
                      onChange={(e) => setRunSecs(e.target.value)}
                    />
                  </div>
                </>
              )}
              <Button size="sm" variant="outline" onClick={saveConfig}>
                <SaveIcon /> Save configuration
              </Button>
            </div>
            {showWebSearch && (
              <div className="flex items-center gap-3 border-t pt-3">
                <Switch
                  id="web-search"
                  checked={webSearch}
                  disabled={!webSearchGlobalOn}
                  onCheckedChange={setWebSearch}
                />
                <div className="grid gap-0.5">
                  <Label htmlFor="web-search" className="text-sm">
                    Search the web
                  </Label>
                  <span className="text-muted-foreground text-xs">
                    {webSearchGlobalOn
                      ? "Enable web_search for this agent (save above to apply)"
                      : "Enable web search and configure its provider in System Settings first"}
                  </span>
                </div>
              </div>
            )}
            {showInteractiveShell && (
              <div className="flex items-center gap-3 border-t pt-3">
                <Switch id="interactive-shell" checked={interactiveShell} onCheckedChange={setInteractiveShell} />
                <div className="grid gap-0.5">
                  <Label htmlFor="interactive-shell" className="text-sm">
                    Interactive shell
                  </Label>
                  <span className="text-muted-foreground text-xs">
                    Enable persistent PTY tools (shell_open/send/read/close/list) for this agent to drive interactive
                    programs such as msfconsole/ssh/REPL (save above to apply).
                  </span>
                </div>
              </div>
            )}
          </div>

          <div className="grid gap-2">
            <Label className="text-muted-foreground text-xs">
              Variables (click to insert placeholders; replaced with runtime data during rendering)
            </Label>
            <div className="flex flex-wrap gap-2">
              {variables.map((v) => (
                <Tooltip key={v.name}>
                  <TooltipTrigger asChild>
                    <button
                      type="button"
                      onClick={() => setPrompt((p) => `${p}{{.${v.name}}}`)}
                      className="inline-flex items-center gap-1 rounded-md border bg-muted/40 px-2 py-1 font-mono text-xs hover:bg-muted"
                    >
                      {`{{.${v.name}}}`}
                      <Badge variant="secondary" className="px-1 py-0 text-[10px]">
                        {v.source}
                      </Badge>
                    </button>
                  </TooltipTrigger>
                  <TooltipContent className="max-w-xs">
                    <p className="font-medium">{v.description}</p>
                    <p className="mt-1 text-muted-foreground">Example: {v.example}</p>
                  </TooltipContent>
                </Tooltip>
              ))}
              {variables.length === 0 && <span className="text-muted-foreground text-xs">(No variables)</span>}
            </div>
          </div>

          <Textarea
            className="font-mono text-xs"
            rows={16}
            value={prompt}
            placeholder="Leave blank to use the built-in default prompt"
            onChange={(e) => setPrompt(e.target.value)}
          />

          <div className="flex flex-wrap gap-2">
            <Dialog>
              <DialogTrigger asChild>
                <Button variant="outline" size="sm" onClick={doPreview}>
                  <EyeIcon /> Preview rendering
                </Button>
              </DialogTrigger>
              <DialogContent className="sm:max-w-2xl">
                <DialogHeader>
                  <DialogTitle>Rendered preview</DialogTitle>
                  <DialogDescription>
                    All {`{{.Var}}`} have been replaced with sample values by the backend.
                  </DialogDescription>
                </DialogHeader>
                <div className="max-h-[60vh] overflow-auto rounded-md border bg-muted/30 p-3">
                  <Markdown text={preview} />
                </div>
              </DialogContent>
            </Dialog>
            <Button size="sm" onClick={savePrompt}>
              <SaveIcon /> Save as new version
            </Button>
            <Button variant="outline" size="sm" onClick={resetPrompt}>
              <RotateCcwIcon /> Restore defaults
            </Button>
          </div>

          <Separator />
          <div className="grid gap-2">
            <Label className="text-muted-foreground text-xs">Version history</Label>
            <ul className="grid gap-1">
              {versions.map((ver, i) => (
                <li
                  key={ver.version}
                  className="flex items-center gap-2 rounded-md px-1 py-0.5 text-xs hover:bg-muted/50"
                >
                  <span className="shrink-0 font-mono">v{ver.version}</span>
                  {i === 0 && (
                    <Badge variant="secondary" className="shrink-0 px-1.5 py-0">
                      Current
                    </Badge>
                  )}
                  <span className="flex-1 truncate text-muted-foreground">{ver.note}</span>
                  {ver.ts && (
                    <span className="shrink-0 text-muted-foreground/60 tabular-nums">
                      {new Date(ver.ts).toLocaleDateString("en-US", {
                        month: "2-digit",
                        day: "2-digit",
                        hour: "2-digit",
                        minute: "2-digit",
                      })}
                    </span>
                  )}
                  <Button variant="ghost" size="icon-sm" className="size-6 shrink-0" onClick={() => setViewVer(ver)}>
                    <EyeIcon className="size-3" />
                  </Button>
                  {i > 0 && versions[0] && (
                    <Button variant="ghost" size="icon-sm" className="size-6 shrink-0" onClick={() => setDiffVer(ver)}>
                      <GitCompareIcon className="size-3" />
                    </Button>
                  )}
                </li>
              ))}
              {versions.length === 0 && (
                <li className="text-muted-foreground text-xs">(No saved versions; using built-in defaults)</li>
              )}
            </ul>
          </div>

          {/* View version dialog */}
          <Dialog
            open={!!viewVer}
            onOpenChange={(o) => {
              if (!o) setViewVer(null);
            }}
          >
            <DialogContent className="sm:max-w-2xl">
              <DialogHeader>
                <DialogTitle>
                  v{viewVer?.version}
                  {viewVer?.version === versions[0]?.version && (
                    <Badge variant="secondary" className="ml-2 px-1.5 py-0 align-middle">
                      Current
                    </Badge>
                  )}
                </DialogTitle>
                <DialogDescription>
                  {viewVer?.note || "(No notes)"}
                  {viewVer?.ts && (
                    <span className="ml-2 text-muted-foreground/60">
                      {new Date(viewVer.ts).toLocaleString("en-US")}
                    </span>
                  )}
                </DialogDescription>
              </DialogHeader>
              <pre className="max-h-[55vh] overflow-auto whitespace-pre-wrap rounded-md bg-muted p-3 font-mono text-xs">
                {viewVer?.template_text || "(Empty)"}
              </pre>
              <div className="flex justify-end gap-2">
                {viewVer && viewVer.version !== versions[0]?.version && (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      if (viewVer) {
                        setDiffVer(viewVer);
                        setViewVer(null);
                      }
                    }}
                  >
                    <GitCompareIcon className="mr-1 size-3.5" /> Compare with current version
                  </Button>
                )}
                <Button
                  size="sm"
                  onClick={() => {
                    if (viewVer) {
                      setPrompt(viewVer.template_text);
                      setViewVer(null);
                      toast.success(
                        `Loaded v${viewVer.version} into the editor; review it and click Save as new version`,
                      );
                    }
                  }}
                >
                  Load into editor
                </Button>
              </div>
            </DialogContent>
          </Dialog>

          {/* Version comparison dialog */}
          <Dialog
            open={!!diffVer}
            onOpenChange={(o) => {
              if (!o) setDiffVer(null);
            }}
          >
            <DialogContent className="sm:max-w-3xl">
              <DialogHeader>
                <DialogTitle>
                  Version comparison: v{diffVer?.version} → v{versions[0]?.version} (current)
                </DialogTitle>
                <DialogDescription>
                  <span className="inline-flex items-center gap-3 text-xs">
                    <span className="rounded bg-red-500/15 px-1.5 py-0.5 text-red-600 dark:text-red-400">
                      - Removed
                    </span>
                    <span className="rounded bg-green-500/15 px-1.5 py-0.5 text-green-600 dark:text-green-400">
                      + Added
                    </span>
                  </span>
                </DialogDescription>
              </DialogHeader>
              <DiffView oldText={diffVer?.template_text ?? ""} newText={versions[0]?.template_text ?? ""} />
            </DialogContent>
          </Dialog>
        </div>
      </TabsContent>

      {/* Wrap-up prompts */}
      <TabsContent value="wrapup" className="min-h-0 flex-1 overflow-y-auto px-4 pb-4">
        <div className="grid gap-3">
          <p className="text-muted-foreground text-xs leading-relaxed">
            When this agent is stopped because of a <b>timeout</b> or <b>turn limit</b> the system injects this wrap-up
            prompt for a final run: persist identified content that has not been saved, then output a brief summary.
            Leave blank to use the built-in default.
          </p>
          <div className="flex items-center gap-2">
            <Label className="text-xs">Wrap-up prompt</Label>
            {wrapup.trim() ? (
              <Badge variant="secondary" className="px-1.5 py-0">
                Custom
              </Badge>
            ) : (
              <Badge variant="outline" className="px-1.5 py-0">
                Use built-in default
              </Badge>
            )}
          </div>
          <Textarea
            className="font-mono text-xs"
            rows={10}
            value={wrapup}
            placeholder={wrapupDefault || "Leave blank to use the built-in default wrap-up prompt"}
            onChange={(e) => setWrapup(e.target.value)}
          />
          <div className="grid gap-1.5">
            <Label htmlFor="wrapup-turns" className="text-xs">
              Wrap-up turns (maximum during wrap-up; 0 = built-in default: {wrapupTurnsDefault} turns)
            </Label>
            <Input
              id="wrapup-turns"
              type="number"
              min={0}
              className="h-8 w-32"
              value={wrapupTurns}
              onChange={(e) => setWrapupTurns(e.target.value)}
            />
          </div>
          <div className="flex gap-2">
            <Button size="sm" onClick={saveWrapup}>
              Save
            </Button>
            <Button size="sm" variant="outline" onClick={resetWrapup}>
              Restore defaults
            </Button>
          </div>
          {wrapupDefault && (
            <>
              <Separator />
              <div className="grid gap-1.5">
                <Label className="text-muted-foreground text-xs">Built-in default (read-only reference)</Label>
                <pre className="max-h-40 overflow-y-auto whitespace-pre-wrap rounded-md border bg-muted/30 p-2 text-muted-foreground text-xs">
                  {wrapupDefault}
                </pre>
              </div>
            </>
          )}

          {ttSupported && (
            <>
              <Separator className="my-2" />
              <p className="text-muted-foreground text-xs leading-relaxed">
                <b>Task timeout wrap-up</b> (separate from the per-run wrap-up above, with <b>independent prompts</b>):
                injected when <b>the entire task</b> reaches its timeout and is about to end. Its meaning often differs
                from per-run wrap-up (e.g. a planner may continue planning after a run, but must stop and give its final
                decision at task timeout). Leave blank for the built-in default.
              </p>
              <div className="flex items-center gap-2">
                <Label className="text-xs">Task timeout wrap-up prompt</Label>
                {ttWrapup.trim() ? (
                  <Badge variant="secondary" className="px-1.5 py-0">
                    Custom
                  </Badge>
                ) : (
                  <Badge variant="outline" className="px-1.5 py-0">
                    Use built-in default
                  </Badge>
                )}
              </div>
              <Textarea
                className="font-mono text-xs"
                rows={10}
                value={ttWrapup}
                placeholder={ttWrapupDefault || "Leave blank to use the built-in default task timeout wrap-up prompt"}
                onChange={(e) => setTtWrapup(e.target.value)}
              />
              <div className="grid gap-1.5">
                <Label htmlFor="tt-turns" className="text-xs">
                  Wrap-up turns (0 = built-in default: {ttTurnsDefault} turns)
                </Label>
                <Input
                  id="tt-turns"
                  type="number"
                  min={0}
                  className="h-8 w-32"
                  value={ttTurns}
                  onChange={(e) => setTtTurns(e.target.value)}
                />
              </div>
              <div className="flex gap-2">
                <Button size="sm" onClick={saveTaskTimeoutWrapup}>
                  Save
                </Button>
                <Button size="sm" variant="outline" onClick={resetTaskTimeoutWrapup}>
                  Restore defaults
                </Button>
              </div>
              {ttWrapupDefault && (
                <div className="grid gap-1.5">
                  <Label className="text-muted-foreground text-xs">Built-in default (read-only reference)</Label>
                  <pre className="max-h-40 overflow-y-auto whitespace-pre-wrap rounded-md border bg-muted/30 p-2 text-muted-foreground text-xs">
                    {ttWrapupDefault}
                  </pre>
                </div>
              )}
            </>
          )}
        </div>
      </TabsContent>

      {/* MCP visibility */}
      <TabsContent value="mcp" className="min-h-0 flex-1 overflow-y-auto px-4 pb-4">
        <p className="mb-3 text-muted-foreground text-xs">Select MCP servers visible to this agent.</p>
        <div className="grid gap-2">
          {mcp.map((m) => {
            const checkboxId = `agent-mcp-${m.id}`;
            return (
              <label key={m.id} htmlFor={checkboxId} className="flex items-center gap-2 rounded-md border p-2 text-sm">
                <Checkbox id={checkboxId} checked={mcpVisible.includes(m.id)} onCheckedChange={() => toggleMcp(m.id)} />
                {m.name}
                <span className="ml-auto text-muted-foreground text-xs">{m.transport}</span>
              </label>
            );
          })}
          {mcp.length === 0 && <span className="text-muted-foreground text-xs">(No MCP servers yet)</span>}
        </div>
      </TabsContent>

      {/* Skill visibility */}
      <TabsContent value="skill" className="min-h-0 flex-1 overflow-y-auto px-4 pb-4">
        <p className="mb-3 text-muted-foreground text-xs">Select skills visible to this agent.</p>
        <div className="grid gap-2">
          {skills.map((s) => {
            const checkboxId = `agent-skill-${encodeURIComponent(s.name)}`;
            return (
              <label
                key={s.name}
                htmlFor={checkboxId}
                className="flex items-center gap-2 rounded-md border p-2 text-sm"
              >
                <Checkbox
                  id={checkboxId}
                  checked={skillVisible.includes(s.name)}
                  onCheckedChange={() => toggleSkill(s.name)}
                />
                <span className="font-mono text-xs">{s.name}</span>
                {s.description && (
                  <span className="ml-auto truncate text-muted-foreground text-xs">{s.description}</span>
                )}
              </label>
            );
          })}
          {skills.length === 0 && <span className="text-muted-foreground text-xs">(No skills yet)</span>}
        </div>
      </TabsContent>

      {/* Tool bindings */}
      <TabsContent value="tools" className="min-h-0 flex-1 overflow-y-auto px-4 pb-4">
        <p className="mb-3 text-muted-foreground text-xs">Select built-in tools to bind to this agent.</p>
        <div className="grid gap-2">
          {tools.map((t) => {
            const isTraffic = TRAFFIC_TOOL_KEYS.has(t.key);
            const gated = isTraffic && !captureOn; // Traffic tools require Traffic capture.
            const checkboxId = `agent-tool-${encodeURIComponent(t.key)}`;
            return (
              <label
                key={t.key}
                htmlFor={checkboxId}
                className={cn("flex items-center gap-2 rounded-md border p-2 text-sm", gated && "opacity-60")}
              >
                <Checkbox
                  id={checkboxId}
                  checked={t.agents.includes(agentKey)}
                  disabled={gated}
                  onCheckedChange={() => toggleTool(t)}
                />
                <span className="font-mono text-xs">{t.key}</span>
                {isTraffic && (
                  <Badge variant="secondary" className="px-1 py-0 text-[9px]">
                    Traffic
                  </Badge>
                )}
                {!t.enabled && (
                  <Badge variant="outline" className="px-1 py-0 text-[9px] text-destructive">
                    Disabled
                  </Badge>
                )}
                {gated ? (
                  <span className="ml-auto text-muted-foreground text-xs">Requires traffic capture</span>
                ) : (
                  t.description && (
                    <span className="ml-auto line-clamp-1 max-w-[55%] text-muted-foreground text-xs">
                      {t.description}
                    </span>
                  )
                )}
              </label>
            );
          })}
          {tools.length === 0 && <span className="text-muted-foreground text-xs">(No tools yet)</span>}
        </div>
      </TabsContent>

      {/* Triggers (P3, custom agents only) */}
      {isCustom && (
        <TabsContent value="triggers" className="min-h-0 flex-1 overflow-y-auto px-4 pb-4">
          <AgentTriggersTab agentKey={agentKey} agent={detail?.agent} />
        </TabsContent>
      )}
    </Tabs>
  );
}

// ---------- Diff helpers ----------

type DiffLine = { type: "same" | "add" | "del"; text: string };

function computeDiff(oldText: string, newText: string): DiffLine[] {
  const a = oldText.split("\n");
  const b = newText.split("\n");
  const m = a.length;
  const n = b.length;
  const dp: number[][] = Array.from({ length: m + 1 }, () => new Array(n + 1).fill(0));
  for (let i = 1; i <= m; i++)
    for (let j = 1; j <= n; j++)
      dp[i][j] = a[i - 1] === b[j - 1] ? dp[i - 1][j - 1] + 1 : Math.max(dp[i - 1][j], dp[i][j - 1]);
  const result: DiffLine[] = [];
  let i = m;
  let j = n;
  while (i > 0 || j > 0) {
    if (i > 0 && j > 0 && a[i - 1] === b[j - 1]) {
      result.unshift({ type: "same", text: a[i - 1] });
      i--;
      j--;
    } else if (j > 0 && (i === 0 || dp[i][j - 1] >= dp[i - 1][j])) {
      result.unshift({ type: "add", text: b[j - 1] });
      j--;
    } else {
      result.unshift({ type: "del", text: a[i - 1] });
      i--;
    }
  }
  return result;
}

function DiffView({ oldText, newText }: { oldText: string; newText: string }) {
  const lines = React.useMemo(() => computeDiff(oldText, newText), [oldText, newText]);
  return (
    <pre className="max-h-[60vh] overflow-auto rounded-md border bg-muted/30 p-2 font-mono text-xs leading-5">
      {lines.map((l, idx) => (
        <div
          key={idx}
          className={cn(
            "whitespace-pre-wrap px-1",
            l.type === "del" && "bg-red-500/15 text-red-700 dark:text-red-400",
            l.type === "add" && "bg-green-500/15 text-green-700 dark:text-green-400",
            l.type === "same" && "text-muted-foreground",
          )}
        >
          <span className="mr-1 select-none opacity-50">{l.type === "del" ? "-" : l.type === "add" ? "+" : " "}</span>
          {l.text}
        </div>
      ))}
    </pre>
  );
}

// AgentTriggersTab manages a custom agent's P3 triggers: list + add + delete.
// Each trigger fires (schedule/finding/goal/task timeout/tool call; multiple conditions allowed) → a new conversation runs
// in parallel with the base user message + auto context appended by the backend.
function AgentTriggersTab({ agentKey, agent }: { agentKey: string; agent?: Agent }) {
  const [triggers, setTriggers] = React.useState<AgentTrigger[]>([]);
  const [tools, setTools] = React.useState<Tool[]>([]);
  // Per-agent trigger processing policy; initialized from agent details and saved on change.
  const [runMode, setRunMode] = React.useState<"serial" | "parallel">(agent?.trigger_run_mode ?? "serial");
  const [mergeMode, setMergeMode] = React.useState<"by_task" | "all" | "none">(agent?.trigger_merge_mode ?? "all");
  const [maxParallel, setMaxParallel] = React.useState(String(agent?.trigger_max_parallel ?? 5));
  React.useEffect(() => {
    setRunMode(agent?.trigger_run_mode ?? "serial");
    setMergeMode(agent?.trigger_merge_mode ?? "all");
    setMaxParallel(String(agent?.trigger_max_parallel ?? 5));
  }, [agent?.trigger_run_mode, agent?.trigger_merge_mode, agent?.trigger_max_parallel]);

  async function saveBehavior(patch: {
    trigger_run_mode?: "serial" | "parallel";
    trigger_merge_mode?: "by_task" | "all" | "none";
    trigger_max_parallel?: number;
  }) {
    try {
      await api.saveAgentConfig(agentKey, patch);
    } catch (e) {
      toast.error(`Failed to save policy: ${(e as Error).message}`);
    }
  }
  const [onInterval, setOnInterval] = React.useState(false);
  const [intervalSec, setIntervalSec] = React.useState("60");
  const [onFinding, setOnFinding] = React.useState(false);
  const [onGoalMet, setOnGoalMet] = React.useState(false);
  const [onTaskTimeout, setOnTaskTimeout] = React.useState(false);
  const [onToolCall, setOnToolCall] = React.useState(false);
  const [onTaskCreate, setOnTaskCreate] = React.useState(false);
  const [intervalMsg, setIntervalMsg] = React.useState("");
  const [findingMsg, setFindingMsg] = React.useState("");
  const [goalMsg, setGoalMsg] = React.useState("");
  const [taskTimeoutMsg, setTaskTimeoutMsg] = React.useState("");
  const [toolCallMsg, setToolCallMsg] = React.useState("");
  const [taskCreateMsg, setTaskCreateMsg] = React.useState("");
  const [toolNames, setToolNames] = React.useState<string[]>([]);
  const [saving, setSaving] = React.useState(false);
  // null = create mode; otherwise edit the trigger with this ID.
  const [editingId, setEditingId] = React.useState<number | null>(null);

  const reload = React.useCallback(() => {
    api
      .agentTriggers(agentKey)
      .then(setTriggers)
      .catch(() => setTriggers([]));
  }, [agentKey]);
  React.useEffect(() => {
    reload();
  }, [reload]);
  React.useEffect(() => {
    api
      .tools()
      .then(setTools)
      .catch(() => setTools([]));
  }, []);

  function toggleTool(key: string) {
    setToolNames((prev) => (prev.includes(key) ? prev.filter((k) => k !== key) : [...prev, key]));
  }

  // resetForm clears the form and returns to create mode.
  function resetForm() {
    setEditingId(null);
    setOnInterval(false);
    setIntervalSec("60");
    setOnFinding(false);
    setOnGoalMet(false);
    setOnTaskTimeout(false);
    setOnToolCall(false);
    setOnTaskCreate(false);
    setIntervalMsg("");
    setFindingMsg("");
    setGoalMsg("");
    setTaskTimeoutMsg("");
    setToolCallMsg("");
    setTaskCreateMsg("");
    setToolNames([]);
  }

  // startEdit populates the form from an existing trigger and enters edit mode.
  function startEdit(t: AgentTrigger) {
    setEditingId(t.id);
    setOnInterval(t.interval_sec > 0);
    setIntervalSec(t.interval_sec > 0 ? String(t.interval_sec) : "60");
    setOnFinding(t.on_finding);
    setOnGoalMet(t.on_goal_met);
    setOnTaskTimeout(t.on_task_timeout);
    setOnToolCall(t.on_tool_call);
    setOnTaskCreate(t.on_task_create);
    setIntervalMsg(t.interval_message);
    setFindingMsg(t.finding_message);
    setGoalMsg(t.goal_message);
    setTaskTimeoutMsg(t.task_timeout_message);
    setToolCallMsg(t.tool_call_message);
    setTaskCreateMsg(t.task_create_message);
    setToolNames(t.tool_names);
  }

  // submit creates or updates based on editingId; edits preserve the trigger's enabled state.
  async function submit() {
    const n = onInterval ? Math.max(1, Math.floor(Number(intervalSec) || 0)) : 0;
    if (n === 0 && !onFinding && !onGoalMet && !onTaskTimeout && !onToolCall && !onTaskCreate) {
      toast.error("Select at least one trigger condition");
      return;
    }
    if (onToolCall && toolNames.length === 0) {
      toast.error("Select at least one tool for tool-call triggers");
      return;
    }
    const body = {
      interval_sec: n,
      on_finding: onFinding,
      on_goal_met: onGoalMet,
      on_task_timeout: onTaskTimeout,
      on_tool_call: onToolCall,
      on_task_create: onTaskCreate,
      interval_message: intervalMsg.trim(),
      finding_message: findingMsg.trim(),
      goal_message: goalMsg.trim(),
      task_timeout_message: taskTimeoutMsg.trim(),
      tool_call_message: toolCallMsg.trim(),
      task_create_message: taskCreateMsg.trim(),
      tool_names: onToolCall ? toolNames : [],
    };
    setSaving(true);
    try {
      if (editingId != null) {
        const cur = triggers.find((x) => x.id === editingId);
        await api.updateTrigger(editingId, { ...body, enabled: cur?.enabled ?? true });
        toast.success("Changes saved");
      } else {
        await api.createTrigger(agentKey, { ...body, enabled: true });
        toast.success("Trigger added");
      }
      resetForm();
      reload();
    } catch (e) {
      toast.error((editingId != null ? "Save failed: " : "Add failed: ") + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }
  async function toggleEnabled(t: AgentTrigger) {
    try {
      await api.updateTrigger(t.id, {
        enabled: !t.enabled,
        interval_sec: t.interval_sec,
        on_finding: t.on_finding,
        on_goal_met: t.on_goal_met,
        on_task_timeout: t.on_task_timeout,
        on_tool_call: t.on_tool_call,
        on_task_create: t.on_task_create,
        interval_message: t.interval_message,
        finding_message: t.finding_message,
        goal_message: t.goal_message,
        task_timeout_message: t.task_timeout_message,
        tool_call_message: t.tool_call_message,
        task_create_message: t.task_create_message,
        tool_names: t.tool_names,
      });
      reload();
    } catch (e) {
      toast.error(`Save failed: ${(e as Error).message}`);
    }
  }
  async function del(id: number) {
    try {
      await api.deleteTrigger(id);
      if (editingId === id) resetForm();
      reload();
    } catch (e) {
      toast.error(`Delete failed: ${(e as Error).message}`);
    }
  }

  function condLabel(t: AgentTrigger): string {
    const parts: string[] = [];
    if (t.interval_sec > 0) parts.push(`Every ${t.interval_sec}s`);
    if (t.on_finding) parts.push("Finding discovered");
    if (t.on_goal_met) parts.push("Goal reached");
    if (t.on_task_timeout) parts.push("Task timeout");
    if (t.on_tool_call) parts.push(`Tool call (${t.tool_names.length})`);
    if (t.on_task_create) parts.push("Task created");
    return parts.join(" · ") || "(No conditions)";
  }

  return (
    <div className="grid gap-4">
      <p className="text-muted-foreground text-xs">
        Triggers run this custom agent automatically. Each trigger starts <b>a new session run</b> (visible in Chat).
        Select multiple conditions as needed; the system appends the trigger reason and relevant task/finding/goal
        details to your base message.
      </p>

      {/* Trigger processing policy */}
      <div className="grid gap-3 rounded-md border p-3">
        <Label className="text-muted-foreground text-xs">Trigger processing policy (queue and merge behavior)</Label>
        <div className="flex flex-wrap items-center gap-4">
          <div className="grid gap-1">
            <Label className="text-xs">Run mode</Label>
            <Select
              value={runMode}
              onValueChange={(v) => {
                const rm = v as "serial" | "parallel";
                setRunMode(rm);
                void saveBehavior({ trigger_run_mode: rm });
              }}
            >
              <SelectTrigger size="sm" className="h-8 w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent position="popper">
                <SelectItem value="serial">Serial (queued, one at a time)</SelectItem>
                <SelectItem value="parallel">Parallel (concurrent sessions)</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div className="grid gap-1">
            <Label className="text-xs">Merge mode</Label>
            <Select
              value={mergeMode}
              disabled={runMode === "parallel"}
              onValueChange={(v) => {
                const mm = v as "by_task" | "all" | "none";
                setMergeMode(mm);
                void saveBehavior({ trigger_merge_mode: mm });
              }}
            >
              <SelectTrigger size="sm" className="h-8 w-44">
                <SelectValue />
              </SelectTrigger>
              <SelectContent position="popper">
                <SelectItem value="by_task">Merge by task</SelectItem>
                <SelectItem value="all">Merge all</SelectItem>
                <SelectItem value="none">Do not merge</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {runMode === "parallel" && (
            <div className="grid gap-1">
              <Label htmlFor="tr-maxpar" className="text-xs">
                Maximum concurrency (0 = unlimited)
              </Label>
              <Input
                id="tr-maxpar"
                type="number"
                min={0}
                className="h-8 w-28"
                value={maxParallel}
                onChange={(e) => setMaxParallel(e.target.value)}
                onBlur={() => {
                  const n = Math.max(0, Math.floor(Number(maxParallel) || 0));
                  setMaxParallel(String(n));
                  void saveBehavior({ trigger_max_parallel: n });
                }}
              />
            </div>
          )}
        </div>
        <p className="text-muted-foreground text-xs">
          {runMode === "parallel"
            ? "Parallel: each trigger immediately starts its own session with no merging; triggers beyond the concurrency limit wait in the queue."
            : mergeMode === "by_task"
              ? "Serial · by task: one run per agent at a time; queued events for the same task merge into one session."
              : mergeMode === "all"
                ? "Serial · all: one run per agent at a time; all pending triggers merge into one session when dequeued."
                : "Serial · no merge: one run per agent at a time; each trigger gets its own session."}
        </p>
      </div>

      {/* Create / edit trigger */}
      <div className="grid gap-3 rounded-md border p-3">
        <Label className="text-muted-foreground text-xs">
          {editingId != null
            ? `Edit trigger #${editingId} (click Save changes when finished)`
            : "New trigger (each condition can have its own user message)"}
        </Label>

        {/* Schedule */}
        <div className="grid gap-1.5">
          <label htmlFor="trigger-schedule" className="flex items-center gap-2 text-sm">
            <Checkbox id="trigger-schedule" checked={onInterval} onCheckedChange={(v) => setOnInterval(!!v)} />
            Scheduled trigger
          </label>
          {onInterval && (
            <div className="grid gap-1.5">
              <div className="flex items-center gap-2">
                <Label htmlFor="tr-interval" className="text-xs">
                  Every
                </Label>
                <Input
                  id="tr-interval"
                  type="number"
                  min={1}
                  className="h-8 w-24"
                  value={intervalSec}
                  onChange={(e) => setIntervalSec(e.target.value)}
                />
                <span className="text-muted-foreground text-xs">seconds</span>
              </div>
              <Textarea
                className="text-xs"
                rows={2}
                value={intervalMsg}
                placeholder="Message for scheduled runs, e.g. inspect all tasks"
                onChange={(e) => setIntervalMsg(e.target.value)}
              />
            </div>
          )}
        </div>

        {/* finding */}
        <div className="grid gap-1.5">
          <label htmlFor="trigger-finding" className="flex items-center gap-2 text-sm">
            <Checkbox id="trigger-finding" checked={onFinding} onCheckedChange={(v) => setOnFinding(!!v)} /> Trigger
            when a finding is discovered
          </label>
          {onFinding && (
            <Textarea
              className="text-xs"
              rows={2}
              value={findingMsg}
              placeholder="Message when a finding is discovered (task and finding details are appended automatically)"
              onChange={(e) => setFindingMsg(e.target.value)}
            />
          )}
        </div>

        {/* Goal reached */}
        <div className="grid gap-1.5">
          <label htmlFor="trigger-goal" className="flex items-center gap-2 text-sm">
            <Checkbox id="trigger-goal" checked={onGoalMet} onCheckedChange={(v) => setOnGoalMet(!!v)} /> Trigger when a
            goal is reached
          </label>
          {onGoalMet && (
            <Textarea
              className="text-xs"
              rows={2}
              value={goalMsg}
              placeholder="Message when a goal is reached (task and completed goal details are appended automatically)"
              onChange={(e) => setGoalMsg(e.target.value)}
            />
          )}
        </div>

        {/* Task timeout */}
        <div className="grid gap-1.5">
          <label htmlFor="trigger-timeout" className="flex items-center gap-2 text-sm">
            <Checkbox id="trigger-timeout" checked={onTaskTimeout} onCheckedChange={(v) => setOnTaskTimeout(!!v)} />
            Trigger on task timeout
          </label>
          {onTaskTimeout && (
            <Textarea
              className="text-xs"
              rows={2}
              value={taskTimeoutMsg}
              placeholder="Message on task timeout (task ID and goals are appended automatically)"
              onChange={(e) => setTaskTimeoutMsg(e.target.value)}
            />
          )}
        </div>

        {/* Tool call */}
        <div className="grid gap-1.5">
          <label htmlFor="trigger-tool-call" className="flex items-center gap-2 text-sm">
            <Checkbox id="trigger-tool-call" checked={onToolCall} onCheckedChange={(v) => setOnToolCall(!!v)} />
            Trigger on tool calls
          </label>
          {onToolCall && (
            <div className="grid gap-1.5">
              <div className="text-muted-foreground text-xs">
                Select at least one tool to watch. A trigger fires each time these tools <b>finish a call</b> during
                task execution. Selected: {toolNames.length} tools.
              </div>
              <div className="max-h-40 overflow-y-auto rounded-md border p-2">
                {tools.length === 0 && <span className="text-muted-foreground text-xs">(Tool list is empty)</span>}
                <div className="grid gap-1">
                  {tools.map((tool) => {
                    const checkboxId = `trigger-tool-${encodeURIComponent(tool.key)}`;
                    return (
                      <label key={tool.key} htmlFor={checkboxId} className="flex items-start gap-2 text-xs">
                        <Checkbox
                          id={checkboxId}
                          className="mt-0.5"
                          checked={toolNames.includes(tool.key)}
                          onCheckedChange={() => toggleTool(tool.key)}
                        />
                        <span className="min-w-0">
                          <span className="font-medium">{tool.key}</span>
                          {tool.description && (
                            <span className="line-clamp-1 text-muted-foreground"> {tool.description}</span>
                          )}
                        </span>
                      </label>
                    );
                  })}
                </div>
              </div>
              <Textarea
                className="text-xs"
                rows={2}
                value={toolCallMsg}
                placeholder="Message on tool calls (task, tool input, and output are appended automatically)"
                onChange={(e) => setToolCallMsg(e.target.value)}
              />
            </div>
          )}
        </div>

        {/* Task created */}
        <div className="grid gap-1.5">
          <label htmlFor="trigger-task-create" className="flex items-center gap-2 text-sm">
            <Checkbox id="trigger-task-create" checked={onTaskCreate} onCheckedChange={(v) => setOnTaskCreate(!!v)} />
            Trigger when a task is created
          </label>
          {onTaskCreate && (
            <Textarea
              className="text-xs"
              rows={2}
              value={taskCreateMsg}
              placeholder="Message when a task is created (task ID and goals are appended automatically)"
              onChange={(e) => setTaskCreateMsg(e.target.value)}
            />
          )}
        </div>

        <div className="flex items-center gap-2">
          <Button size="sm" onClick={submit} disabled={saving}>
            <SaveIcon /> {editingId != null ? "Save changes" : "Add trigger"}
          </Button>
          {editingId != null && (
            <Button size="sm" variant="ghost" onClick={resetForm} disabled={saving}>
              <XIcon /> Cancel editing
            </Button>
          )}
        </div>
      </div>

      {/* Existing triggers */}
      <div className="grid gap-2">
        <Label className="text-muted-foreground text-xs">Existing triggers</Label>
        {triggers.length === 0 && <span className="text-muted-foreground text-xs">(None yet)</span>}
        {triggers.map((t) => (
          <div
            key={t.id}
            className={cn(
              "flex items-start gap-2 rounded-md border p-2 text-sm",
              editingId === t.id && "border-primary bg-primary/5",
            )}
          >
            <Switch checked={t.enabled} onCheckedChange={() => toggleEnabled(t)} className="mt-0.5" />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-1.5">
                <span className="font-medium">{condLabel(t)}</span>
                {!t.enabled && (
                  <Badge variant="outline" className="px-1 py-0 text-[9px] text-destructive">
                    Disabled
                  </Badge>
                )}
              </div>
              <div className="grid gap-0.5 text-muted-foreground text-xs">
                {t.interval_sec > 0 && t.interval_message && (
                  <div className="line-clamp-1">Schedule: {t.interval_message}</div>
                )}
                {t.on_finding && t.finding_message && <div className="line-clamp-1">finding: {t.finding_message}</div>}
                {t.on_goal_met && t.goal_message && <div className="line-clamp-1">Goal: {t.goal_message}</div>}
                {t.on_task_timeout && t.task_timeout_message && (
                  <div className="line-clamp-1">Timeout: {t.task_timeout_message}</div>
                )}
                {t.on_task_create && t.task_create_message && (
                  <div className="line-clamp-1">Task created: {t.task_create_message}</div>
                )}
                {t.on_tool_call && (
                  <>
                    <div className="line-clamp-1">Tools: {t.tool_names.join(", ") || "(None selected)"}</div>
                    {t.tool_call_message && <div className="line-clamp-1">Message: {t.tool_call_message}</div>}
                  </>
                )}
              </div>
            </div>
            <Button
              variant="ghost"
              size="icon-sm"
              className="text-muted-foreground hover:text-foreground"
              onClick={() => startEdit(t)}
              title="Edit"
            >
              <PencilIcon className="size-3.5" />
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              className="text-muted-foreground hover:text-destructive"
              onClick={() => del(t.id)}
              title="Delete"
            >
              <Trash2Icon className="size-3.5" />
            </Button>
          </div>
        ))}
      </div>
    </div>
  );
}
