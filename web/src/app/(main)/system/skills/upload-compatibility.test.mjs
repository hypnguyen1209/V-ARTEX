import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const path = new URL("./page.tsx", import.meta.url);
const source = readFileSync(path, "utf8");
const ast = ts.createSourceFile(path.pathname, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
let declaration;
function visit(node) {
  if (ts.isFunctionDeclaration(node) && node.name?.text === "isSkillAlreadyInstalled") declaration = node;
  ts.forEachChild(node, visit);
}
visit(ast);
const script = declaration
  ? ts.transpileModule(declaration.getText(ast), {
      compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
    }).outputText
  : "";
const matches = vm.runInNewContext(
  `${script}\ntypeof isSkillAlreadyInstalled === 'undefined' ? undefined : isSkillAlreadyInstalled`,
);

test("skill upload conflicts recognize English and historical errors without matching unrelated failures", () => {
  assert.equal(typeof matches, "function");
  assert.equal(matches("Skill research already exists"), true);
  assert.equal(matches("Skill research \u5df2\u5b58\u5728"), true);
  assert.equal(matches("Skill upload failed: permission denied"), false);
});
