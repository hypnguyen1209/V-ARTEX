# Changelog

Notable changes to this project are recorded here, following [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/).

## [Unreleased]

## [0.3.15] - 2026-10-07

### Interception

#### Added

- **Added a built-in Delete API Paths interception rule**: The existing destructive HTTP rule recognized only the DELETE **method**, but many applications delete data through GET/POST; calls such as `curl 'http://t/api/user/delete?id=1'` can delete real target data. A new `deny` rule covers `/delete /del /remove /unlink /erase /destroy` (the verb must be followed by a separator, avoiding false matches for `/delivery`, `/details`, and `/delta`). Existing instances receive it on upgrade; disable or delete it under System → Command Interception.
- **Separate token usage for model fallback approvals**: Approval calls previously had no separate usage attribution. A usage card below Model Fallback Approval in System → Command Interception now shows cumulative calls, input, output, cache reads, and cache writes, plus a daily usage mini bar chart for the last 30 days.

### Vulnerability Notifications

#### Added

- **Send vulnerability findings to messaging channels**: Added System → Notifications, supporting **DingTalk, Feishu, WeCom, generic Webhook, Telegram, and email**. Each type can have any number of instances with independent enablement, rate limits, and filters.
- **Real-time and digest delivery modes**: Real-time mode sends each match immediately; digest mode combines findings on a global interval (default 30 minutes), headed by the new finding count and severity distribution. Configure two channels for high-severity real-time alerts and digests for the rest.
- **Four filter dimensions**: Minimum severity, task / asset scope, vulnerability-type keyword inclusion and exclusion (exclusion takes precedence), and disposition changes (off by default). Messages include a View Details button using the global Backlink URL; leaving it blank omits the button.
- **Delivery history and manual retry**: Lists each delivery's status, attempts, failure reason, and channel, with channel and status filters. Failed entries can be resent in one click, resetting their retry count.
- **Masked channel credentials**: Webhook URLs, signing secrets, Bot Tokens, SMTP passwords, and similar fields display only masked values with a trailing identifier. Submitting an unchanged mask keeps the value; clearing the input deletes the field.

#### Fixed

> These findings came from a security audit after the initial implementation. Each was reproduced, fixed, and covered by a regression test.

- **Fixed bypassing masks by changing the destination while retaining credentials (critical)**: Configuration merging preserved all omitted keys. Changing only the address silently made the server send stored credentials to any destination. A changed destination now requires every credential field to be explicit: supply a new value or an empty value to remove it.
- **Fixed unescaped untrusted content in Markdown channels**: Model-generated vulnerability titles and summaries, and asset URLs from scans, could render as clickable external links in DingTalk / WeCom / Feishu. Image syntax could trigger client requests that exposed readers' IPs. Content is now flattened to one line and Markdown metacharacters are escaped at each channel's rendering boundary, rather than in the shared title function.
- **Fixed SSRF through delivery addresses**: Loopback, internal, and cloud metadata addresses previously accepted deliveries; the first 200 bytes of a failed response entered `last_error` and delivery history, creating a semi-blind read primitive. Protection now applies at **dial time**, covering DNS rebinding, and cross-host redirects are rejected. Loopback and link-local addresses require explicit `ARTEX_NOTIFY_ALLOW_LOCAL=1`; RFC1918 private networks remain allowed for common self-hosted internal SMTP relays.
- **Fixed notification credentials leaking through errors**: DingTalk's `access_token`, WeCom's `key`, and Telegram's bot token appear in URLs. Failed deliveries exposed the full URL through `last_error`, delivery history, server logs, and frontend messages. Redaction now retains only `scheme://host` and the underlying cause.
- **Fixed silent loss when truncated digests marked the whole batch delivered**: Omitted findings appeared neither in the message nor in failures, while history showed success. Digests now pack **whole entries**, mark only included entries delivered, and requeue the rest for the next message without consuming retries. The header reports how many remain.
- **Fixed minimum-severity typos silently disabling filtering**: An unknown `min_severity` such as `hgih` had ordinal 0, making the check always true and sending every finding when users expected only high severity. The write path now validates values and lists allowed options.
- **Fixed unreachable `rate_per_min = 0` (unlimited)**: The database layer silently replaced explicit 0 with the default. Defaults are now applied at the API layer only when the field is omitted.
- **Fixed digest channels bypassing the token bucket**: `rate_per_min` previously had no effect in digest mode. Claims are now bounded by both the current allowance and the memory limit.
- **Fixed batch-wide failure decisions penalizing new deliveries**: A delivery with two prior retries could push new entries in the same batch directly into failed, permanently losing them before any retry. Decisions are now made per entry.
- **Fixed unparseable digest snapshots being silently marked successful**: Rendering skipped these entries while the batch marked them successful. They now fail explicitly with a reason.
- **Fixed per-channel delivery batches exceeding the lease duration**: In multi-instance deployments, expired rows could be reclaimed and sent twice, incrementing attempts twice. The per-round limit now derives from lease duration / per-attempt timeout, with assertions enforcing the relationship between these constants.
- **Fixed Telegram truncation splitting HTML entities**: Truncation avoided partial tags but could split entities such as `&amp`, causing the parser to reject the **entire** message. It now avoids both.
- **Fixed temporary SMTP errors being treated as permanent**: Greylisting 4xx responses such as `450` should be retried, but previously failed every notification on its first attempt. Response-code prefixes now distinguish retryable 4xx errors from permanent 5xx errors.
- **Fixed nested submissions storing literal credential masks**: Object fields such as `webhook.headers` must be masked or submitted as a whole. A mask sentinel inside an object was stored as a real value and silently broke authentication. Such submissions are now explicitly rejected.

#### Design Notes

- **Vulnerability transactions perform only a blind INSERT**: `notification_events` and the vulnerability are written in the same transaction for atomic consistency. This INSERT deliberately avoids channel-table reads and user filters, and is wrapped in a `SAVEPOINT`; otherwise, a misconfigured filter could abort the transaction and prevent a high-severity finding from being saved.
- **Deliveries use leases instead of long transactions**: After claiming with `FOR UPDATE SKIP LOCKED`, rows become `sending` and `next_attempt_at` is moved into the future as a lease. Delivery holds no database locks. Rows left by crashes are reclaimed after expiry, recovering without unlimited retries.
- **Rate limiting does not consume retries**: Compute the round's token-bucket allowance before claiming that many deliveries. Reversing the order would charge attempts for entries blocked by rate limits, exhausting the three-attempt budget while merely waiting.

### Test Infrastructure

#### Fixed

- **Fixed permanent test assets left by company ICP attribution cleanup order**: `defer d.Close()` ran before `t.Cleanup`, so cleanup used a closed connection and discarded errors, leaving companies and assets behind and breaking other asset-count tests. Closing now also uses `t.Cleanup`, registered first, and cleanup failures are surfaced.

### Traffic

#### Added

- **Traffic content search, path / status / length filters, and column sorting**: Filter by **response content** (full-text index), **path**, **status code** (exact or buckets such as `2xx`), and **response length range**. Sort by time, status code, or response length; preferences persist locally. Two new indexes apply automatically on upgrade; the Agent tool's query contract is unchanged.
- **Added Clear All to traffic**: Deletes all traffic regardless of filters, removes historical host directories no longer tracked by the index, performs full compaction, and reports freed disk space. Traffic evidence bound to vulnerabilities is stored separately and remains intact.

#### Fixed

- **Fixed deleting traffic without freeing disk space**: Index databases lacked `auto_vacuum` and never shrank; the `contentless_delete` full-text index wrote tombstones on `DELETE` without reclaiming postings, making deletion increase index size (6MB of captured bodies produced a 16MB index that stayed 16MB after deletion). New databases enable `auto_vacuum=incremental`. Deletion triggers background full-text merges and incremental reclamation in chunks (104KB in the same scenario), releasing write locks between chunks to keep recording responsive.

