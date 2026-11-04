"use client";

import * as React from "react";

import { CheckIcon, CopyIcon, FileTextIcon } from "lucide-react";
import { toast } from "sonner";

import { Markdown } from "@/components/markdown";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import { copyText } from "@/lib/utils";

export function ReportTab({ taskId }: { taskId: string }) {
  return <TaskReport key={taskId} taskId={taskId} />;
}

type ReportState = { status: "loading" | "error" } | { status: "ready"; text: string };

function TaskReport({ taskId }: { taskId: string }) {
  const [state, setState] = React.useState<ReportState>({ status: "loading" });
  const [attempt, setAttempt] = React.useState(0);
  const [copied, setCopied] = React.useState(false);
  const mounted = React.useRef(false);
  const report = state.status === "ready" ? state.text : "";

  React.useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // biome-ignore lint/correctness/useExhaustiveDependencies: Retry increments attempt to reload this task.
  React.useEffect(() => {
    let active = true;
    setState({ status: "loading" });
    api.report(taskId).then(
      (text) => {
        if (active) setState({ status: "ready", text });
      },
      () => {
        if (active) setState({ status: "error" });
      },
    );
    return () => {
      active = false;
    };
  }, [taskId, attempt]);

  React.useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  function retry() {
    setAttempt((current) => current + 1);
  }

  async function copy() {
    if (!report) return;
    const ok = await copyText(report);
    if (!mounted.current) return;
    if (ok) {
      setCopied(true);
      toast.success("Markdown copied");
    } else {
      toast.error("Copy failed. Select and copy the text manually.");
    }
  }

  let content: React.ReactNode;
  if (state.status === "loading") {
    content = (
      <div
        role="status"
        className="flex flex-col items-center justify-center gap-2 rounded-md border border-dashed py-16 text-muted-foreground text-sm"
      >
        <FileTextIcon className="size-8 opacity-40" />
        Loading…
      </div>
    );
  } else if (state.status === "error") {
    content = (
      <div
        role="alert"
        className="flex flex-col items-center justify-center gap-2 rounded-md border border-dashed py-16 text-sm"
      >
        <p>Failed to load the report.</p>
        <Button size="sm" variant="outline" onClick={retry}>
          Retry
        </Button>
      </div>
    );
  } else if (report) {
    content = (
      <div className="max-h-[60vh] overflow-auto rounded-md border bg-muted/20 p-4">
        <Markdown text={report} />
      </div>
    );
  } else {
    content = (
      <div className="flex flex-col items-center justify-center gap-2 rounded-md border border-dashed py-16 text-muted-foreground text-sm">
        <FileTextIcon className="size-8 opacity-40" />
        No report yet
      </div>
    );
  }

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2 text-sm">
          <FileTextIcon className="size-4" /> Penetration Test Report (Markdown)
        </CardTitle>
        <div className="flex gap-2">
          {report && (
            <Button size="sm" variant="outline" onClick={copy}>
              {copied ? <CheckIcon /> : <CopyIcon />} Copy
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent>{content}</CardContent>
    </Card>
  );
}
