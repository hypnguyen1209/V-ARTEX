import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const path = new URL("../app/(main)/function/tasks/detail/_tabs/sessions-tab.tsx", import.meta.url);
const source = readFileSync(path, "utf8");
const ast = ts.createSourceFile(path.pathname, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);

function nodes(root, predicate) {
  const found = [];
  function visit(node) {
    if (predicate(node)) found.push(node);
    ts.forEachChild(node, visit);
  }
  visit(root);
  return found;
}

function callNamed(name) {
  return (node) => ts.isCallExpression(node) && node.expression.getText(ast) === name;
}

function loadFallbackState() {
  const names = ["MAX_KEEP", "mergeBySeq", "mergeFallbackPage"];
  const declarations = ast.statements.filter((node) => {
    if (ts.isFunctionDeclaration(node)) return names.includes(node.name?.text);
    if (ts.isVariableStatement(node))
      return node.declarationList.declarations.some((item) => names.includes(item.name.getText(ast)));
    return false;
  });
  const script = ts.transpileModule(declarations.map((node) => node.getText(ast)).join("\n"), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
  }).outputText;
  const exports = names.map((name) => `${name}: typeof ${name} === "undefined" ? undefined : ${name}`).join(",");
  return vm.runInNewContext(`${script}\n({${exports}})`);
}

function state(overrides = {}) {
  return {
    items: [],
    loaded: false,
    loading: false,
    loadingMore: false,
    hasMore: false,
    earliestSeq: 0,
    unread: 0,
    lastTs: "",
    ...overrides,
  };
}

const activity = (seq) => ({ seq, worker: "mainagent", kind: "text", ts: "2026-11-02T00:00:00Z", summary: `${seq}` });

test("a successful fallback recovers failed unloaded transcript state", () => {
  const { mergeFallbackPage } = loadFallbackState();
  assert.equal(typeof mergeFallbackPage, "function");
  const recovered = mergeFallbackPage(
    state({ loading: true, error: "Load failed" }),
    [activity(10), activity(11)],
    true,
    false,
  );
  assert.equal(recovered.loaded, true);
  assert.equal(recovered.loading, false);
  assert.equal(recovered.error, undefined);
  assert.equal(recovered.hasMore, true);
  assert.equal(recovered.earliestSeq, 10);
  assert.equal(recovered.lastTs, activity(11).ts);
  assert.deepEqual(
    Array.from(recovered.items, (item) => item.seq),
    [10, 11],
  );
});

test("fallback updates preserve loaded pagination metadata without trimming", () => {
  const { mergeFallbackPage } = loadFallbackState();
  assert.equal(typeof mergeFallbackPage, "function");
  const updated = mergeFallbackPage(
    state({ items: [activity(20)], loaded: true, hasMore: true, earliestSeq: 7, error: "stale" }),
    [activity(21)],
    false,
    true,
  );
  assert.equal(updated.hasMore, true);
  assert.equal(updated.earliestSeq, 7);
  assert.equal(updated.error, undefined);
});

test("fallback trims MAX_KEEP plus one records when the transcript is trimmable", () => {
  const { MAX_KEEP, mergeFallbackPage } = loadFallbackState();
  assert.equal(typeof mergeFallbackPage, "function");
  const incoming = Array.from({ length: MAX_KEEP + 1 }, (_, index) => activity(index + 1));
  const updated = mergeFallbackPage(state(), incoming, false, true);
  assert.equal(updated.items.length, MAX_KEEP);
  assert.equal(updated.items[0].seq, 2);
  assert.equal(updated.hasMore, true);
  assert.equal(updated.earliestSeq, 2);
});

test("fallback keeps an over-limit transcript intact while the user is scrolled up", () => {
  const { MAX_KEEP, mergeFallbackPage } = loadFallbackState();
  assert.equal(typeof mergeFallbackPage, "function");
  const incoming = Array.from({ length: MAX_KEEP + 1 }, (_, index) => activity(index + 1));
  const updated = mergeFallbackPage(state(), incoming, false, false);
  assert.equal(updated.items.length, MAX_KEEP + 1);
  assert.equal(updated.hasMore, false);
  assert.equal(updated.earliestSeq, 1);
});

