"use client";

// Shared LLM retry controls: attempt counts and intervals for five layers.
//
// Inner to outer: SDK connection → SDK empty response → same-provider safe window → rotation circuit breaker → intent rerun.
// The first three follow the endpoint and can override global defaults per profile; the last two are process-wide.
//
// All inputs use blank = unconfigured semantics, matching backend db.RetryRule:
// Attempts: blank/0 = built-in default | -1 = disable this layer | >0 = specified count
// Interval: blank/0 = the layer's exponential backoff | >0 = fixed interval in milliseconds

import * as React from "react";

import { Loader2Icon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { LLMRetryOverride, LLMRetryPolicy, LLMRetryRule } from "@/lib/types";

export const ZERO_RULE: LLMRetryRule = { attempts: 0, interval_ms: 0 };
export const ZERO_OVERRIDE: LLMRetryOverride = {
  connect: ZERO_RULE,
  empty: ZERO_RULE,
  stream: ZERO_RULE,
};
const ZERO_POLICY: LLMRetryPolicy = {
  ...ZERO_OVERRIDE,
  breaker: ZERO_RULE,
  intent: ZERO_RULE,
};

type LayerMeta = {
  title: string;
  /** Where this retry happens and which component performs it */
  where: string;
  /** Errors triggering this layer, including explicit status codes */
  trigger: string;
  /** Similar errors excluded from this layer, to explain why its controls may not affect them */
  skips?: string;
  desc: string;
  attemptsLabel: string;
  /** Default attempts used as a placeholder */
  defAttempts: number;
  /** Default interval strategy used as a placeholder */
  defInterval: string;
  /** Meaning of -1 attempts */
  offHint: string;
};

export const RETRY_LAYERS = {
  connect: {
    title: "Connection retries",
    where: "SDK · before HTTP 200",
    trigger:
      "Connection failures before HTTP 200: connection resets, read/write timeouts, DNS failures, other network errors, and HTTP 408, 429, 500, 502, 503, 504.",
    skips:
      "Other status codes (400 / 401 / 403 / 404 / 413 / 422, etc.) are deterministic rejections. Retrying will fail again, so the error propagates immediately.",
    desc: "Resend the same request unchanged. Once streaming begins (HTTP 200 received), mid-stream disconnects belong to another layer.",
    attemptsLabel: "Retry attempts",
    defAttempts: 3,
    defInterval: "0.5s→1s→2s exponential (8s cap)",
    offHint: "-1 = no retries; propagate failures immediately",
  },
  empty: {
    title: "Empty response retries",
    where: "SDK · openai format only",
    trigger:
      "HTTP 200 with a normal stop finish_reason, but no content blocks in the entire response. Empty gateway frames, dropped thinking fields, or sampling glitches can cause this.",
    skips:
      "Excludes empty content caused by max_tokens truncation. Increase the output limit; resending would hit it again.",
    desc: "Resend the entire prompt. This is costly with long contexts, so keep the retry count low.",
    attemptsLabel: "Retry attempts",
    defAttempts: 2,
    defInterval: "0.5s→1s→2s exponential (8s cap)",
    offHint: "-1 = return the empty response unchanged",
  },
  stream: {
    title: "Same-provider safe-window retries",
    where: "Application · before output is delivered",
    trigger:
      "Failures after the stream starts (HTTP 200): mid-stream disconnects, provider overloaded errors, and in-stream 429 / 5xx events, before any token reaches the caller.",
    skips:
      "No retries for exhausted quota (402 / insufficient_quota; handled by profile rotation), excessive context (413 / context length; handled by compression), or deterministic 400 / 401 / 403 / 404 / 422 rejections.",
    desc: "Replay the same request on the same profile. Since no output has been delivered, replay does not duplicate model output or tool execution.",
    attemptsLabel: "Retry attempts",
    defAttempts: 2,
    defInterval: "0.5s→1s exponential (4s cap)",
    offHint: "-1 = pass stream failures directly to the outer intent rerun layer",
  },
  breaker: {
    title: "Rotation circuit breaker",
    where: "Application · one global, process-wide setting",
    trigger:
      "Transient failures (429, 5xx, network errors) trip the breaker after consecutive failures reach the threshold. Deterministic failures such as insufficient balance (402), invalid keys (401 / 403), or missing models (404) trip it immediately.",
    skips: "A single success resets the counter, so occasional failures do not accumulate indefinitely.",
    desc: "A tripped profile enters cooldown and rotation skips it until recovery. State is stored in the database and survives restarts.",
    attemptsLabel: "Consecutive failures before tripping",
    defAttempts: 3,
    defInterval: "1min→5min→30min steps",
    offHint: "-1 = transient failures never trip the breaker (deterministic failures still do)",
  },
  intent: {
    title: "Intent reruns",
    where: "Application · one global, process-wide setting",
    trigger:
      "The earlier layers could not recover: a worker ends with model_error after inner retries are exhausted or the stream disconnects after output delivery, when replay is unsafe and the whole intent must restart.",
    skips:
      "Exhausted quota is already handled by profile rotation. Reruns yield immediately when a task pauses, terminates, or starts wrapping up, without waiting for backoff.",
    desc: "Restart the entire intent. This outermost layer multiplies all inner retry attempts again for each rerun.",
    attemptsLabel: "Rerun attempts",
    defAttempts: 2,
    defInterval: "Fixed 3s",
    offHint: "-1 = no reruns; mark the intent blocked",
  },
} satisfies Record<string, LayerMeta>;

type LayerKey = keyof typeof RETRY_LAYERS;

/** Readable milliseconds shown beside the input to avoid counting zeros. */
function humanMs(ms: number) {
  if (!Number.isFinite(ms) || ms <= 0) return "";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
  return `${Number((ms / 60_000).toFixed(2))}min`;
}

/** Controlled numeric input: empty string ↔ 0; preserve intermediate local text ("-", "1e") without notifying the parent. */
function NumField({
  id,
  value,
  onChange,
  placeholder,
  min,
}: {
  id: string;
  value: number;
  onChange: (n: number) => void;
  placeholder: string;
  min: number;
}) {
  const [text, setText] = React.useState(value === 0 ? "" : String(value));
  // Follow whole-value changes from the parent (policy loads or profile switches); local typing skips this
  // because the parsed local text already equals value.
  React.useEffect(() => {
    const incoming = value === 0 ? "" : String(value);
    setText((cur) => (Number(cur || 0) === value ? cur : incoming));
  }, [value]);
  return (
    <Input
      id={id}
      type="number"
      min={min}
      className="w-28 shrink-0"
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        const n = Number(e.target.value);
        onChange(e.target.value.trim() === "" || !Number.isFinite(n) ? 0 : Math.trunc(n));
      }}
    />
  );
}

