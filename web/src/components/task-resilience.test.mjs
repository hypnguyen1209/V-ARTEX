import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

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
  assert.equal(
    effects.indexOf(fallbackEffect),
    effects.indexOf(sseEffect) + 1,
    "the transcript fallback effect must immediately follow the primary SSE effect",
  );

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

  const maxGuard = nodes(
    callback,
    (node) =>
      ts.isIfStatement(node) &&
      ts.isBinaryExpression(node.expression) &&
      node.expression.operatorToken.kind === ts.SyntaxKind.LessThanEqualsToken &&
      node.expression.left.getText(ast) === "maxSeq" &&
      node.expression.right.getText(ast).includes(`fallbackMaxRef.current[${key}]`),
  )[0];
  assert.ok(maxGuard, "an unchanged fallback page must not update the transcript store");
  const callbackText = callback.getText(ast);
  assert.match(callbackText, /fallbackMaxRef\.current\[[^\]]+\]\s*=\s*maxSeq/);
  const patchCall = nodes(callback, callNamed("patchStore")).find(
    (call) => call.arguments[0]?.getText(ast) === key && call.getText(ast).includes("mergeBySeq"),
  );
  assert.ok(patchCall, "only a newer fallback page should merge into the keyed transcript store");
});