> Upgrade note: `auto_vacuum` must be set when creating the database, so **existing index databases retain the old mode** and incremental reclamation is a no-op (reported at startup). Upgrading stops tombstone growth; use Clear All once to reclaim existing space and convert to incremental mode. Later deletions reclaim space automatically.

### LLM

#### Fixed

- **Fixed custom session headers missing from one-shot LLM calls**: Goal decomposition (round 0) and cold-node compaction are one-shot calls without a transcript store or session id in context. Gateways such as opencode zen returned 400 for the missing header, while later planner rounds worked. Both paths now attach a session id stable per exploration, also attributing previously unrecorded token usage correctly.

### Tools

#### Fixed

- **Fixed missing output truncation for MCP and custom tools overflowing context**: MCP output was never truncated; custom HTTP tools received an empty tool context and used a hardcoded 6000 characters rather than the session limit. Both now use `actool.Capture`, truncating at session `MaxOutputChars` (default 30000) and, when `ToolOutputDir` is set, spilling full output to disk while retaining an initial excerpt and file pointer in the message.
- **Upgraded norma to v0.4.3**: The harness consistently runs `CaptureOnce`, protecting every tool, including future additions, from missing truncation hooks and overflowing context.

### Deployment

#### Fixed

- **Fixed activity feeds failing behind a reverse proxy**: SSE always used `${hostname}:8787`; pages served at `https://domain/` through port 443 tried to reach an unexposed 8787 port and repeatedly reconnected. Behavior now follows `NODE_ENV`: production defaults to same-origin; only `next dev` retains `:8787` to bypass Next.js SSE buffering. The `NEXT_PUBLIC_SSE_BASE` override remains. README now documents reverse proxy deployment.

### Findings

#### Fixed

- **Fixed missing scrolling in the By Asset list**: The outer card had `max-height` but no definite height, leaving the scroll viewport's percentage height unresolved. Large lists overflowed or were clipped without scrolling. The height limit now applies directly to the asset tree's native scroll container and adapts to window height.

### Database

#### Fixed

- **Capped the connection pool**: `database/sql` defaults to unlimited open connections, creating new ones whenever none are idle until PostgreSQL rejects them at `max_connections` (default 100). The cap is now 32; excess queries wait for an idle connection, slowing under load instead of failing. Reduce this cap if `max_connections` is lower.
- **Improved connection reuse**: Default `MaxIdleConns` was 2, closing excess connections after use and repeating TCP and PostgreSQL authentication handshakes on later requests. The idle limit now matches the total cap, with a 30-minute connection lifetime and 5-minute idle timeout to replace stale connections naturally.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.14] - 2026-09-24

### Task List

#### Added

- **Added a Vulnerabilities column to the task list**: Shows critical / high / medium / low counts, coloring nonzero counts by severity for a quick view of each task's vulnerability distribution.

#### Changed

- **Narrowed Description / Goals columns**: Long content is truncated with full text on hover, reducing horizontal space occupied by lengthy text.

### Exploration Graph and Planning Overview

#### Changed

- **Reduced planning overview (graph_overview) size and capped lists**: Recent facts, completed intents, pending intents, cold digests, and confirmed findings now use a latest window plus total count; omitted entries can be queried on demand. This substantially reduces per-round LLM context and long-task growth, also capping cold regions in linked-task overviews.

#### Fixed

- **Removed duplicate reverse edges between digest and finding nodes after cold-node folding**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.13] - 2026-09-19

### Asset Interception

#### Added

- **Added global asset interception rules (blocklist)**: Exact and fuzzy domain / IP / URL matching and CIDR ranges, with CRUD and enable/disable controls under System → Asset Interception. Built-in fuzzy rules block government (`.gov` / `.gov.cn`) and education (`.edu` / `.edu.cn`) sites by default.
- **Asset interception integrated into execution**: Agents check targets before creating intents (`add_intent`) or inserting assets (`insert_assets`). Blocked intents are not dispatched and blocked assets are not inserted; asset details and reasons are returned to the Agent.
- **Added task-level block / allow (allowlist) rules**: Independent of global rules and limited to the task, evaluated block-first. A block match forbids testing; if no block matches but allow rules exist and none match, testing is disallowed. With no allow rules, no allowlist applies. Configure during task creation or manage rules and enablement in task Overview.
- **Task templates support categories and task-level block/allow rules**: Templates save a category and rule set, loading both into the new-task form.

### Operation Review

#### Fixed

- **Tightened model judge output to reduce unintended allowance after truncation**: Reduced the decision comment limit from 500 to 120 Chinese characters and required JSON only, without preamble or code fences. This avoids `MaxTokens` truncation causing parse failures that allow execution under a fail-open model-failure policy.

### Task Archiving

#### Fixed

- **Archives skip symlinks instead of failing entirely**: The archive format supports only regular files and directories; any symlink previously failed the whole task archive. Symlinks are now skipped and logged while other files are archived, without following links or leaving the directory tree.

### Accounts and Compliance

#### Added

- **Added a Usage Terms and Disclaimer dialog before login**: Users must check their agreement before signing in.

### License and Dependencies

#### Changed

- **Adopted the AGPL-3.0 open source license** and expanded README license and disclaimer information.
- **Upgraded norma to v0.4.1**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.12] - 2026-09-17

### Exploration Broadcast

#### Added