/** Two controls for one retry layer; idPrefix keeps label htmlFor unique when repeated on a page. */
export function RetryRuleFields({
  layer,
  idPrefix,
  value,
  onChange,
  compact,
}: {
  layer: LayerKey;
  idPrefix: string;
  value: LLMRetryRule;
  onChange: (r: LLMRetryRule) => void;
  /** true = compact profile drawer: omit expanded explanations and retain only triggering errors */
  compact?: boolean;
}) {
  const meta = RETRY_LAYERS[layer];
  const human = humanMs(value.interval_ms);
  return (
    <div className={compact ? "grid gap-2" : "grid gap-3 rounded-lg border p-3"}>
      <div className="grid gap-0.5">
        <div className="flex flex-wrap items-baseline gap-2">
          <Label className="text-sm">{meta.title}</Label>
          <span className="text-muted-foreground text-xs">{meta.where}</span>
        </div>
        {/* List specific triggering status codes so users can tell whether these settings apply to an error. */}
        <p className="text-muted-foreground text-xs">
          <span className="font-medium text-foreground">Triggers</span>: {meta.trigger}
        </p>
        {!compact && meta.skips && (
          <p className="text-muted-foreground text-xs">
            <span className="font-medium text-foreground">Excluded</span>: {meta.skips}
          </p>
        )}
        {!compact && <p className="text-muted-foreground text-xs">{meta.desc}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-n`} className="text-muted-foreground text-xs">
            {meta.attemptsLabel}
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-n`}
            min={-1}
            value={value.attempts}
            placeholder={`Default ${meta.defAttempts}`}
            onChange={(n) => onChange({ ...value, attempts: n })}
          />
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-ms`} className="text-muted-foreground text-xs">
            Interval (ms)
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-ms`}
            min={0}
            value={value.interval_ms}
            placeholder="Default backoff"
            onChange={(n) => onChange({ ...value, interval_ms: n })}
          />
          <span className="text-muted-foreground text-xs">{human ? `Fixed ${human}` : meta.defInterval}</span>
        </div>
      </div>
      {!compact && <p className="text-muted-foreground text-xs">Leave blank for defaults; {meta.offHint}.</p>}
    </div>
  );
}

