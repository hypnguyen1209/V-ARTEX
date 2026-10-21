"use client";

import * as React from "react";

import { ListTodo } from "lucide-react";

import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

// TodoPopover shows the latest TodoWrite state of a session (chat conversation or
// task worker/planner replay). The full todo list isn't in the activity list
// (detail is lazy), so on open it fetches the detail of the most-recent TodoWrite
// call — by seq — and parses its {todos:[…]} JSON. Purely client-side; the caller
// supplies the seq + a detail fetcher (conversation or exploration endpoint).
export function TodoPopover({
  seq,
  fetchDetail,
}: {
  seq: number | null;
  fetchDetail: (seq: number) => Promise<string>;
}) {
  const [open, setOpen] = React.useState(false);
  const [todos, setTodos] = React.useState<{ content: string; status: string }[] | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [err, setErr] = React.useState("");

  const load = React.useCallback(async () => {
    if (seq == null) return;
    setLoading(true);
    setErr("");
    try {
      const detail = await fetchDetail(seq);
      const start = detail.indexOf("{"); // detail may have a "TodoWrite " prefix
      const parsed = JSON.parse(start >= 0 ? detail.slice(start) : detail);
      setTodos(Array.isArray(parsed?.todos) ? parsed.todos : []);
    } catch {
      setErr("Failed to parse todos");
      setTodos(null);
    } finally {
      setLoading(false);
    }
  }, [seq, fetchDetail]);

  // refetch on each open — todos change as the run progresses.
  React.useEffect(() => {
    if (open) void load();
  }, [open, load]);

  const disabled = seq == null;
  const MARK: Record<string, string> = { pending: "☐", in_progress: "▶", completed: "✔" };
  return (
    <Popover open={open} onOpenChange={disabled ? undefined : setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          disabled={disabled}
          title={disabled ? "No todos in this session" : "View latest todos"}
          className="flex items-center gap-0.5 text-muted-foreground/70 text-xs hover:text-primary disabled:pointer-events-none disabled:opacity-40"
        >
          <ListTodo className="size-3" />
          Todo
        </button>
      </PopoverTrigger>
      <PopoverContent align="end" className="max-h-80 w-80 overflow-auto p-2">
        <p className="px-1 pb-1 font-medium text-[11px] text-muted-foreground">
          Latest todos{loading ? " · Loading…" : ""}
        </p>
        {err && <p className="px-1 text-destructive text-xs">{err}</p>}
        {todos && todos.length === 0 && !loading && <p className="px-1 text-muted-foreground text-xs">(empty)</p>}
        <ul className="space-y-0.5">
          {(todos ?? []).map((t, i) => (
            <li
              key={`${i}:${t.content}`}
              className={cn(
                "flex gap-1.5 px-1 text-xs",
                t.status === "completed" && "text-muted-foreground line-through",
              )}
            >
              <span className="shrink-0">{MARK[t.status] ?? "☐"}</span>
              <span className="break-words">{t.content}</span>
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  );
}
