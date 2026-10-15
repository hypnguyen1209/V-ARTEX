import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

function loadFunctions(relativePath, names) {
  const path = new URL(relativePath, import.meta.url);
  const source = readFileSync(path, "utf8");
  const ast = ts.createSourceFile(path.pathname, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
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

test("approval source recognizes English and historical model prefixes with explicit sources taking precedence", () => {
  const { source } = loadFunctions("./approval-records.tsx", ["source"]);
  assert.equal(source({ reason: "[Model] Allowed" }), "model");
  assert.equal(source({ reason: "[\u6a21\u578b] Allowed" }), "model");
  assert.equal(source({ reason: "[Model] Allowed", rule_id: 7 }), "rule");
  assert.equal(source({ reason: "[Model] Allowed", decision_source: "rule" }), "rule");
  assert.equal(source({ reason: "Unclassified reason" }), "unknown");
});

test("upload manifests use an English heading and append to historical headings without duplicating them", () => {
  const { appendUploads } = loadFunctions("../app/(main)/function/tasks/page.tsx", [
    "fmtBytes",
    "UPLOAD_MARKER",
    "LEGACY_UPLOAD_MARKER",
    "appendUploads",
  ]);
  const attachments = [{ abs: "/workspace/uploads/test.txt", path: "uploads/test.txt", size: 1024 }];
  const fresh = appendUploads("Test scope", attachments);
  assert.match(fresh, /\[Uploaded files \(absolute paths\)\]/);
  assert.match(fresh, /\/workspace\/uploads\/test\.txt/);
  const legacy = "Test scope\n\u3010\u4e0a\u4f20\u6587\u4ef6\uff08\u7edd\u5bf9\u8def\u5f84\uff09\u3011\n- /old.txt";
  const appended = appendUploads(legacy, attachments);
  assert.ok(appended.startsWith(legacy));
  assert.equal(appended.includes("[Uploaded files (absolute paths)]"), false);
  assert.equal(appendUploads(fresh, attachments).split("[Uploaded files (absolute paths)]").length, 2);
});

test("restored archives recognize English and historical not-found errors without swallowing other failures", () => {
  const { isArchiveMissing } = loadFunctions("../app/(main)/function/tasks/page.tsx", ["isArchiveMissing"]);
  assert.equal(typeof isArchiveMissing, "function");
  assert.equal(isArchiveMissing("Task archive not found"), true);
  assert.equal(isArchiveMissing("\u5f52\u6863\u4e0d\u5b58\u5728"), true);
  assert.equal(isArchiveMissing("Archive storage unavailable"), false);
});

test("service graph labels retain truthy fallbacks when domain or IP fields are empty", () => {
  const { graphLabel } = loadFunctions("../app/(main)/function/tasks/detail/_tabs/coverage-graph-tab.tsx", [
    "graphLabel",
  ]);
  assert.equal(graphLabel({ kind: "service", domain: "", ip: "192.0.2.10", label: "Fallback" }), "192.0.2.10");
  assert.equal(graphLabel({ kind: "service", domain: "", ip: "", label: "Fallback" }), "Fallback");
  assert.equal(graphLabel({ kind: "service", domain: "example.com", ip: "192.0.2.10" }), "example.com");
  assert.equal(
    graphLabel({ kind: "service", port: 443, page_title: "Portal", status_code: 200 }),
    ":443 · Portal · 200",
  );
});

test("coverage collision bounds include labels for wrapped d3-force node data", () => {
  const path = new URL("../app/(main)/function/tasks/detail/_tabs/coverage-graph-tab.tsx", import.meta.url);
  const source = readFileSync(path, "utf8");
  const ast = ts.createSourceFile(path.pathname, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  let radius;
  function visit(node) {
    if (ts.isPropertyAssignment(node) && node.name.getText(ast) === "radius") radius = node.initializer;
    ts.forEachChild(node, visit);
  }
  visit(ast);
  const script = ts.transpileModule(`(${radius.getText(ast)})`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText;
  const measure = vm.runInNewContext(script, {
    nd: (node) => node,
    labelContext: { measureText: () => ({ width: 148 }) },
  });
  const service = { size: 26, lbl: ":443 · Acme homepage · 200" };
  const direct = measure(service);
  const wrapped = measure({ id: "a:5", _original: service });
  assert.ok(Number.isFinite(wrapped));
  assert.equal(wrapped, direct);
  assert.ok(wrapped > Math.hypot(148 / 2 + 3, 26 / 2 + 16));
});