/** Per-profile overrides for the three endpoint-specific retry layers. */
export function ProfileRetryFields({
  value,
  onChange,
}: {
  value: LLMRetryOverride;
  onChange: (o: LLMRetryOverride) => void;
}) {
  return (
    <div className="grid gap-3 rounded-lg border p-3">
      <div className="grid gap-0.5">
        <Label className="text-sm">Retry overrides</Label>
        <p className="text-muted-foreground text-xs">
          Overrides the global Retry and backoff defaults for this profile only. Leave each field blank to follow global
          settings; -1 attempts disables that retry layer. A specified interval replaces exponential backoff with a
          fixed interval. Circuit breakers and intent reruns are process-wide and configured globally.
        </p>
      </div>
      {(["connect", "empty", "stream"] as const).map((k) => (
        <div key={k} className="border-t pt-3 first:border-t-0 first:pt-0">
          <RetryRuleFields
            compact
            layer={k}
            idPrefix="pf"
            value={value[k]}
            onChange={(r) => onChange({ ...value, [k]: r })}
          />
        </div>
      ))}
    </div>
  );
}

/** Retry and backoff tab: global defaults for all five layers. */
export function RetryPolicyPanel() {
  const [policy, setPolicy] = React.useState<LLMRetryPolicy>(ZERO_POLICY);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const p = await api.llmRetryPolicy();
      setPolicy({ ...ZERO_POLICY, ...p });
    } catch (e) {
      toast.error(`Failed to load retry policy: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function save() {
    if (saving) return;
    setSaving(true);
    try {
      // The backend clamps out-of-range values; display the returned values so the UI matches stored settings.
      const saved = await api.saveLLMRetryPolicy(policy);
      setPolicy({ ...ZERO_POLICY, ...saved });
      toast.success("Saved; effective immediately (the current call keeps its previous settings)");
    } catch (e) {
      toast.error(`Save failed: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const set = (k: LayerKey) => (r: LLMRetryRule) => setPolicy((p) => ({ ...p, [k]: r }));

  if (loading) {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-dashed p-10 text-muted-foreground text-sm">
        <Loader2Icon className="size-4 animate-spin" /> Loading retry policy…
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      <div className="rounded-lg border bg-muted/30 p-3 text-muted-foreground text-xs leading-relaxed">
        A failed model call passes through five retry layers, from inner to outer:
        <span className="text-foreground">
          {" "}
          Connection → Empty response → Same-provider safe window → Rotation circuit breaker → Intent rerun
        </span>
        . Outer layers run only after inner layers are exhausted, so attempt counts{" "}
        <span className="text-foreground">multiply</span>. Maximizing every layer can turn one transient failure into
        dozens of requests. Leave all fields blank to retain the current defaults. The first three layers can be
        overridden for each model profile.
      </div>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {(Object.keys(RETRY_LAYERS) as LayerKey[]).map((k) => (
          <RetryRuleFields key={k} layer={k} idPrefix="gl" value={policy[k]} onChange={set(k)} />
        ))}
      </div>

      <div className="flex gap-2">
        <Button onClick={save} disabled={saving}>
          {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />}
          Save
        </Button>
        <Button variant="outline" onClick={() => setPolicy(ZERO_POLICY)} disabled={saving}>
          Reset all defaults
        </Button>
      </div>
    </div>
  );
}
