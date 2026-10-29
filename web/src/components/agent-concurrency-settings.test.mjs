import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { setImmediate } from "node:timers/promises";
import vm from "node:vm";

const types = readFileSync(new URL("../lib/types.ts", import.meta.url), "utf8");
const page = readFileSync(new URL("../app/(main)/system/settings/page.tsx", import.meta.url), "utf8");
const mock = readFileSync(new URL("../lib/mock/handler.ts", import.meta.url), "utf8");
const data = readFileSync(new URL("../lib/mock/data.ts", import.meta.url), "utf8");
const ast = ts.createSourceFile("page.tsx", page, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);

function findNode(root, predicate) {
  if (predicate(root)) return root;
  return ts.forEachChild(root, (node) => findNode(node, predicate));
}

function execute(source, context = {}) {
  const script = ts.transpileModule(`(${source})`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
  }).outputText;
  return vm.runInNewContext(script, context);
}

function loadLocal(name, context = {}) {
  const declaration = findNode(ast, (node) => ts.isVariableDeclaration(node) && node.name.getText(ast) === name);
  assert.ok(declaration?.initializer, `Missing local declaration ${name}`);
  return execute(declaration.initializer.getText(ast), {
    React: { useCallback: (callback) => callback },
    ...context,
  });
}

function effectContaining(marker) {
  const effect = findNode(
    ast,
    (node) =>
      ts.isCallExpression(node) &&
      node.expression.getText(ast) === "React.useEffect" &&
      node.arguments[0].getText(ast).includes(marker),
  );
  assert.ok(effect, `Missing effect containing ${marker}`);
  return effect.arguments[0].getText(ast);
}

function pollingHarness(loaded = true) {
  let tick;
  const requests = [];
  const active = [];
  const waiting = [];
  const cleared = [];
  const effect = execute(effectContaining("setAgentRunsActive"), {
    loaded,
    api: {
      settings: () =>
        new Promise((resolve, reject) => {
          requests.push({ resolve, reject });
        }),
    },
    setAgentRunsActive: (value) => active.push(value),
    setAgentRunsWaiting: (value) => waiting.push(value),
    window: {
      setInterval: (callback, delay) => {
        assert.equal(delay, 3000);
        tick = callback;
        return 42;
      },
      clearInterval: (timer) => cleared.push(timer),
    },
  });
  const cleanup = effect();
  return { tick, cleanup, requests, active, waiting, cleared };
}

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
  assert.match(page, /aria-labelledby="agent-concurrency-heading"/);
  assert.match(page, /id="agent-concurrency-heading"/);
  for (const id of ["agent-concurrency-enabled", "agent-concurrency-limit"]) {
    assert.match(page, new RegExp(`id="${id}"`));
    assert.match(page, new RegExp(`htmlFor="${id}"`));
  }
  assert.match(page, /Active: \{agentRunsActive\} · Waiting: \{agentRunsWaiting\}/);
});

test("saving submits only the parsed concurrency draft, hydrates returned settings, and clears saving state", async () => {
  const returned = {
    agent_concurrency_enabled: false,
    agent_concurrency_limit: 4,
    agent_runs_active: 2,
    agent_runs_waiting: 1,
  };
  const payloads = [];
  const saving = [];
  const applied = [];
  const hydrated = [];
  const messages = [];
  const save = loadLocal("saveAgentConcurrency", {
    agentConcurrencyEnabled: true,
    agentConcurrencyLimit: "12",
    setSavingAgentConcurrency: (value) => saving.push(value),
    api: {
      setSettings: async (payload) => {
        payloads.push({ ...payload });
        return returned;
      },
    },
    apply: (settings) => applied.push(settings),
    applyAgentConcurrency: (settings) => hydrated.push(settings),
    toast: { success: (message) => messages.push(message), error: assert.fail },
  });
  save();
  assert.deepEqual(payloads, [{ agent_concurrency_enabled: true, agent_concurrency_limit: 12 }]);
  assert.deepEqual(saving, [true]);
  await setImmediate();
  assert.deepEqual(saving, [true, false]);
  assert.deepEqual(applied, [returned]);
  assert.deepEqual(hydrated, [returned]);
  assert.deepEqual(messages, ["Global agent concurrency settings saved"]);
});

test("invalid concurrency limits do not save or enter the saving state", () => {
  for (const value of ["", "0", "-1", "1.5", "invalid", "Infinity"]) {
    const errors = [];
    const save = loadLocal("saveAgentConcurrency", {
      agentConcurrencyEnabled: true,
      agentConcurrencyLimit: value,
      setSavingAgentConcurrency: assert.fail,
      api: { setSettings: assert.fail },
      toast: { error: (message) => errors.push(message) },
    });
    save();
    assert.deepEqual(errors, ["Global agent concurrency must be an integer greater than 0"]);
  }
});

test("a failed concurrency save reports its error and clears the saving state", async () => {
  const saving = [];
  const errors = [];
  const save = loadLocal("saveAgentConcurrency", {
    agentConcurrencyEnabled: false,
    agentConcurrencyLimit: "8",
    setSavingAgentConcurrency: (value) => saving.push(value),
    api: {
      setSettings: async () => {
        throw new Error("offline");
      },
    },
    apply: assert.fail,
    applyAgentConcurrency: assert.fail,
    toast: { success: assert.fail, error: (message) => errors.push(message) },
  });
  save();
  await setImmediate();
  assert.deepEqual(saving, [true, false]);
  assert.deepEqual(errors, ["Save failed: offline"]);
});

