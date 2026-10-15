# Long side conversations and context budgets

Fixed 2026-09-11. The original implementation treated request JSON character count as tokens and inherited the main task's 32K output reserve. HTML, JavaScript, and large tool results could therefore reject otherwise valid side questions early.

## Upstream implementation review

- [Grok CLI side context](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/agent.ts#L739) extracts recent user/assistant text within roughly 2000 characters and at most 400 per entry. This path does not maintain continuous side-question history.
- [Grok CLI independent requests](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/utils/side-question.ts) use independent cancellation, a 2048-token output cap when supported, and no tools.
- [Grok CLI main-session compaction](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/compaction.ts) estimates tokens, preserves recent content, updates older summaries, and handles truncation across turns.
- [OpenCode compaction](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/compaction.ts) estimates complete requests, reserves output/buffer space, uses recent content plus a rolling summary, and summarizes without tools. Its default recent budget is 8000 and summary output cap is 4096 tokens.
- [OpenCode overflow recovery](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/runner/llm.ts) retries only before assistant output begins, and excludes the recovered call from the same recovery path.

ARTEX adopts a separate output budget, recent content with rolling summaries, and bounded recovery. It retains norma v0.3.6 structured messages/tool pairing instead of Grok's text excerpts, and does not write OpenCode-style main compaction events into the ARTEX main transcript.

## Request budgets and execution

- Estimate message content blocks by UTF-8 bytes using norma and a 4/3 safety factor. Also count system prompts, tool schemas, and framing. These are estimates, not exact model token counts.
- Side output defaults to at most 8192 tokens, also bounded by the main configuration's output cap. `ARTEX_BTW_MAX_OUTPUT_TOKENS` accepts 256–32768 without changing the default model or main task parameters.
- Input budget is the context window minus output cap and safety margin. Unknown windows use the platform default 200K. The margin is 5%, bounded to 128–8192 tokens.
- Load successful exchanges by ascending ordinal in batches of at most 20. Retain at most 20 verbatim exchanges, using at most one quarter of the input budget and no more than 16K tokens.
- Fold excess exchanges into a rolling summary with historical sources and context times. Historical assistant answers are not new tool evidence; resolve conflicts using the latest main snapshot.
- If main context is still too large, summarize only older messages in its copy and retain at most 8K recent tokens. Never split tool call/result pairs; oversized pairs enter the summary together.
- Split summary inputs safely at UTF-8 boundaries within the remaining window; cap output at 2048 tokens. Do not cache empty, truncated, tool-producing, or oversized summaries. Allow at most 12 summary calls per side request under the same 120-second timeout; fail explicitly at the cap.
- Only the first context overflow before any text/tool output permits one reduced retry. Stop immediately if the estimate does not shrink. Other errors and partial streams do not trigger recovery.
- Accumulate all obtained usage, including summaries, failed attempts, and cancellation, into the same request. When the Provider omits usage, record zero; do not present estimates as actual usage.

## Persistence and interface

`side_question_sessions.memory` stores the old-exchange summary, covered ordinal, and main-context summary keyed by snapshot identity. `side_question_requests.context_info` stores preparation phase, actual replay count, summary usage, and budget estimates.

Save summaries only while the original request is running and its clear generation matches. Clear also removes caches; late writes cannot restore cleared data. New snapshots do not reuse old summaries. Summary fields are included in v3 archives; restore empty objects for older v3 archives without them, preserving v1/v2 compatibility.

POST accepts and returns the request before preparation/compaction runs in the background, without holding admission locks or database transactions. SSE/history show preparing, summarizing history, compressing the snapshot copy, and answering phases. Persist compression failures as terminal failures. The frontend retains the error and restores the failed question draft without a floating notification covering the input; history polling no longer clears submission errors.

## Validation record

- Automated checks passed for 19/20/21/50 exchanges, preserving conclusions beyond 20 exchanges, and reusing summary caches after restart.
- Automated checks passed for long multibyte answers/code context, chunk budgets, tool pairing, unchanged snapshots, and invalidating caches for new snapshots.
- Automated checks passed for summary failure/cancellation/truncation/oversize/tool responses, clear races, call caps, one overflow recovery, and no retry after partial output.
- Isolated PostgreSQL checks passed for pagination, restart, v1/v2/v3 archives, summary/budget metadata restore, older v3 missing fields, and 20 parents sharing four concurrency slots.
- Candidate Go service build, frontend TypeScript, component Biome checks, and an isolated Next.js production build passed.
- The built-in browser used isolated UI fixtures at 1280×720 and 390×844 to verify preparation, summary scope notices, failed-draft recovery, no floating error notifications, no horizontal overflow, and clean console logs. Temporary fixtures were removed.
- Read-only replay of existing local Worker snapshots passed the new budget check. For example, Worker #3's 293085-character snapshot was no longer rejected by local character counting. No external model call was made.
- Automatic approval review rejected sending private Worker snapshots to Grok for a real conversation test. That test did not run and is not counted as passed.
- At the user's request, the local backend restarted on 2026-09-11 at 00:37 using the existing database, data directory, and login configuration. Running and candidate binaries had matching SHA-256; backend and frontend proxy `/api/health` returned healthy.

Validation commands (isolated test database only):

```sh
go test -race ./sidequestion ./db ./server -run 'TestSide|TestCheckpoint|TestSnapshot|TestBuildRequest|TestService|TestMainSide|TestTaskArchive' -count=1
go build ./cmd/artex
npx tsc --noEmit
npm run build -- --webpack
```

The frontend production build used a separate copy to preserve the preview's `.next`. The candidate service at `/private/tmp/artex-btw-budget-candidate` was copied to `/private/tmp/artex-btw-preview/artex` and started. The original binary was retained there as `artex.before-context-budget`.
