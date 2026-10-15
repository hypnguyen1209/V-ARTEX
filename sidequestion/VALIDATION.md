# `/btw` validation record

Date: 2026-09-10. Branch: `codex/btw-side-question`. Baseline: `8dae851b9b622f2ff2631f332fde9719d0b16fba`.

Used an isolated PostgreSQL test database and data directory. Real model credentials were injected only into the test environment, never committed here; the product's default model was unchanged. Go 1.26.3, norma v0.3.6, Next.js 16.2.9.

Model conversations, returned objects, engineering assertions, and Qwen's original review are recorded in [validation-2026-09-10.json](validation-2026-09-10.json), without API credentials. Human-readable excerpts in that fixture have been translated into English.

## Engineering checks

| Scope | Result | Evidence |
| --- | --- | --- |
| Structured message/tool argument deep copies | Passed | `TestCheckpointDeepCopyAndBoundaries` |
| Summary/compaction excluded; complete replies and terminal states published; partial replies excluded | Passed | `TestCheckpointDeepCopyAndBoundaries`, `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Actual pool member identity | Passed | `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Tool pairing, 20-exchange replay, budget trimming and overflow errors | Passed | `TestBuildRequestCompactionToolPairingAndBudget` |
| Main/side concurrency and independent cancellation | Passed | Blocking Provider, `TestMainSideConcurrencyAndIndependentCancellation` |
| No tool execution, streaming/non-streaming and failure usage | Passed | `TestServiceNoToolsAndUsageOnFailure` |
| Real norma ChatAgent/local Read tool; transcript/activity isolation | Passed | `TestSideActualChatCheckpointToolResultAndTranscriptIsolation`, streaming and non-streaming |
| Persistence, pagination, idempotency and partial answers after restart | Passed | `TestSideHistoryIdempotencyPagingAndRecovery` |
| Clear/late-write races, parent deletion and version ordering | Passed | `TestSideClearLateWritersAndDeletedParent` |
| MainAgent/Worker archive restore, v1/v2/v3 | Passed | `TestSideTaskArchiveVersions` |
| Three parent interfaces, authentication, ownership and logical Worker deletion | Passed | `TestSideHTTPGlobalLimitTaskWorkerAndDeletion`, `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Busy main session, independent SSE reconnect/disconnect, cancel and clear | Passed | `TestSideHTTPBusyIsolationClearAndReconnect` |
| One request per parent, four globally | Passed | Both `TestSideHTTP…` cases |
| Snapshot persistence before admission, restart continuation, no fabricated old-session snapshots | Passed | `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Reject deleted or changed cached model configurations | Passed | `TestSideRejectsDeletedOrChangedCachedProfile` |
| Cancel/drain final answer and usage before archival | Passed | `TestSideTaskDrainPersistsBeforeArchive` |
| Record usage once on early streaming-consumer cancellation, attributed to side request | Passed | `TestSideUsageRecordedOnceOnConsumerCancellation` |
| Restored Worker/deadline runtime publishes fresh snapshots | Passed | `TestSideRestoredWorkerRuntimePublishesNewCheckpoint` |
| Related package race checks | Passed | Commands below |
| TypeScript and production build | Passed | `npx tsc --noEmit`, `npm run build` |
| New frontend module Biome checks | Passed | `biome check`, three new modules |

Reproduce with `ARTEX_PG_DSN` pointing only to a disposable test database:

```sh
go test -race ./agent ./db ./server ./sidequestion ./llmrec ./llmpool \
  -run 'Test(Side|Checkpoint|Snapshot|BuildRequest|Service|MainSide|CaptureRun|TaskArchive|CompleteForwards|StopIntent|CancelIntent)' -count=1
cd web
npx tsc --noEmit
npx biome check src/lib/side-questions.ts src/hooks/use-side-questions.ts src/components/side-question-workspace.tsx
npm run build
```

The full Go regression run was not entirely green. Two existing server tests failed during temporary-directory cleanup with `TempDir RemoveAll … directory not empty`:

- `TestInheritedActivityDetailAndRelationDeletion`
- `TestTaskMetadataPatchReturnsRenameAndPin`

