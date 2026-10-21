import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const types = readFileSync(new URL("../lib/types.ts", import.meta.url), "utf8");
const page = readFileSync(new URL("../app/(main)/system/settings/page.tsx", import.meta.url), "utf8");
const mock = readFileSync(new URL("../lib/mock/handler.ts", import.meta.url), "utf8");

test("global agent concurrency settings stay aligned across type, UI, and mock API", () => {
  for (const field of [
    "agent_concurrency_enabled",
    "agent_concurrency_limit",
    "agent_runs_active",
    "agent_runs_waiting",
  ]) {
    assert.match(types, new RegExp(`${field}\\?`));
    assert.match(page, new RegExp(field));
  }
  assert.match(page, /Global Agent Concurrency/);
  assert.match(page, /setInterval/);
  assert.match(mock, /Object\.assign\(D\.settings, b\)/);
});