test("the active transcript polls recursively only while task SSE is down", () => {
  const fallbackRef = nodes(
    ast,
    (node) => ts.isVariableDeclaration(node) && node.name.getText(ast) === "fallbackMaxRef",
  )[0];
  assert.ok(fallbackRef, "fallbackMaxRef must retain the latest fallback sequence for each session key");

  const effects = nodes(ast, callNamed("React.useEffect"));
  const sseEffect = effects.find((effect) => effect.arguments[0]?.getText(ast).includes("new EventSource"));
  const fallbackEffect = effects.find((effect) => {
    const text = effect.arguments[0]?.getText(ast) ?? "";
    return text.includes("fallbackMaxRef") && text.includes("setTimeout");
  });
  assert.ok(sseEffect, "the primary SSE effect is missing");
  assert.ok(fallbackEffect, "the transcript fallback effect is missing");
  const callback = fallbackEffect.arguments[0];
  const dependencies = fallbackEffect.arguments[1];
  assert.ok(ts.isArrayLiteralExpression(dependencies));
  assert.deepEqual(
    dependencies.elements.map((element) => element.getText(ast)),
    ["sseLive", "taskId", "patchStore"],
  );

  const sseGuard = nodes(
    callback,
    (node) =>
      ts.isIfStatement(node) &&
      node.expression.getText(ast) === "sseLive" &&
      nodes(node.thenStatement, (child) => ts.isReturnStatement(child) && child.expression === undefined).length > 0,
  )[0];
  assert.ok(sseGuard, "healthy SSE must exit before starting transcript polling");

  const activeKey = nodes(
    callback,
    (node) => ts.isVariableDeclaration(node) && node.initializer?.getText(ast) === "activeKeyRef.current",
  )[0];
  assert.ok(activeKey, "each fallback tick must read the current active session key");
  const key = activeKey.name.getText(ast);

  const historyCall = nodes(callback, callNamed("api.activityHistory")).find(
    (call) => call.arguments.map((argument) => argument.getText(ast)).join(",") === `taskId,${key},0,PAGE`,
  );
  assert.ok(historyCall, "fallback polling must load the active session's latest history page");

  const maxCall = nodes(callback, callNamed("Math.max")).find((call) =>
    call.arguments.some((argument) => argument.getText(ast).endsWith(".seq")),
  );
  assert.ok(maxCall, "fallback polling must compute the latest sequence in the response");

  const timeoutCalls = nodes(callback, callNamed("setTimeout"));
  assert.equal(timeoutCalls.length, 2, "fallback polling must schedule an initial and a recursive timeout");
  assert.ok(
    timeoutCalls.every((call) => call.arguments[1]?.getText(ast) === "2000"),
    "both fallback waits must be 2000ms",
  );
  assert.equal(
    new Set(timeoutCalls.map((call) => call.arguments[0]?.getText(ast))).size,
    1,
    "the timeout must recursively schedule the same poll function",
  );
  assert.equal(
    nodes(callback, callNamed("setInterval")).length,
    0,
    "fallback requests must never overlap via setInterval",
  );

  const requestTry = nodes(callback, (node) => ts.isTryStatement(node)).find(
    (statement) => nodes(statement.tryBlock, callNamed("api.activityHistory")).length > 0,
  );
  assert.ok(requestTry?.catchClause, "fallback request errors must be best-effort");
  const retryGuard = nodes(
    requestTry.finallyBlock,
    (node) =>
      ts.isIfStatement(node) &&
      node.expression.getText(ast) === "alive" &&
      nodes(node.thenStatement, callNamed("setTimeout")).length === 1,
  )[0];
  assert.ok(retryGuard, "the next fallback timeout must be scheduled from finally only while alive");

  const cleanup = nodes(
    callback,
    (node) =>
      ts.isReturnStatement(node) &&
      !!node.expression &&
      nodes(node.expression, callNamed("clearTimeout")).some(
        (call) => call.arguments.length === 1 && call.arguments[0].getText(ast) === "timer",
      ),
  )[0];
  assert.ok(cleanup, "effect cleanup must call clearTimeout(timer)");

  const aliveGuard = nodes(
    callback,
    (node) =>
      ts.isIfStatement(node) &&
      node.expression.getText(ast) === "!alive" &&
      nodes(node.thenStatement, (child) => ts.isReturnStatement(child) && child.expression === undefined).length > 0,
  )[0];
  assert.ok(aliveGuard, "a response received after cleanup must not update transcript state");

  assert.ok(callback.getText(ast).includes("fallbackMaxRef.current[key]"));
  const callbackText = callback.getText(ast);
  assert.match(callbackText, /fallbackMaxRef\.current\[[^\]]+\]\s*=\s*Math\.max/);
  const patchCall = nodes(callback, callNamed("patchStore")).find(
    (call) => call.arguments[0]?.getText(ast) === key && call.getText(ast).includes("mergeFallbackPage"),
  );
  assert.ok(patchCall, "only a newer fallback page should recover the keyed transcript store");
});

