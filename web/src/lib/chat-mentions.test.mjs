import { activeMention, mentionKinds, mentionSearch, mentionToken, selectedMentions } from "./chat-mentions.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("mention trigger supports cursor placement without hijacking email", () => {
  assert.equal(activeMention("user@example.com", 16), null);
  assert.equal(activeMention("Selected @[Finding#1 X]", 22), null);
  assert.equal(activeMention("\u5df2\u9009 @[\u6f0f\u6d1e#1 X]", 12), null);
  assert.deepEqual(activeMention("See @Finding trailing text", 12), { start: 4, end: 12, query: "Finding" });
  assert.equal(activeMention("@Finding\nNext line", 18), null);
  assert.equal(activeMention("@\u6f0f\u6d1e\n\u4e0b\u4e00\u884c", 8), null);
  assert.deepEqual(activeMention("\u67e5\u770b@\u6f0f\u6d1e trailing text", 5), {
    start: 2,
    end: 5,
    query: "\u6f0f\u6d1e",
  });
});

test("categories, English labels, legacy aliases, IP and keyword search", () => {
  assert.equal(mentionSearch("").categories.length, 9);
  assert.equal(mentionSearch("Fin").categories[0].kind, "finding");
  assert.equal(mentionSearch("Finding").kind, "finding");
  assert.equal(mentionSearch("Finding SQL injection").query, "SQL injection");
  assert.equal(mentionSearch("\u6f0f").categories[0].kind, "finding");
  assert.equal(mentionSearch("\u6f0f\u6d1e").kind, "finding");
  assert.equal(mentionSearch("\u6f0f\u6d1eSQL injection").query, "SQL injection");
  assert.equal(mentionSearch("\u6f0f\u6d1eSQL\u6ce8\u5165").query, "SQL\u6ce8\u5165");
  assert.equal(mentionSearch("ip 192.0.2.1").kind, "ip");
  assert.equal(mentionSearch("Endpoint GET /api").query, "GET /api");
  assert.equal(mentionSearch("api GET /api").query, "GET /api");
  assert.equal(mentionSearch("\u63a5\u53e3 GET /api").query, "GET /api");
  assert.equal(mentionSearch("acme.com").kind, "");
});

test("tokens roundtrip labels and removing one reference preserves its neighbors", () => {
  const first = mentionToken({ kind: "finding", id: 12, label: "Title[1]\nDescription" });
  const second = mentionToken({ kind: "ip", id: 13, label: "192.0.2.1" });
  const value = `Analyze ${first} and ${second}`;
  const selected = selectedMentions(value);
  assert.equal(selected.length, 2);
  assert.equal(first, "@[Finding#12 Title（1） Description]");
  assert.equal(selected[0].label, "Finding #12 · Title（1） Description");
  const next = value.slice(0, selected[0].start) + value.slice(selected[0].start + selected[0].token.length);
  assert.equal(selectedMentions(next)[0].token, second);
});

test("all canonical mention labels roundtrip with stable kind IDs", () => {
  assert.deepEqual(
    mentionKinds.map(({ label }) => label),
    ["Finding", "Asset", "Company", "Endpoint", "IP", "App", "Domain", "Subdomain", "Service"],
  );
  for (const item of mentionKinds) {
    assert.equal(mentionSearch(`${item.label} example`).kind, item.kind);
    const token = mentionToken({ kind: item.kind, id: 42, label: "Example" });
    assert.equal(token, `@[${item.label}#42 Example]`);
    assert.equal(selectedMentions(token)[0].label, `${item.label} #42 · Example`);
  }
});

test("legacy mention tokens keep their source text and display English labels", () => {
  const legacyLabels = [
    "\u6f0f\u6d1e",
    "\u8d44\u4ea7",
    "\u4f01\u4e1a",
    "\u63a5\u53e3",
    "IP",
    "\u5e94\u7528",
    "\u57df\u540d",
    "\u5b50\u57df\u540d",
    "\u670d\u52a1",
  ];
  for (const [index, label] of legacyLabels.entries()) {
    const token = `@[${label}#42 Example]`;
    const selected = selectedMentions(token);
    assert.equal(selected.length, 1);
    assert.equal(selected[0].token, token);
    assert.equal(selected[0].label, `${mentionKinds[index].label} #42 · Example`);
    assert.equal(mentionSearch(`${label} Example`).kind, mentionKinds[index].kind);
  }
});
