import { approvalToolName } from "./approval-summary.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("English approval summaries display only the tool name", () => {
  assert.equal(approvalToolName("Tool Bash requests approval (#42)"), "Bash");
  assert.equal(approvalToolName("Tool WebSearch requests approval (#123)"), "WebSearch");
});

test("legacy approval summaries retain tool name extraction", () => {
  assert.equal(approvalToolName("\u5de5\u5177 Bash \u8bf7\u6c42\u5ba1\u6279 (#42)"), "Bash");
  assert.equal(approvalToolName("\u5de5\u5177\tWebSearch\t\u8bf7\u6c42\u5ba1\u6279 (#123)"), "WebSearch");
});

test("unrecognized summaries retain their original text", () => {
  assert.equal(approvalToolName("Approval details unavailable"), "Approval details unavailable");
  assert.equal(approvalToolName(""), "");
});