Exporting the unchanged baseline and rerunning server in the same isolated environment reproduced both cleanup failures. The baseline also failed a target-node-count assertion in `TestCoreTaskLifecyclePG`; the final server regression did not. Other packages and the side-question/race checks passed. Baseline failures were not counted as acceptance passes or hidden by changing assertions.

Next.js emitted an existing multiple-lockfile/workspace-root inference warning; the build completed and all pages generated.

## Browser checks

Codex In-app Browser connected to isolated local Go and Next.js servers. Desktop and 390 × 844 checks covered:

- Entering `/btw` while normal chat runs, with main/side content visible and a working desktop sidebar.
- Follow-up questions and stopping with partial answers retained while the main workflow continues.
- Requests continuing after panel closure, completed answers restored on reopen, and history restored with empty `/btw` after refresh.
- Working narrow-screen Drawer input, buttons, history and close control, without horizontal overflow.
- Confirmed clear removes side history while retaining the main transcript and snapshot.
- Separate questions/history for task MainAgent and two Workers while switching between them.
- A blocking local fixture keeps a Worker running. Submit `/btw` from its main input, stop the side request, and retain its partial answer while the Worker remains live with its own pause button.
- No browser errors or warnings.

Controlled fixtures verify concurrency timing independently of real model speed. Two initial Worker checks had no valid overlap because the task or answer ended too soon; corrected fixtures were rerun successfully. Those initial attempts were not counted as passes.

## Real model conversations

First probe: `grok-4.6` via OpenAI-compatible `http://127.0.0.1:12580/tingly/openai`. HTTP 200 returned model `grok-4.6` and `READY` in 2.82 seconds. Since the preferred endpoint worked, Tingly `glm` and Zhipu `glm-5.3` fallbacks were not used or validated.

| Scenario | Actual result |
| --- | --- |
| Ask asset/goal/marker during main execution | Returned `redhaze.top`, homepage read/summary goal and `BTW-REAL-0910`; completed in 16.97 seconds |
| Ask tool evidence after homepage read | Correctly cited WebFetch 200, curl 301 → 302 → 200 and title; 7.24 seconds |
| Ask side request to create a file with Bash | Refused; target file absent; 7.74 seconds |
| Completed side request leaves main context unchanged | Main transcript SHA-256/activity unchanged; zero side tool executions |
| Continue after stopping/restarting Go service | Retained three exchanges and answered asset/marker/title from saved snapshot without rerunning main Agent |
| New non-streaming Grok session | Correct asset and `ATOMIC-0910`; persisted usage: input 11734, output 138, cache_read 11520 |

The main conversation used WebFetch and Bash/curl to read a public homepage, landing at `https://id.redhaze.top/home`. The title translates to “RedHaze Technology RedHaze Group · Global Group Portal”. Bash saved the response in a local test file; it did not write remotely. Verify this separately from the absence of side-request tool execution.

Main transcript checksum: `e7e61f135a4a120954b539f357e8c4205d7d5cd7460dcaf3dc0fd066463e1d00`.

**Usage limitation:** Tingly's Grok stream returned no usage. A separate direct probe with `stream_options.include_usage=true` returned HTTP 200, 12 data frames and zero usage frames. Thus streaming usage zero means the endpoint supplied no usage, not no billing. Non-streaming and fixture failure/cancellation usage persisted correctly.

## Qwen review

Review model `qwen-flash`, OpenAI-compatible endpoint `https://dashscope.aliyuncs.com/compatible-mode/v1`, HTTP 200. It received the first three real side conversations, main tool evidence, and engineering assertions. It returned `verdict: accept` and `concerns: []`, finding answers consistent with assets, markers and homepage evidence, and tool refusal consistent with constraints. Usage: prompt 6625, completion 312, total 6937.

This review did not include the later restart and non-streaming tests. Qwen's “no writes” summary was too broad: main curl did create a local response file, explicitly recorded above. Engineering assertions establish concurrency, zero side tool execution, and transcript isolation; model review only supplements answer-quality assessment.
