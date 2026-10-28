import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

function loadFunctions(relativePath, names, context = {}) {
  const path = new URL(relativePath, import.meta.url);
  const source = readFileSync(path, "utf8");
  const ast = ts.createSourceFile(path.pathname, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const declarations = [];
  function visit(node) {
    if (ts.isFunctionDeclaration(node) && names.includes(node.name?.text)) declarations.push(node);
    ts.forEachChild(node, visit);
  }
  visit(ast);
  assert.equal(declarations.length, names.length);
  const script = ts.transpileModule(declarations.map((node) => node.getText(ast)).join("\n"), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
  }).outputText;
  const exports = names.map((name) => `${name}: typeof ${name} === "undefined" ? undefined : ${name}`).join(",");
  return vm.runInNewContext(`${script}\n({${exports}})`, context);
}

test("opening a disabled remote MCP record tolerates null args", () => {
  let form;
  const loaded = [];
  const state = [];
  const { openEdit } = loadFunctions("./mcp/page.tsx", ["envToText", "openEdit"], {
    setEditing(value) {
      state.push(["editing", value.id]);
    },
    setForm(value) {
      form = value;
    },
    setTab(value) {
      state.push(["tab", value]);
    },
    setOpen(value) {
      state.push(["open", value]);
    },
    loadTools(id) {
      loaded.push(id);
    },
  });

  openEdit({
    id: 7,
    name: "ScopeSentry",
    transport: "http",
    args: null,
    env: { "X-API-Key": "" },
    enabled: false,
  });

  assert.equal(form.args, "");
  assert.equal(form.env, "X-API-Key=");
  assert.deepEqual(state, [
    ["editing", 7],
    ["tab", "config"],
    ["open", true],
  ]);
  assert.deepEqual(loaded, [7]);
});

test("opening a tool record treats a null schema as having no parameter rows", () => {
  const { toRows } = loadFunctions("./tools/page.tsx", ["toRows"]);

  assert.deepEqual([...toRows(null)], []);
});

test("saving a tool record turns a null schema into an empty schema", () => {
  const { applyRows } = loadFunctions("./tools/page.tsx", ["coerceDefault", "applyRows"], { structuredClone });

  assert.deepEqual({ ...applyRows(null, []) }, {});
});