test("the dedicated concurrency hydrator applies returned form values, counters, and defaults", () => {
  const values = {};
  const context = {
    setAgentConcurrencyEnabled: (value) => {
      values.enabled = value;
    },
    setAgentConcurrencyLimit: (value) => {
      values.limit = value;
    },
    setAgentRunsActive: (value) => {
      values.active = value;
    },
    setAgentRunsWaiting: (value) => {
      values.waiting = value;
    },
  };
  const hydrate = loadLocal("applyAgentConcurrency", context);
  hydrate({
    agent_concurrency_enabled: true,
    agent_concurrency_limit: 5,
    agent_runs_active: 3,
    agent_runs_waiting: 2,
  });
  assert.deepEqual(values, { enabled: true, limit: "5", active: 3, waiting: 2 });
  hydrate({});
  assert.deepEqual(values, { enabled: false, limit: "8", active: 0, waiting: 0 });
});

test("shared settings application cannot erase an unsaved concurrency draft", () => {
  const declaration = findNode(ast, (node) => ts.isVariableDeclaration(node) && node.name.getText(ast) === "apply");
  assert.ok(declaration);
  assert.doesNotMatch(
    declaration.getText(ast),
    /setAgentConcurrencyEnabled|setAgentConcurrencyLimit|setAgentRunsActive|setAgentRunsWaiting|applyAgentConcurrency/,
  );
});

test("initial settings load applies both general settings and concurrency settings", async () => {
  const settings = { agent_concurrency_enabled: true, agent_concurrency_limit: 6 };
  const calls = [];
  execute(effectContaining("setLoaded"), {
    api: { settings: async () => settings },
    apply: (value) => calls.push(["general", value]),
    applyAgentConcurrency: (value) => calls.push(["concurrency", value]),
    setLoaded: (value) => calls.push(["loaded", value]),
  })();
  await setImmediate();
  assert.deepEqual(calls, [
    ["general", settings],
    ["concurrency", settings],
    ["loaded", true],
  ]);
});

test("counter polling is single-flight and cleanup rejects late responses", async () => {
  const poll = pollingHarness();
  assert.equal(poll.requests.length, 0);
  assert.equal(typeof poll.cleanup, "function");
  assert.doesNotMatch(
    effectContaining("setAgentRunsActive"),
    /setAgentConcurrencyEnabled|setAgentConcurrencyLimit|apply(?:AgentConcurrency)?\(/,
  );
  poll.tick();
  poll.tick();
  assert.equal(poll.requests.length, 1, "pending requests must block additional ticks");
  poll.requests[0].resolve({ agent_runs_active: 4, agent_runs_waiting: 2 });
  await setImmediate();
  assert.deepEqual(poll.active, [4]);
  assert.deepEqual(poll.waiting, [2]);
  poll.tick();
  assert.equal(poll.requests.length, 2, "settled requests must release the polling slot");
  poll.cleanup();
  assert.deepEqual(poll.cleared, [42]);
  poll.requests[1].resolve({ agent_runs_active: 1, agent_runs_waiting: 0 });
  await setImmediate();
  assert.deepEqual(poll.active, [4]);
  assert.deepEqual(poll.waiting, [2]);
});

test("polling waits for load and recovers after request errors", async () => {
  const unloaded = pollingHarness(false);
  assert.equal(unloaded.tick, undefined);
  assert.equal(unloaded.cleanup, undefined);
  assert.equal(unloaded.requests.length, 0);
  const poll = pollingHarness();
  poll.tick();
  poll.requests[0].reject(new Error("offline"));
  await setImmediate();
  poll.tick();
  assert.equal(poll.requests.length, 2);
  poll.requests[1].resolve({});
  await setImmediate();
  assert.deepEqual(poll.active, [0]);
  assert.deepEqual(poll.waiting, [0]);
  poll.cleanup();
});

test("mock settings have concurrency defaults and persist successive partial updates", () => {
  const dataAst = ts.createSourceFile("data.ts", data, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const settingsDeclaration = findNode(
    dataAst,
    (node) => ts.isVariableDeclaration(node) && node.name.getText(dataAst) === "settings",
  );
  assert.ok(settingsDeclaration?.initializer);
  const settings = execute(settingsDeclaration.initializer.getText(dataAst));
  assert.deepEqual(
    [
      settings.agent_concurrency_enabled,
      settings.agent_concurrency_limit,
      settings.agent_runs_active,
      settings.agent_runs_waiting,
    ],
    [false, 8, 0, 0],
  );
  const mockAst = ts.createSourceFile("handler.ts", mock, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const putBranch = findNode(
    mockAst,
    (node) => ts.isIfStatement(node) && node.expression.getText(mockAst) === 'path === "/settings" && m === "PUT"',
  );
  assert.ok(putBranch);
  const put = execute(`(b) => { ${putBranch.getText(mockAst)} }`, {
    D: { settings },
    path: "/settings",
    m: "PUT",
  });
  const first = put({ agent_concurrency_enabled: true });
  assert.equal(first.agent_concurrency_enabled, true);
  assert.notEqual(first, settings);
  const second = put({ agent_concurrency_limit: 3 });
  assert.equal(second.agent_concurrency_enabled, true);
  assert.equal(second.agent_concurrency_limit, 3);
  assert.equal(settings.agent_concurrency_enabled, true);
  assert.equal(settings.agent_concurrency_limit, 3);
  assert.equal(settings.workers, 3);
  assert.equal(settings.agent_runs_active, 0);
  assert.equal(settings.agent_runs_waiting, 0);
});