test("the System transcript fallback uses generic activity with a durable cursor", () => {
  const cursorRef = nodes(
    ast,
    (node) => ts.isVariableDeclaration(node) && node.name.getText(ast) === "systemFallbackCursorRef",
  )[0];
  assert.ok(cursorRef, "System fallback needs a task-level generic activity cursor");

  const fallbackEffect = nodes(ast, callNamed("React.useEffect")).find((effect) => {
    const text = effect.arguments[0]?.getText(ast) ?? "";
    return text.includes("fallbackMaxRef") && text.includes("setTimeout");
  });
  assert.ok(fallbackEffect);
  const systemBranch = nodes(
    fallbackEffect.arguments[0],
    (node) => ts.isIfStatement(node) && node.expression.getText(ast) === 'key === "system"',
  )[0];
  assert.ok(systemBranch, "System must have a dedicated fallback branch");
  assert.equal(nodes(systemBranch.thenStatement, callNamed("api.activityHistory")).length, 0);
  assert.equal(nodes(systemBranch.thenStatement, callNamed("api.activity")).length, 1);
  assert.ok(systemBranch.thenStatement.getText(ast).includes("SYSTEM_SCAN_PAGE"));
  assert.ok(systemBranch.thenStatement.getText(ast).includes("systemFallbackCursorRef.current"));
  assert.ok(systemBranch.thenStatement.getText(ast).includes('sessionKeyOf(item) === "system"'));
  assert.ok(systemBranch.elseStatement, "non-System sessions must use a distinct history branch");
  assert.equal(nodes(systemBranch.elseStatement, callNamed("api.activityHistory")).length, 1);
});