- **Added an Exploration Broadcast to task details** (#144): Displays exploration nodes (start/goal/intent/fact/finding/hint/digest) as a timeline, with type filters, keyword search, ascending/descending order, pagination, and auto-refresh. The first page in newest-first order acts as a live feed and refreshes each round. Elsewhere, unread counts accumulate without interrupting reading; N New Updates · Back to Latest returns to the feed. Daily grouping keeps long-task pages easy to date.
- **Each broadcast row displays its node id** (#145) for comparison with the exploration graph and locating specific nodes.
- **Expanded broadcast details show upstream/downstream nodes and anchored assets** (#147): Each entry exposes its source and derived relationships. Hovering linked entries shows a node card (type/status/source/time/summary/payload excerpt); anchored assets appear with type labels and recognizable text. Data arrives with the broadcast page, requiring no extra requests on expansion.
- **Broadcast search supports node ids** (#150): In addition to content / source, enter digits or the displayed form such as #41 to locate an exact node.

### Intent Management

#### Added

- **Intent deletion supports soft and hard deletion** (#149): Pending, running, and paused intents can be deleted with a required reason; select the mode in the confirmation dialog.
  - **Soft deletion (default)**: Marks the intent Deleted and stores the reason in a separate field, retaining the node, all outputs, and lineage.
  - **Hard deletion**: Physically removes the intent and exclusive descendants supported only by it, cascading through output / intent chains to leaves to avoid orphaned data. Deleted intents' token usage is archived under the original date. Shared nodes referenced by other intents, goals, and task-root facts remain. The dialog previews the expected cascade node count.

  Both modes notify the planner that the user deleted the intent, with the reason, and trigger replanning.

### Operation Review

#### Added

- **Approval records support status and decision-source filters** (#139) for quickly locating records.

#### Fixed

- **Pending approvals load independently of history pagination** (#133): All pending requests remain visible while browsing historical pages.

### Agent

#### Changed

- **Reworded the Worker role description as a general cybersecurity platform role** (#138).

#### Fixed

- **Fixed cold-node compaction never triggering**: Connected the Compactor to per-task planners, restoring previously disconnected cold-digest execution.

### Chat

#### Added

- **Chat supports @ references to multiple record types and scroll pagination** (#135): Reference candidates load more records as you scroll.

#### Fixed

- **Constrained long message bubbles to the session panel** (#137), preventing overflow.
- **Fixed Missing Upload File errors before a new conversation was created**: Composer now snapshots `FileList` to an array before clearing the input and invoking the callback. Draft uploads previously waited for asynchronous conversation creation, then found the input-bound `FileList` already empty, omitted the file field, and received backend 400 errors.

### Traffic

#### Fixed

- **The traffic recording proxy now binds only to 127.0.0.1 by default** (#129, #130), avoiding exposure as an open proxy on other interfaces.

### Web

#### Fixed

- **Fixed the missing global header on the statically exported task list**.
- **Added missing linked-traffic mocks to demo vulnerability details, fixing a blank page**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)
- [@dingpotian](https://github.com/dingpotian)

## [0.3.11] - 2026-09-15

### Operation Review

#### Added

- **Approval records link to their source execution** (#125): Clicking Source opens the original full session, paginates to the exact tool execution, expands its command and result, and centers and highlights it while keeping surrounding messages readable. Manual scrolling stops automatic adjustment. Persistent `tool_use_id` and task mappings locate executions across ordinary chats, task Workers, planners, and segmented Main Agent sessions, with a new scoped call-id index. Missing, duplicate, or ambiguous links show explicit messages instead of jumping to another execution; deleted sessions, archived tasks, and missing records are also reported clearly.
- **Approval records support pagination** (#115).

#### Changed

- **Reduced model-review input** (#124, #125): The fallback LLM judge now receives versioned JSON containing the current complete tool call, explicitly selected brief context, and local working directory. Context uses only real user messages (the current chat / task Main Agent user message); Workers no longer include intent summaries and clear context inherited from parent Agents. Planner and automatically triggered sessions do not fabricate user messages. Task descriptions, goals, operation constraints, global exploration state, historical calls, and complete Worker intents remain available for execution and independent session auditing but are excluded from operation review. All decisions require JSON `decision`/`comment`; comments must describe the actual operation, consequences of success, and matched rules. Instructions inside arguments or context cannot change review policy. Actual model input snapshots and fingerprints are saved; older snapshots retain their version and are never reconstructed from current data.

#### Fixed

- **Fenced model decisions no longer silently allow execution** (#126): Decision JSON wrapped in ``` code fences previously failed parsing and could silently allow execution under the model-failure policy. Fences are now stripped before parsing; only remaining parse failures use the configured failure policy.

### Agent

#### Added

- **Added experimental noa context compaction** (norma upgraded to v0.4.0): Opt in through System Settings; disabled by default. noa (model-driven context compaction) replaces built-in compaction for Main Agent, planner, Worker, and chat. Original compacted content is persistently archived under `<workDir>/noa/<sessionID>/`, centrally grouped by globally unique session id rather than scattered across task directories. Integration failures fall back to built-in compaction without interrupting tasks. The switch is read once per run and affects only subsequently started runs.

#### Fixed

- **Exploration graph tools reject calls without task context instead of panicking on nil**: Such calls now return explicit errors rather than dereferencing a nil store.

### MCP

#### Added

- **Added legacy SSE MCP support** (#117) for servers offering only the older SSE transport.

### Traffic

#### Fixed

- **Traffic search matches hosts with port awareness** (#114): `traffic_search` includes ports in host matching to avoid mixing records for the same host on different ports.
- **Failed `traffic_search` description migrations no longer interrupt later reporter migrations**: Individual migration failures are isolated.

### Web

#### Changed

- **Added separators between task retest vulnerability options** (#122) for clarity.

#### Fixed

- **Fixed the entire demo task details page crashing**: In mock mode, Sessions requested `GET /api/tasks/<id>/side-questions` for side-question history. The missing mock route fell through to the generic collection response `[]`, leaving `data.items` as `undefined`; the side-question hook's `merge()` threw `TypeError: t is not iterable`. Inside the `setItems` updater, React deferred the exception to rendering, beyond the caller's `catch`, so the page error boundary displayed “This page couldn't load”. The mock now explicitly returns empty side-question history; `sideAPI.history` also normalizes non-array `items` to `[]` for defense in depth.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.10] - 2026-09-13

### Networking

#### Added

- **Added official DeepSeek as a web search source**: Reuses the active LLM configuration. Unlike the other three sources, DeepSeek has no directly callable search API; search runs server-side inside its Anthropic-compatible API through `web_search_20250305`. It therefore **requires an official DeepSeek endpoint and the anthropic protocol** (OpenAI-protocol endpoints reject server tools). Each search **consumes an additional model call**, **bypasses the search outbound proxy**, and **is not traffic-recorded**. Results contain **only titles and links**, without summaries; use WebFetch for page bodies. Settings explain these limits but **do not enforce validation**; users can verify compatibility with Test Search.

### Agent

#### Added

- **Workers can review other work traces**: Added `search_all_worker_traces` to search steps across all work in the task by keyword without knowing intent_id first, and `get_worker_trace` to list a selected work's steps, search within them, and fetch full content by step_id. This reuses observations that other workers saw but did not record as facts, avoiding repeated work.
- **Made `node_detail` available to workers**: Workers can use intent_id / node id from trace tools to retrieve full node details.
- **Planning rounds triggered by `add_hint` are announced explicitly**: Hints previously appeared only in the overview for the planner to notice. Each `add_hint` now records one trigger announcing N new human strategic hints (one per batch, avoiding repeated messages), identifying the round's trigger and including the hint content.

#### Changed

- **Relaxed global-state prompt wording**: Encourages broader exploration and prompt reporting of cross-intent leads rather than premature convergence.
- **Simplified worker boundary wording**: An initial obstacle does not mean exploration is complete; exhaust bypass options within the intent before concluding.
- Removed each asset's `related` input from `insert_assets`: It only determined task-scope inclusion and was not persisted, becoming irrelevant on re-registration and invisible in the UI. It added a model judgment that could not be retained.
- **Test scope (`task_scope`) no longer depends on the asset coverage switch**: `insert_assets` always adds automatic scope (`source='auto'`), and `add_task_scope` remains available to planner / task Main Agent / goal decomposition Agent. Scope defines authorization and asset-query filtering; the coverage switch should only control its use as the metric denominator. Previously, disabling coverage disabled both `auto`/`agent` write paths, leaving only manually added `task_scope` rows. `list_untested_assets` remains hidden when coverage is disabled because it is specific to coverage.

#### Fixed

- **Fixed assets leaking across task boundaries** (#59): `list_assets` hardcoded task id 0 and queried the entire shared store, with no usable scope filter for the model, which treated other tasks' assets (especially IPs) as targets. It now returns only assets within this task's or directly linked tasks' test scope (`task_scope`), matching **ownership**, not literal values: a root domain includes its subdomains/services/endpoints; a network range includes hosts and services. Direct id lookup also excludes out-of-scope assets. Non-task contexts (Auto/pentest) still fall back to the full store. Also fixed attribution of direct-IP URLs such as `http://1.2.3.4/api` to network ranges. The UI Tested Assets view, filtered by task producer, is unchanged.
- **Fixed workers losing cross-work trace tools**: An older tool-reduction migration unbound `search_all_worker_traces` / `get_worker_trace` (and `node_detail`) at startup after their default binding. They are now removed from that unbinding list, with a one-time rebind for databases that already ran the migration.
- **Fixed intermittent `/btw` side-question persistence failures**: Strips NUL (`\u0000`) escapes unsupported by JSONB before saving side_question checkpoints.

### Triggers

#### Fixed

- **Merged trigger sessions deduplicate descriptions/goals per task**: Multi-task triggers no longer repeat the same task's description/goals in messages, preventing lengthy repeated goals from overflowing context.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.9] - 2026-09-11

### Agent

#### Added

- The task Main Agent supports **multiple sessions**, with creation, switching, independent context reset, and interaction in each session.
- Added persistent `/btw` **side questions** without interrupting the main thread; questions and answers survive restarts.
- Added `source_task_ids` to `spawn_task`, allowing new tasks to **inherit assets and findings read-only** from source tasks.

#### Changed

- Disabled worker cross-engagement memory and reduced default tools: Context reading and cross-work review belong to planning; workers execute and write back one intent only.
- Simplified default worker prompts; removed `terminated` and `worker_name` from `get_worker_output`; simplified `insert_assets` / `list_assets` descriptions.

#### Fixed

- Fixed work ending prematurely after **idle turns**: A model may spend a whole turn reasoning without text or tools. The harness saw a natural end (`end_turn` without `tool_use`) and returned `completed` with an empty summary, stopping an unfinished intent. None of the five retry layers caught this: it was not an error; same-provider safe-window retries required stream failure; the circuit breaker treated `err == nil` as success; intent reruns required `model_error`; SDK empty-response detection counted reasoning deltas as yielded events. The work Stop hook now detects these turns and injects a continuation instruction, retaining prior reasoning for the next step. Identical replay was deliberately avoided because this behavior is usually stable for a given prompt and context and would merely repeat the reasoning.
- Reuses the **Empty Response Retries** setting in LLM → Retry & Backoff, default 2; `-1` disables it and restores ending on idle turns. Both features address completion without substantive output, with different detection and recovery. This is a **total per-intent** limit, not a consecutive-turn count: the harness nudges consecutive idle turns only once, refreshing that allowance only after a real tool turn. The total limit prevents tool → idle → nudge → tool → idle loops from consuming the intent budget. Trigger logs read `[work <worker> · #<intent>] Idle turn…injecting continuation instruction (n/N)`. See the historical LLM retry design note, `docs/llm-retry-design.md` §1.1.
- Fixed `/btw` context budgets and input layout in long conversations; side-request ids fall back to an alternative generator in insecure (non-HTTPS) contexts.

### Traffic

#### Added

- Vulnerabilities support **multiple linked traffic evidence entries**, with ordering, notes, and roles (request/response/corroboration).
- The report Agent **automatically links** relevant traffic evidence before writing reports.
- Added an Agent **traffic-binding switch** and completed evidence handoff between Agents.

### Tasks

#### Added

- Added **session-level vulnerability retests** in separate Agent sessions, with runtime status in lists and details.

### Interception

#### Added

- Added **details and execution audits** to approval records: tool request context, initial model/rule decisions, execution output, and argument fingerprints.

### Assets

#### Added

- Task test assets support **DSL search**.

### LLM

#### Fixed

- Fixed connection tests omitting custom session headers and causing opencode zen to return 400.

### Networking

#### Added

- MCP HTTP transport supports **skipping TLS certificate verification** for services with self-signed certificates.

### UI

#### Added

- Session lists group by Agent, with expansion/collapse and independent pinning; chats support Agent filters.
- Attack-chain graphs render digest nodes and collapse their members.

#### Fixed

- Fixed blank pages caused by unsynchronized login credentials.
- Interception messages explicitly identify Platform Controls to avoid confusion with target-side defenses.

### Deployment and Updates

#### Added

- Added **one-click updates in the UI**: A Version & Updates card in System Settings and a top-bar notice for new versions. The flow downloads the release → verifies `SHA256SUMS` → smoke-tests → stages → exits so the supervisor can replace the binary and restart, then refreshes the page.
- Added supervisor scripts `start.sh` / `start.bat` as the official startup entry points, included in releases and Docker images (`install.sh` unchanged). They restart based on exit codes and forward SIGTERM to artex. Verification and replacement logic stay in Go, keeping scripts simple.
- Automatic failure recovery: Failed verification or smoke tests discard the replacement and keep the current version. Three consecutive startup failures roll back to the previous version. Settings also offer manual rollback; database schema changes are not rolled back.
- Updates accept only GitHub domains and require HTTPS, with a fixed release source. Development builds disable one-click updates. GitHub queries are cached for 30 minutes to prevent top-bar checks from exhausting API quotas.

#### Known Limitations

- In Docker, updates replace only the program; toolchains stay unchanged and container recreation restores the bundled version. Use `docker compose pull artex` when needed.
- Release `skills/` are not synchronized; newly added built-in skills do not become available automatically.
- Updating restarts the program and interrupts running tasks.

### Dependencies

#### Changed

- Upgraded norma to v0.3.7.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.8] - 2026-09-09

### LLM

#### Added

- Added a Retry & Backoff tab to LLM settings, configuring **counts** and **intervals** for five retry layers. Failures pass from inner to outer layers: connection retries (SDK, connection reset / timeout / 429 / 5xx before streaming), empty-response retries (SDK, normal completion with no content, openai format only), same-provider safe-window retries (stream replay before any output reaches the caller), pool circuit breaking (cooldown after consecutive failures reach a threshold), and intent reruns (rerun an entire worker intent after model_error). Each inner layer must exhaust first. Each layer has two settings: blank keeps default counts and exponential backoff; a count overrides the count; an interval replaces exponential with fixed backoff; -1 disables that layer. The first three are endpoint-specific, with per-field profile overrides of global defaults (overriding only the interval still inherits the global count). Circuit breaking and intent reruns are process-wide global settings. Saved changes apply without restart. Leaving all blank preserves existing behavior byte-for-byte on upgrade (new columns default to 0; absent settings keys use defaults). Connection tests deliberately omit these parameters because user retries could exceed their 30s hard timeout and falsely fail working endpoints. See the historical LLM retry design note, `docs/llm-retry-design.md`.
- Each LLM profile supports a custom **session header** (`session_header_key`). When nonempty, every LLM request includes this HTTP header with the current run's session id (`conv-<id>` for chat, `exp<x>-worker-i<intent>` for workers, etc.), supporting prompt-caching / sticky-routing gateways. Injection reads the session id from request context without changing norma, allowing shared providers to send different values per session. Includes an upgrade migration and a new field in the LLM profile dialog.

#### Fixed

- Fixed `23502` errors when saving LLM profiles with an empty session header: The column is `NOT NULL DEFAULT ''`; incorrect `NULLIF($n,'')` converted empty input to NULL. The value is now passed directly with empty-string semantics.

### Agent

#### Added

- Wall-clock timeouts now **wrap up in place**: With norma v0.3.6, `MaxDuration` interrupts running tools and continues for configured wrap-up rounds on a live context, saving identified results and a summary with terminal state timeout. This replaces worker/planner external `maxDur+90s` hard contexts that killed stalled runs as `aborted_tools`. Chat inherits the same harness behavior; mainagent has no `MaxDuration` and is unaffected.
- Stall recovery: On heartbeat / unchanged-state wakes with no open or running intents anywhere in the graph, the planner receives an explicit stall warning: no workers are running and no directions are queued, so it must produce one or more distinct new intents rather than 0.

#### Changed

- Refactored worker prompts: Intent, startup instructions, and raw anchored-asset JSON move into the system prompt, rebuilt each round and never lost to compaction. Resuming no longer depends on retaining the first transcript message. The startup user message contains only the global overview, which may degrade or be stale. Per-intent system data sacrifices cross-intent prompt-cache reuse to ensure intents are retained.
- Simplified default planner / worker text and corrected operational issues: Planner handles `recent_done` by status (inspect blocked/exhausted traces before deciding, neither assuming dead ends nor rerunning blindly), treats negative conclusions as tentative observations and checks evidence before accepting them, must create intents when goals remain unmet with no open/running intents, and prioritizes depth over coverage. Workers report negative conclusions as observations with tentative interpretations, leaving judgment to the planner; cross-intent leads go into fact summaries rather than being pursued directly. Reseeding appends and activates new default versions, retaining custom / old versions for rollback.
- Reduced default work-agent tools and asset writeback guidance: Workers execute and write back one intent; planning owns context reading and cross-work review. Removed `list_facts` / `node_detail` / `list_companies` and cross-work tools (`search_all_worker_traces` / `list_worker_traces` / `get_worker_trace`) from defaults, retaining `list_findings` for deduplication before reporting + `add_finding` / `record_fact` + `insert_assets` / `list_assets`. Removed outdated prompt fields conflicting with the `insert_assets` schema (`type=tech`/`on_url`/`props`). A one-time upgrade migration removes these worker bindings without changing planner/main bindings.

### Exploration Graph

#### Added

- **Exploration graph cold-node compaction (cold-digest)**: Folds old, inactive intents / facts into digest nodes in `graph_overview`. Original nodes remain permanently retrievable by id: storage is lossless, only presentation is compressed, and folding is reversible. Temperature uses reverse reachability, with any live branch making a node hot, R=6-round debounce, and connected-component grouping. Background compaction (minor folds uncovered cold regions; major recompresses originals and merges fragments) rechecks activity and uses cooldown mutual exclusion, staying off the hot path and never overwriting revived nodes. Overviews expose `cold_digests` and asset-indexed `cold_index`; `expand_digest` / `expand_index` restore details. Linked / inherited task overviews reuse their own folded views, and `expand_digest` supports read-only cross-task expansion. `expand_digest` / `expand_index` are available only to planner and main agent, not workers. Includes upgrade migrations.
- `graph_overview` now returns the full `finding_list`: Findings are high-value outputs and usually few per task, so the overview includes all confirmed findings rather than a recent window like facts. Planner/worker can see them each round without `list_findings`. Entries use `{id, summary, evidence?, from_intent?, assets?}`, with readable affected assets (url / domain / ip:port) rather than bare ids.

#### Changed

- `graph_overview` no longer lists `hosts`: Removed host lists from coverage (up to 500 host strings per round with little planning value), retaining `host_count`; retrieve hosts on demand with `list_assets`. Added top-level `done_intents_total` alongside `recent_done_intents`, capped at ≤15, so the planner knows when deduplication context was truncated.

### Tools

#### Changed

- Moved the default `add_company_scope` binding from worker to planner: Defining company scope belongs to planning / orchestration / Auto; workers only explore. New databases seed mainagent/planner/auto bindings, with a one-time upgrade migration.
- Planner now binds `list_assets` by default, alongside `list_untested_assets`: `list_assets` searches the full store with DSL; the latter finds untested in-scope assets. A one-time backfill preserves user unbindings.

### Tasks

#### Added

- Added a Running Workers task-list column, counting intent nodes with `state='running'`, matching task Overview and displaying 0 when none are running.

### Networking

#### Added

- Added a **global outbound proxy** for all target traffic (http/https/socks5, supporting `user:pass`). With capture enabled, it is upstream of the MITM recording proxy; traffic is recorded then proxied, and both intercepted and passthrough paths use upstream to avoid exposing the source IP. Without capture, settings are injected into agent Bash and WebFetch (`proxyEnv` adds `ALL_PROXY` for socks5). Stored in settings KV without migration, with a Global Proxy card in System Settings independent of web-search and LLM proxies.

### UI

#### Fixed

- Corrected intent status labels: `exhausted` changes from Exhausted to Budget Exhausted (step / time limits interrupted execution with partial results, not full exploration); `blocked` changes from Intercepted to Execution Error (model / API / network retries exhausted, not target / WAF interception). Added missing `stopped`, Stopped, for manually stopped work.

### Dependencies

#### Changed

- Upgraded norma to v0.3.4: Added MCP output truncation and disk spill (see `651b961`); later v0.3.6 supports in-place wall-clock timeout wrap-up.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.7] - 2026-08-31

### LLM

#### Added

- Added Output Limit to model profiles, with a selectable request field name. It caps tokens generated per reply and is sent with each request; 0 (default) omits the field and uses server defaults. This differs from Context Window, the total model capacity used locally for compaction thresholds and never sent. Field selection applies only to `openai` (Chat Completions): blank defaults to `max_tokens`, supported by most compatible gateways. Official OpenAI reasoning models (o-series / GPT-5) require `max_completion_tokens` and reject `max_tokens` as `unsupported_parameter`; select the new field manually. Other formats fix the name (Anthropic: `max_tokens`; Responses API: `max_output_tokens`), so the control is disabled and cleared on save for non-openai formats. Also connected previously missing output limits in planner / worker / chat / Main Agent / goal decomposition; openai previously sent none and Anthropic used the SDK's 8192 fallback. Limits now resolve from the profile each round like streaming settings, applying on the next round after failover. Upgrade behavior is unchanged (new columns default to 0 and an empty field name).
- Clicking Test now logs each HTTP attempt's status and raw gateway response body (capped at 4K), helping diagnose 401s, quota messages, empty frames, and HTML responses beyond the UI's condensed ok / err. Logging is independent of the LLM Records switch.

#### Changed

- Task LLM chains can now be edited in any state, beyond running / paused / exhausted. Main Agent conversations still use the chain after done / failed / timeout; previously, broken models could not be replaced, preventing further interaction. Removed terminal-state checks from backend HTTP and DB transactions and enabled editing/saving in the UI. Saving terminal tasks no longer reopens quota-blocked intents, which otherwise became open without workers and ineligible for Rerun Intent. Use intent reruns / new goals to resume execution and return the task to running.

### Agent

#### Changed

- Messaging running Workers to steer direction now reuses pause / resume and transcript resume, matching Main Agent chat. This replaces a custom persistent intervention protocol spanning scheduling barriers, recovery, and more than ten activity filters. Messages enter the next round's input; intents run immediately in dedicated goroutines outside the 3-slot worker pool. The UI retains the message box, restores Continue Directly, and sends through SSE. No schema changes. Tradeoffs: Messages remain in memory without crash recovery, and sending may briefly exceed task concurrency by one worker, accepted for this infrequent case.

### Tasks

#### Fixed

- Fixed OOM crashes when archiving large tasks: Snapshots stream to storage instead of loading entire tasks into memory. Also closed three recovery gaps in cold archiving: modern traffic exists only in SQLite, and a crash between PostgreSQL and SQLite commits could strand archived traffic in hot storage without recovery. Staging journals are now written regardless of legacy directory presence, and missing `journal.json` is treated as discardable.
- Fixed sluggishness with many tasks: Optimized task-list, task-context, and exploration queries and reduced duplicate requests in the dashboard, chat, and task details to improve conversation and page loading.

### Assets

#### Changed

- Removed the 256-rule company-scope limit: Companies entering individual IPs / domains easily hit it and had to split into multiple companies, despite rule count not inherently slowing scope handling. Removed the backend, frontend, and demo mock limits; each rule remains capped at 1024 characters and request bodies at 2 MiB (roughly 40,000–50,000 rules).

### Skills

#### Fixed

- Fixed skill archive uploads failing with “Upload failed: zip: unsupported compression”: Go's standard library supports only Store / Deflate. Added pure-Go bzip2 and Zstandard decompressors without new external dependencies. Unsupported Deflate64 / LZMA / XZ / PPMd methods and encrypted archives now produce a Chinese message before extraction identifying the file, compression method, and repackaging steps instead of exposing the underlying English error.
- Fixed skill filename validation rejecting Chinese names: The ASCII allowlist `[A-Za-z0-9-_./]` rejected entire archives containing a Chinese-named path (for example, a localized equivalent of `references/guide.md`) as invalid. A Unicode blocklist now permits multilingual filenames and spaces while still rejecting control characters, invalid UTF-8, zero-width and bidirectional controls (RLO filename spoofing), `\ % # ? * : " < > |`, and `..` / absolute paths / empty segments, preserving traversal defenses. Skill names likewise accept non-ASCII letters including Chinese; ASCII remains limited to lowercase letters, digits, and hyphens under agentskills.io, and spaces, dots, and path separators remain forbidden.
- Fixed garbled filenames or rejected Chinese archives created by Windows compression tools: Such zips omit the UTF-8 flag and encode names in GBK; GBK fallback decoding now precedes path validation. Quoted frontmatter names, such as a localized equivalent of `name: "Chinese Skill"`, also parse correctly.

### UI

#### Added

- Added a default flat All Findings view, switchable with By Task tabs. Grouped views required expanding each task to see vulnerabilities; the flat cross-task table supports 10/20/50/100 entries per page and the same export selection, inline evidence/report expansion, name/category/severity editing, disposition changes, deeper exploration, and deletion. Both share statistics and filters, preserve filters on switching, and persist view/filter preferences locally. Only the active view polls; inline edits update both caches to prevent stale data after switching.
- Added a By Asset findings view: A left tree (company → root domain / IP → subdomain → service → endpoint; companies appear only when assets have owners) selects a whole subtree's findings in the shared table with common filters. Only assets with findings appear, with missing ancestors added as needed. Counts aggregate subtrees and deduplicate findings; findings with deleted or absent assets go into Unlinked Assets. Unlike the other views, this view queries only on entry, filter changes, finding edits, or tree refresh, avoiding needless 5-second navigation-tree recalculation. Labels show relative values (subdomain without root suffix, services as `https :443`, endpoints as paths), with full values in tooltips and breadcrumbs. Excess nodes cause whole endpoint/service levels to be omitted with a notice; counts remain included above.

#### Fixed

- Fixed compressed, unreadable session history on mobile: The session list collapses on narrow screens to give history more space.

### Dependencies

#### Changed

- Upgraded norma v0.3.2 → v0.3.3: Added `Config.MaxTokensField` to send OpenAI Chat Completions output limits as `max_completion_tokens`, required by reasoning models. The two keys are mutually exclusive; only one is sent, defaulting to `max_tokens`.
- Upgraded `golang.org/x/mod` v0.37.0 → v0.40.0, fixing two dependency security alerts.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)
- [@begininvoke](https://github.com/begininvoke)

## [0.3.6] - 2026-08-27

### LLM

#### Fixed

- Fixed non-streaming reverting to streaming when reopened after saving (#69): The profile-list DTO omitted `streaming`, so responses never returned it and the frontend defaulted `undefined` to streaming. Writes and database storage were correct. Restored the field without `omitempty`, ensuring `false` also appears.
- Connection tests now require an actual model reply and use the profile's real transport mode (#65): Previously, HTTP success passed even with no content (reasoning exhausted the budget / safety policy suppressed text / compatibility layers lost `content`), despite silent conversations. Tests also always streamed, testing a different path for non-streaming profiles. Empty replies now fail; successful tests display the reply and honor the current streaming switch.

### Agent

#### Changed

- `list_facts` now paginates and filters by keyword to prevent large fact sets from overflowing context (#74): Defaults to the latest 20, with `limit` (maximum 100), `before` cursor, and `q` summary keyword, returning `{facts, total, has_more, next_before}`. Long summaries are truncated by character count; full text remains available through `node_detail`. Worker / planner prompts reflect pagination. A one-time upgrade migration refreshes the parameter schema in the tool catalog because `SeedTool` inserts only initially; otherwise Tool Management showed No Parameters.

### Tools

#### Added

- Added tool-call statistics to Tool Execution (#72): The Statistics toolbar button opens counts, percentages, and failures per tool, sorted by call count descending. Statistics use the list's task / keyword filters across the full result set, fetching only when the dialog opens.

### Dependencies

#### Changed

- Upgraded norma v0.3.1 → v0.3.2: Restored silently dropped reasoning/refusal in OpenAI responses (adding Responses `reasoning_text`), deduplicated three reasoning field aliases, added bounded retries for empty responses with zero content blocks, and fixed gateway 400s from tool pairs split by compaction boundaries, including existing transcript orphans.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.5] - 2026-08-25

### LLM

#### Added

- Each LLM profile supports streaming / non-streaming (streaming by default): Enabled uses SSE; disabled uses true non-streaming (`stream:false`, returning complete JSON), bypassing faulty gateway SSE implementations such as empty frames or dropped reasoning frames at the cost of live progress and token counts. Worker / planner / mainagent / chat / goals resolve the active profile dynamically. Provider wrappers in `llmpool` / `llmrec` / task runtime all support non-streaming. Added `streaming` to `llm_profiles` with an `ALTER` upgrade (default `true`, preserving old profiles).
- Added OpenAI Responses API profiles: A third format, `openai-responses`, calls `POST /v1/responses` alongside Chat Completions / Anthropic, with normalized `BaseURL` and default model `gpt-5`. The `llm_profiles` `format` constraint includes `openai-responses`, with idempotent upgrade migration; the UI adds OpenAI (Responses API). Upgraded norma to v0.3.1, including a `reasoning_content` return fix.
- Record raw LLM HTTP requests/responses at the transport layer, preserving tool schemas, `tool_use` blocks, and raw SSE frames absent from normalized views. Each norma retry attempt is retained. Added `raw_request` / `raw_response` to `llm_records` with `ALTER` upgrades, plus a Raw view and request/response copy buttons with insecure-context `execCommand` fallback.

### Chat

#### Changed

- Session multi-selection now uses a Select mode: Default rows omit checkboxes; the header shows N Total and Select. Select reveals checkboxes, Select All, and bulk deletion; Done exits and clears selection. Fully successful bulk deletion exits automatically; failures retain selection mode for retry. Per-row rename / pin / delete remain in the ⋯ menu.

### UI

#### Changed

- Improved task management, session actions, and traffic viewing (#57): Persisted task-list column sorting, added direct inline rename icons instead of menu actions, and refined sheet interactions and traffic details.

#### Fixed

- Worker asset labels show only domains and IPs and handle empty labels correctly.

### Agent

#### Added

- Added quantitative acceptance checks to planner goal evaluation: Goals with measurable criteria (X% asset coverage, N flags, specific privileges) must compare actual `graph_overview` values (`coverage.pct`, finding counts, etc.) before `prove_goal`. If unmet, `prove_goal` is forbidden and intents must address the gap; Mostly Complete is not enough to mark met. Fixes goals marked complete at 40% coverage when 100% was required.

#### Changed

- Rewrote planner criteria for producing 0 intents: Calling this the most common and important principle caused premature stopping with unmet goals and untested surfaces. Zero intents now applies only when ① all considered directions are covered by open/running/recent_done intents, or ② the next step depends on outputs of running work that have not arrived (wait for completion and the next graph-change wake). Explicitly warns against stopping merely because 0 intents is common when independent uncovered directions or unmet goals with untested surfaces remain.
- Strengthened worker evidence requirements for negative conclusions: Claims such as Not Injectable / Port Closed / No Login Entry could abandon entire directions. Workers must first exhaust reasonable methods within the intent (alternate encodings/parameters/paths/methods). Incomplete attempts or weak evidence require `confidence=inferred`, avoiding premature `observed` negatives that permanently block routes, especially early in tasks.
- These planner / worker prompt changes are appended and activated as new versions through one-time migrations (`reseedPlannerPrompt` / `reseedWorkerPrompt`, each guarded by a settings flag). Previous versions remain in history so users can restore custom prompts.

### Operations

#### Added

- Added `reset-password.sh` to reset the administrator password (fixed username `ARTEX`), supporting local / docker deployments. Specify connection details or load them from `--dsn`/`$ARTEX_PG_DSN`/`config.json`. Database `pgcrypto` generates a login-compatible bcrypt hash and writes `settings.auth.password_hash`, taking effect without restart. Passwords pass through environment variables rather than process argv and are escaped against injection.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

## [0.3.4] - 2026-08-24

### UI

#### Added

- Multiple selected tasks can change category together, including Uncategorized to remove the current category. Writes use one transaction; only tasks deleted after selection fail.

#### Changed

- New-task categories use a searchable input instead of a dropdown. Typing filters existing categories; Enter or Create adds and selects a new name, shown as a removable tag. Selection remains limited to one category.

### Chat

#### Fixed

- Fixed messages being blocked as LLM Not Configured when a session had a selected profile but none was globally active. Pre-send checks previously inspected only global configuration while execution preferred the session profile. Both now resolve session/Agent bindings first, then global fallback.
- Unavailable-chat messages distinguish states: Add a Configuration when none exists; Activate or Assign a Configuration to This Session when profiles exist but none is active, instead of always reporting Not Configured.

### Agent

#### Added

- For tasks whose goals are all met, Workers directly claim intents created by the Main Agent through `add_intent`, run them, and stop. Planner remains inactive without open goals, avoiding cancellation of newly issued intents after reevaluating completion; the task returns to complete when the frontier drains. Before dispatching, Main Agent asks whether the intent should become a formal goal; registering it restores normal autonomous planning.

#### Changed

- When `step_ids` exceeds the 5-step limit, `get_worker_trace` / `get_task_worker_trace` returns the first 5 full steps instead of an error. `returned_step_ids` / `omitted_step_ids` / `notice` identify included and omitted steps and advise against fetching more if sufficient. Duplicate and invalid ids are removed before counting.

#### Fixed

- Task lists now sort by descending task id (newest first) instead of creation time. Identical timestamps combined with 10-second polling and random map iteration previously made tasks repeatedly swap positions.

### Assets

#### Fixed

- Fixed a single invalid row breaking company-attribution recalculation: Hostnames stored in `assets.ip` by Agents or the asset API made `a.ip::inet` throw `22P02`, rolling back company-scope creation/editing and company deletion. Safe `try_inet()` conversion now skips invalid values instead of aborting the statement.
- Unparseable `ip` values are no longer silently skipped: Saving company scope reports the count, ids, and values of invalid asset `ip` fields and explains that IP/CIDR rules will not attribute them. Server logs carry the same information for paths without frontend responses, including company deletion, scopesentry sync, and Agent writes.
- Task IP/CIDR scope matching also uses `try_inet()`, replacing a character-set regex guard that let hex-letter hostnames such as `abc.def` through and triggered the same error.

#### Changed

- The `ip` field of `ip`, `service`, and `endpoint` assets no longer accepts hostnames. `insert_assets` and the asset API return per-entry errors with `index` and corrective guidance: use `type=subdomain` with `domain`, or resolve A/AAAA records first. Agents can correct and retry; other entries in the batch save normally.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.3] - 2026-08-23

### Worker

#### Added

- Running Workers support individual pause, resume, and cancel. Pausing retains intents, facts, and findings; cancellation transactionally removes the current intent and its direct outputs after execution exits.
- Added the `paused` intent state, execution fences, and named termination reasons to prevent late blackboard writes after pause, cancel, or task deletion.
- Added an optional task concurrency cap; creation, resume, deeper vulnerability exploration, and queue replenishment share a persistent FIFO admission path.

#### Changed

- Increased the default Worker run wall-clock limit from 600 to 1200 seconds; revived timed-out tasks restart the clock on their next actual execution.
- Actual Worker, Planner, and Main Agent execution now drives task status badges consistently, fixing Idle badges while Workers were running.
- Worker sessions retain individual controls and current-session token totals; model names move to the right of the current session title.

#### Removed

- Removed Worker multi-selection, Select All, and bulk pause/resume UI, along with bulk-control APIs and Mock contracts.
- Removed row-level Worker token badges and tooltips; the complete token ledger and task aggregation APIs remain.

### LLM

#### Added

- Tasks support ordered LLM profile chains, switching automatically on explicit provider quota exhaustion and persisting the current profile, exhaustion state, and error summary.
- Running and paused tasks can edit the full chain, reorder profiles, and switch the current profile manually, effective on the next LLM call.
- Automatic switches, manual switches, and full-chain exhaustion create structured system activity and deduplicated task-feed notifications.
- Added a task-role model resolution API, consistently resolving the profile and model for Main Agent, Planner, and Worker next calls.
- Added an optional global LLM Pool with call ordering, fallback for specified model failures, health status, cooldown recovery, and manual reset.
- Added an always-on LLM usage ledger aggregating input, output, and cache tokens by task, session, model, and profile.

#### Changed

- Goal decomposition, Planner, Worker, and Main Agent share task-level LLM runtime; resolution precedence is described below.
- Failover responds only to explicit quota, balance, or billing errors; ordinary rate limits, authentication, network, server, and context errors do not trigger mistaken switches.
- LLM configuration uses profile cards and a right-side editing drawer, with Pool rotation order, priorities, exclusions, health, and recovery controls.
- Chain-exhaustion messages wrap on narrow screens; current-model icons in session lists become text labels beside the current session title with full tooltips.
- Task roles resolve LLMs in order: Agent binding → task profile chain → global. Explicitly bound roles always use their bound model; unbound roles use the task chain, falling back globally only when the chain is empty.
- Empty outbound proxy settings mean direct access, no longer falling back to `HTTP_PROXY`/`HTTPS_PROXY` (explicit `ARTEX_LLM_PROXY` is unchanged). Proxy inputs support authenticated `socks5://user:pass@host:port`.

#### Fixed

- Transient streaming failures before any output is committed safely retry with backoff on the same provider, reducing runs that stop after a few tools without a summary as `model_error`. Quota exhaustion, oversized context, and deterministic 4xx errors are not retried; failover and compaction recovery retain their respective responsibilities.

#### Removed

- Removed LLM icons from session lists to avoid confusion with Worker status icons.

### UI

#### Added

- Tasks support global category creation, renaming, deletion, and filtering; new tasks can select a category directly.
- Added global task-template CRUD and a right-side management drawer; new tasks can load preset descriptions/goals or save current content as a template.
- New tasks can link multiple source tasks and company asset scopes. One multiline input recognizes domains, URLs, IPs, CIDRs, ICP registrations, and company keywords.
- Task test assets support live addition/deletion and track manual, company, inherited, and Agent-discovered origins. Worker session titles show current test assets and an origin summary.
- Findings group by task with independent group pagination, allowing vulnerability descriptions and high-priority deeper-exploration intents.
- Conversations support rename, pin, unpin, and delete; task lists support bulk pause/resume on the current page.
- Traffic and tool execution details use right-side drawers; HTTP messages include missing `Host` and highlight request lines, status codes, headers, JSON, and markup bodies.
- Added Details and Pause/Resume task-list actions; session send shortcuts are configurable in System Settings (`Enter` / `Cmd+Enter`, etc.), and web search adds a proxy input.
- Session-title badges show LLM profile names, with model IDs in hover tooltips. Optional task names gain a list column and search, falling back to descriptions when blank.
- Skills show usage counts, last-use times, and missing-dependency statistics to identify inactive or uninstalled skills.

#### Changed

- Task titles are focusable detail links; task assets, company assets, finding groups, and grouped vulnerabilities use real server pagination, stable sorting, and exact totals.
- Company creation uses a right-side drawer with multiline scope input, live recognition, validation, and type previews.
- Main Agent input grows automatically, sends with `Enter`, inserts newlines with `Shift+Enter`, and avoids accidental sends during Chinese IME composition.
- Agent previews, task reports, and related details share Markdown rendering; fixed deletion confirmations, long errors, and mobile drawer widths consistently.
- Task-template selectors display/search names and submit IDs; restored original font sizes and standardized the displayed app version as `0.3.3`.
- Task and current-session token totals highlight only input, cache-read, and output values; the send button uses a simpler upward arrow.
- Task Overview test scope shows company names rather than IDs.

#### Fixed

- Ordinary text containing dots is no longer mistaken for an ICP registration number.
- Fixed duplicate keys in Agent editor variable lists when catalog names collided with global runtime variables such as `{{.Now}}`.

#### Removed

- Removed separate Enter buttons from task cards; click the task title to open details.
- Removed the dashboard New Task button, company-creation Logo URL input, and vulnerability-count badges from the findings header and task groups.
- Reverted the global 10% font enlargement and removed Worker checkboxes, model icons, and row-level token statistics from session lists.

### Agent

#### Added

- New tasks can link multiple existing tasks, inheriting direct-source goals, facts, findings, completed intents, asset scopes, and blackboard context live and read-only.
- Blackboard read tools query source-task nodes, facts, findings, and execution traces on demand. Inherited nodes carry source labels and all write tools reject changes to them.
- Company-scope tools support domains, URLs, IPs, CIDRs, ICP registrations, and company keywords. Keywords guide Agent scope only and do not drive automatic attribution.
- Deeper vulnerability exploration creates high-priority manual Worker intents in the original task, with asset anchors and `derived_from` edges.
- Added Goal Management to Overview for viewing, adding, editing, and deleting goals. Adds/edits notify Planner and revive the task; deletion hard-deletes the goal and cascades edges/anchors without revival.
- Main Agent binds `steer_work` by default to inject corrective instructions into running Workers without interruption or lost progress, still validating that the intent belongs to the task.

#### Changed

- Source-task intents do not enter the new frontier; new tasks retain independent exploration, execution queues, working directories, and conversation histories.
- Deleting running intents no longer destroys data: Stops them as `stopped`, requires a reason, attaches it as a fact and in payload, and notifies the planner through a `cancelled` trigger while preserving intent content and reason.
- Main Agent chat is driven by server activity streams and restores input after send failures. Opening sessions scrolls to the bottom and keeps the last reply visible after lazy-loading details.
- Pausing a task terminates current Main Agent, Planner, and Worker calls, but permits users to start new Main Agent orchestration sessions while paused.
- Cancellation, shutdown, and stream interruption consistently retain true termination reasons, generated content, rounds, duration, tokens, and unanswered tool calls.

#### Removed

- Removed optimistic Main Agent client messages to prevent duplicates and cross-session streams during pauses, failures, or concurrent activity.

### Constraints

#### Added

- Goal decomposition extracts operation constraints (allow/deny) before splitting goals; Main Agent can add constraints at runtime. Constraints enter Planner and Worker system prompts at highest priority; debiased diversity and breadth exploration explicitly obey them.
- Added Constraint Management to Overview with create/edit/delete APIs. Constraint injection can be toggled separately for Planner / Worker, both enabled by default and reread every round for immediate effect. New `task_constraints` uses `CREATE TABLE IF NOT EXISTS` for automatic upgrades.

### Interception

#### Added

- Added model fallback approvals beyond regex/string command-interception rules. With no rule match, the model judges `ALLOW`/`ASK`/`DENY` semantically, with configurable model-failure and approval-timeout actions. Interception separates Rules / Model Configuration tabs and prefixes model decisions with `[Model]` and a reason.

#### Fixed

- Hardened judge-output parsing to prevent valid `DENY` decisions from being treated as allowance.

### Traffic

#### Changed

- Traffic recording now stores whole exchanges in SQLite, spilling large bodies into hash-deduplicated blob storage. Text bodies use trigram full-text indexes for arbitrary substring and Chinese searches. Deletion becomes a single SQL transaction, dropping from hours to milliseconds without stopping recording. Added large-body streaming downloads and `traffic_search` full-text parameters. Legacy file-tree data remains readable, searchable, and deletable without migration.

### Tasks and Assets

#### Added

- Added an Asset Coverage switch to task creation, enabled by default. Disabling it hides coverage calculations/display, shows only assets in the overview graph, stops automatic `task_scope` accumulation, and removes related Planner / Main Agent tools. Company linking is unaffected.
- Added per-asset `related` to `insert_assets` (default `true`), active only with coverage enabled. `false` saves only to the shared asset store and excludes the asset from task coverage, for incidental neighboring sites or unrelated assets.
- Company scopes and task test assets recognize domains, URLs, IPs, CIDRs, ICP registrations, and keywords consistently. Domains/IPs create or reuse global assets; CIDR/ICP/keywords remain task-scope context.

### Build

- `build.sh --release` builds Linux amd64/arm64, macOS amd64/arm64, and Windows amd64 together, producing zip releases with `skills/`, configuration examples, and README.
- Go linker strips debug information before zip packaging. UPX is explicitly optional to avoid startup segmentation faults from self-extracting ELF binaries in some Linux environments.
- Release Workflow smoke-tests Linux amd64 startup and provides `SHA256SUMS` with release archives.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.2] - 2026-08-20

### Added

- New tasks can link multiple existing tasks, inheriting direct-source facts, findings, completed intents, asset scopes, and blackboard context live and read-only. They retain independent execution queues, working directories, and session histories.
- Tasks support ordered LLM profile chains, automatically switching on explicit provider quota exhaustion and persisting the current profile, exhaustion state, and structured audit activity.
- Running and paused tasks can edit/reorder LLM chains and switch profiles manually. Task details announce automatic/manual switches and full-chain exhaustion.
- Running Workers support pause, resume, and cancel. Pausing retains blackboard data; cancellation transactionally removes the intent and directly produced facts, findings, and execution records after Worker writes stop.
- Task deletion optionally removes linked assets, traffic, findings, and task files, with deletion barriers, concurrency protection, and auditable deletion statistics.
- Added optional task concurrency limits; new tasks at capacity queue in FIFO order and start automatically when slots become available.
- Added task-asset and company-asset pagination, category statistics, and paginated intent Mock contracts.
- Added `build.sh` for single-binary builds with static frontend export, embedded assets, cross-platform targets, and build-version injection.

### Changed

- Explicit task LLM chains coexist with the global LLM Pool. Explicit chains retain strict quota-failover semantics; absent a chain, Agent bindings and global rules apply.
- Main Agent input supports auto-growing multiline text, `Enter` to send, `Shift+Enter` for newlines, and prevention of accidental sends during Chinese IME composition.
- Planner, Worker, and Main Agent share task-level LLM runtime; chains use the smallest candidate context window as a safe compaction threshold.
- Task details show running, idle, and paused states from actual LLM calls. Source-task facts, findings, intents, asset references, and graph nodes consistently show their origin and remain read-only.
- Agent previews, task reports, and related details use a shared Markdown renderer.
- LLM profiles use cards and drawers, with independently scrolling model lists inside drawers.
- Task pause no longer blocks Main Agent chat: Orchestration sessions are independent, and new messages remain possible while paused. Pausing only terminates the current round.
- Increased the default Worker run wall-clock limit from 600 to 1200 seconds.

### Fixed

- Removed optimistic Main Agent console messages in favor of server-only rendering, fixing ordering and cross-session contamination after pauses or failed sends.
- Opening Main Agent sessions scrolls to the bottom and fully displays the last reply; lazy-loading its full content keeps the bottom in view instead of pushing it off-screen.
- Fixed Main Agent sessions continuing after task pause and orchestration Agents pausing tasks without also stopping Main Agent.
- Fixed task status badges and actions diverging from completion and actual Planner, Worker, or Main Agent execution.
- Fixed late blackboard writes and leftover files caused by Worker cancellation, task deletion, and concurrent writes.
- Fixed broken Markdown previews, long deletion-confirmation overflow, mobile widths, and misaligned task-detail buttons.
- Added named termination reasons to every Agent cancellation path; activity details show the canceller, terminal state, rounds, duration, token usage, and unanswered tool calls.
- Fixed a shutdown race where parent context could overwrite the named `shutdown` reason, and garbled Chinese activity summaries caused by byte truncation.
- Fixed cancellation events with partial streaming output losing the true termination reason, while preserving already generated content.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

[Unreleased]: https://github.com/hypnguyen1209/V-ARTEX/compare/v0.3.10...HEAD
[0.3.10]: https://github.com/hypnguyen1209/V-ARTEX/compare/v0.3.9...v0.3.10
[0.3.9]: https://github.com/hypnguyen1209/V-ARTEX/compare/v0.3.8...v0.3.9
[0.3.8]: https://github.com/hypnguyen1209/V-ARTEX/compare/v0.3.7...v0.3.8
[0.3.7]: https://github.com/hypnguyen1209/V-ARTEX/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/hypnguyen1209/V-ARTEX/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/hypnguyen1209/V-ARTEX/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/hypnguyen1209/V-ARTEX/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/hypnguyen1209/V-ARTEX/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/hypnguyen1209/V-ARTEX/compare/v0.3.1...v0.3.2