function fallbackHarness({ live = false, key = "main:0", history, generic } = {}) {
  const effect = nodes(ast, callNamed("React.useEffect")).find(
    (node) =>
      node.arguments[0]?.getText(ast).includes("fallbackMaxRef") &&
      node.arguments[0]?.getText(ast).includes("setTimeout"),
  );
  const pending = new Map();
  const stores = {};
  const writes = [];
  const activeKeyRef = { current: key };
  const fallbackMaxRef = { current: {} };
  const systemFallbackCursorRef = { current: 0 };
  let nextTimer = 0;
  const context = {
    ...loadFallbackState(),
    PAGE: 200,
    SYSTEM_SCAN_PAGE: 500,
    sessionKeyOf: vm.runInNewContext(
      ts.transpileModule(
        `(${ast.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "sessionKeyOf").getText(ast)})`,
        { compilerOptions: { target: ts.ScriptTarget.ES2022 } },
      ).outputText,
    ),
    sseLive: live,
    taskId: "task",
    activeKeyRef,
    storeRef: { current: stores },
    fallbackMaxRef,
    systemFallbackCursorRef,
    atBottomRef: { current: true },
    api: { activityHistory: history, activity: generic },
    patchStore: (session, update) => {
      writes.push(session);
      stores[session] = update(stores[session] ?? state());
    },
    setTimeout: (callback, delay) => {
      assert.equal(delay, 2000);
      pending.set(++nextTimer, callback);
      return nextTimer;
    },
    clearTimeout: (id) => pending.delete(id),
  };
  const script = ts.transpileModule(`(${effect.arguments[0].getText(ast)})()`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText;
  const cleanup = vm.runInNewContext(script, context);
  return {
    stores,
    writes,
    pending,
    atBottomRef: context.atBottomRef,
    activeKeyRef,
    systemFallbackCursorRef,
    cleanup,
    tick() {
      assert.equal(pending.size, 1);
      const [id, callback] = pending.entries().next().value;
      pending.delete(id);
      return callback();
    },
  };
}

test("healthy SSE starts no fallback timer or request", () => {
  const harness = fallbackHarness({ live: true, history: () => assert.fail("unexpected request") });
  assert.equal(harness.pending.size, 0);
  assert.equal(harness.cleanup, undefined);
});

test("fallback captures the requested key and waits for completion before scheduling again", async () => {
  let resolve;
  const harness = fallbackHarness({
    history: (_task, key) => {
      assert.equal(key, "main:0");
      return new Promise((done) => {
        resolve = done;
      });
    },
  });
  const request = harness.tick();
  assert.equal(harness.pending.size, 0);
  harness.activeKeyRef.current = "plan";
  resolve({ items: [activity(7)], hasMore: true });
  await request;
  assert.equal(harness.stores["main:0"].items[0].seq, 7);
  assert.equal(harness.stores.plan, undefined);
  assert.equal(harness.pending.size, 1);
  harness.cleanup();
  assert.equal(harness.pending.size, 0);
});

test("cleanup ignores an in-flight response and prevents another fallback timer", async () => {
  let resolve;
  const harness = fallbackHarness({
    history: () =>
      new Promise((done) => {
        resolve = done;
      }),
  });
  const request = harness.tick();
  harness.cleanup();
  resolve({ items: [activity(7)], hasMore: true });
  await request;
  assert.deepEqual(harness.stores, {});
  assert.equal(harness.pending.size, 0);
});

test("fallback retries failures and recovers a later load failure even at the same sequence", async () => {
  let attempts = 0;
  const harness = fallbackHarness({
    history: async () => {
      if (++attempts === 1) throw new Error("offline");
      return { items: [activity(7)], hasMore: true };
    },
  });
  await harness.tick();
  assert.equal(harness.pending.size, 1);
  await harness.tick();
  harness.stores["main:0"].error = "late initial history failure";
  await harness.tick();
  assert.equal(harness.stores["main:0"].error, undefined);
  harness.cleanup();
});

test("System fallback scans full generic pages and retains its cursor across ticks", async () => {
  const cursors = [];
  const harness = fallbackHarness({
    key: "system",
    history: () => assert.fail("System history is unsupported"),
    generic: async (_task, { since, limit }) => {
      cursors.push(since);
      assert.equal(limit, 500);
      if (since === 0) return { items: Array.from({ length: 500 }, (_, i) => activity(i + 1)), cursor: 500 };
      if (since === 500)
        return {
          items: [
            { ...activity(501), kind: "llm_switch" },
            { ...activity(502), worker: "system" },
          ],
          cursor: 502,
        };
      return { items: [], cursor: 502 };
    },
  });
  await harness.tick();
  assert.deepEqual(cursors, [0, 500]);
  assert.deepEqual(
    Array.from(harness.stores.system.items, (item) => item.seq),
    [501, 502],
  );
  await harness.tick();
  assert.deepEqual(cursors, [0, 500, 502]);
  assert.equal(harness.writes.length, 1, "unchanged System polling must advance no transcript state");
  assert.equal(harness.systemFallbackCursorRef.current, 502);
  harness.cleanup();
});

test("System older-history loading never requests unsupported session history", () => {
  const loader = nodes(ast, (node) => ts.isVariableDeclaration(node) && node.name.getText(ast) === "loadEarlier")[0];
  assert.ok(loader);
  const systemBranch = nodes(
    loader,
    (node) => ts.isIfStatement(node) && node.expression.getText(ast) === 'key === "system"',
  )[0];
  assert.ok(systemBranch, "trimmed System history needs a generic activity loader");
  assert.equal(nodes(systemBranch.thenStatement, callNamed("api.activityHistory")).length, 0);
  assert.equal(nodes(systemBranch.thenStatement, callNamed("api.activity")).length, 1);
});

test("System older-history loading scans to its anchor and returns the closest older page", async () => {
  const loader = nodes(ast, (node) => ts.isVariableDeclaration(node) && node.name.getText(ast) === "loadEarlier")[0];
  const { mergeBySeq } = loadFallbackState();
  const current = state({
    loaded: true,
    items: [{ ...activity(900), worker: "system" }],
    hasMore: true,
    earliestSeq: 900,
  });
  const cursors = [];
  const context = {
    store: { system: current },
    taskId: "task",
    PAGE: 200,
    SYSTEM_SCAN_PAGE: 500,
    mergeBySeq,
    sessionKeyOf: (item) => (item.worker === "system" ? "system" : "main:0"),
    patchStore: (_key, update) => {
      context.store.system = update(context.store.system);
    },
    requestAnimationFrame: (callback) => callback(),
    api: {
      activityHistory: () => assert.fail("System history is unsupported"),
      activity: async (_task, { since }) => {
        cursors.push(since);
        return {
          items: Array.from({ length: 500 }, (_, i) => ({ ...activity(since + i + 1), worker: "system" })),
          cursor: since + 500,
        };
      },
    },
  };
  const callback = loader.initializer.arguments[0];
  const script = ts.transpileModule(`(${callback.getText(ast)})("system", () => null)`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText;
  vm.runInNewContext(script, context);
  // The callback starts an async fetch and updates the store once its page completes.
  await new Promise(setImmediate);
  assert.deepEqual(cursors, [0, 500]);
  assert.equal(context.store.system.loadingMore, false);
  assert.equal(context.store.system.hasMore, true);
  assert.equal(context.store.system.earliestSeq, 700);
  assert.equal(context.store.system.items.length, 201);
  assert.deepEqual(
    Array.from(context.store.system.items, (item) => item.seq),
    Array.from({ length: 201 }, (_, i) => 700 + i),
  );
});

test("returning to the bottom trims retained history even when the fallback page is unchanged", async () => {
  const { MAX_KEEP } = loadFallbackState();
  const harness = fallbackHarness({ history: async () => ({ items: [activity(MAX_KEEP + 1)], hasMore: false }) });
  harness.atBottomRef.current = false;
  harness.stores["main:0"] = state({
    loaded: true,
    earliestSeq: 1,
    items: Array.from({ length: MAX_KEEP }, (_, i) => activity(i + 1)),
  });
  await harness.tick();
  assert.equal(harness.stores["main:0"].items.length, MAX_KEEP + 1);
  await harness.tick();
  assert.equal(harness.writes.length, 1, "unchanged scrolled-up history must not patch");
  harness.atBottomRef.current = true;
  await harness.tick();
  assert.equal(harness.writes.length, 2, "returning to bottom must perform exactly one trim write");
  assert.equal(harness.stores["main:0"].items.length, MAX_KEEP);
  assert.equal(harness.stores["main:0"].earliestSeq, 2);
  assert.equal(harness.stores["main:0"].hasMore, true);
  harness.cleanup();
});

test("unchanged healthy bounded fallback pages perform no store writes", async () => {
  const harness = fallbackHarness({ history: async () => ({ items: [activity(7)], hasMore: false }) });
  await harness.tick();
  assert.equal(harness.writes.length, 1);
  const previous = harness.stores["main:0"];
  await harness.tick();
  await harness.tick();
  assert.equal(harness.writes.length, 1, "unchanged responses must never call patchStore");
  assert.equal(harness.stores["main:0"], previous);
  harness.cleanup();
});

test("fallback guards read the latest committed store without restarting polling", () => {
  const sync = nodes(ast, callNamed("React.useEffect")).find((effect) =>
    effect.arguments[0]?.getText(ast).includes("storeRef.current = store"),
  );
  assert.ok(sync, "the polling guard must keep its store snapshot current");
  assert.equal(sync.arguments[1].getText(ast), "[store]");
  const store = { plan: state({ loaded: true }) };
  const storeRef = { current: {} };
  const script = ts.transpileModule(`(${sync.arguments[0].getText(ast)})()`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText;
  vm.runInNewContext(script, { store, storeRef });
  assert.equal(storeRef.current, store);
});
