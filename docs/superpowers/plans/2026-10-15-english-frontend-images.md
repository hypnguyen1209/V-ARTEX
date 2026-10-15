# Frontend and Images English Translation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert the entire Next.js interface and mock experience to English, then recreate 13 application screenshots and the separate WeChat promotional banner.

**Architecture:** Replace human-readable Chinese in place without adding i18n. Treat the scoped Han scan as the text regression test, preserve TypeScript/API contracts, run Biome and production builds, and capture deterministic mock-mode images at fixed dimensions.

**Tech Stack:** Next.js 16, React 19, TypeScript, Biome, npm, mock API layer, Playwright CLI, image generation for the promotional banner.

---

## File Map

- Modify: all 92 Han-matching tracked files under `web/` identified by Task 1.
- Replace: `screenshots/dashboard.png`, `screenshots/tasks.png`, `screenshots/sessions.png`, `screenshots/graph.png`, `screenshots/findings.png`, `screenshots/assets.png`, `screenshots/assets_test.png`, `screenshots/traffic.png`, `screenshots/chat.png`, `screenshots/agents.png`, `screenshots/llm.png`, `screenshots/intercept.png`, and `screenshots/logs.png`.
- Replace: `screenshots/wx.png`, preserving the original QR region pixel-for-pixel.
- Do not add an i18n runtime or a screenshot dependency to `web/package.json`.

### Task 1: Freeze the Frontend Inventory and Baseline

**Files:**
- Read: all tracked files under `web/`.
- Test: `web/package.json` scripts.

- [ ] **Step 1: Record the exact frontend inventory**

Run:

```bash
git grep -I -P -l '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- web \
  > /tmp/artex-web-han-files.txt
wc -l /tmp/artex-web-han-files.txt
```

Expected: `92` files before implementation.

- [ ] **Step 2: Verify RED for the scoped text check**

Run:

```bash
git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- web \
  > /tmp/artex-web-han-before.txt
test -s /tmp/artex-web-han-before.txt
```

Expected: exit 0 because the baseline contains 4,574 matching lines.

- [ ] **Step 3: Install pinned dependencies and run the baseline**

Run:

```bash
cd web
npm ci
npm run check
NEXT_PUBLIC_MOCK=1 npm run build
```

Expected: dependency installation completes, Biome exits 0, and the production build exits 0. Record any existing failure before editing.

### Task 2: Translate Shared Frontend Contracts and Navigation

**Files:**
- Modify: `web/next.config.mjs`
- Modify: `web/src/config/app-config.ts`
- Modify: `web/src/navigation/sidebar/sidebar-items.ts`
- Modify: `web/src/lib/api.ts`
- Modify: `web/src/lib/auth.ts`
- Modify: `web/src/lib/chat-mentions.ts`
- Modify: `web/src/lib/chat-mentions.test.mjs`
- Modify: `web/src/lib/chat-send-mode.ts`
- Modify: `web/src/lib/company-scope.ts`
- Modify: `web/src/lib/side-questions.ts`
- Modify: `web/src/lib/status.ts`
- Modify: `web/src/lib/task-assets.ts`
- Modify: `web/src/lib/types.ts`
- Modify: `web/src/lib/utils.ts`
- Modify: `web/src/lib/preferences/preferences-storage.ts`
- Modify: `web/src/hooks/use-side-questions.ts`
- Modify: `web/src/proxy.ts`

- [ ] **Step 1: Translate shared visible text and comments**

Use `Dashboard`, `Chat`, `Tasks`, `Findings`, `Traffic`, `Tool Runs`, `LLM Records`, `Assets`, `Asset Sync`, `Workspace`, and `System` in navigation. Keep route URLs, object keys, status enum values, storage keys, cookie names, API paths, and serialized values unchanged.

- [ ] **Step 2: Run focused checks**

Run:

```bash
cd web
node --test src/lib/chat-mentions.test.mjs
npx biome check next.config.mjs src/config src/navigation src/lib src/hooks src/proxy.ts
```

Expected: tests and Biome PASS.

- [ ] **Step 3: Commit**

```bash
git add web/next.config.mjs web/src/config web/src/navigation web/src/lib web/src/hooks web/src/proxy.ts
git commit -m "refactor(web): translate shared UI contracts"
```

### Task 3: Translate Authentication, Shell, Dashboard, and Chat

**Files:**
- Modify: `web/src/app/(auth)/login/page.tsx`
- Modify: `web/src/app/(auth)/setup/page.tsx`
- Modify: `web/src/app/(main)/layout.tsx`
- Modify: every Han-matching file under `web/src/app/(main)/_components/`
- Modify: `web/src/app/(main)/dashboard/page.tsx`
- Modify: `web/src/app/(main)/chat/page.tsx`
- Modify: `web/src/components/mention-textarea.tsx`
- Modify: `web/src/components/side-question-workspace.tsx`
- Modify: `web/src/components/transcript.tsx`
- Modify: `web/src/components/todo-popover.tsx`

- [ ] **Step 1: Translate the application shell and primary flows**

Preserve form names, accessibility relationships, redirects, cookie behavior, keyboard handling, polling, and API calls. Translate legal/disclaimer text faithfully rather than shortening its security meaning.

- [ ] **Step 2: Check the batch**

Run:

```bash
cd web
npx biome check 'src/app/(auth)' 'src/app/(main)/_components' \
  'src/app/(main)/dashboard' 'src/app/(main)/chat' \
  src/components/mention-textarea.tsx src/components/side-question-workspace.tsx \
  src/components/transcript.tsx src/components/todo-popover.tsx
```

Expected: Biome PASS.

- [ ] **Step 3: Commit**

```bash
git add 'web/src/app/(auth)' 'web/src/app/(main)/_components' \
  'web/src/app/(main)/dashboard' 'web/src/app/(main)/chat' \
  web/src/components/mention-textarea.tsx web/src/components/side-question-workspace.tsx \
  web/src/components/transcript.tsx web/src/components/todo-popover.tsx
git commit -m "refactor(web): translate application shell"
```

### Task 4: Translate Task Workflows and Shared Task Components

**Files:**
- Modify: `web/src/app/(main)/function/tasks/page.tsx`
- Modify: `web/src/app/(main)/function/tasks/detail/page.tsx`
- Modify: every Han-matching file under `web/src/app/(main)/function/tasks/detail/_tabs/`
- Modify: `web/src/components/approval-execution-focus.tsx`
- Modify: `web/src/components/approval-records.tsx`
- Modify: `web/src/components/finding-retest-dialog.tsx`
- Modify: `web/src/components/finding-retest-panel.tsx`
- Modify: `web/src/components/scope-text-editor.tsx`
- Modify: `web/src/components/task-llm-profile-chain.tsx`
- Modify: `web/src/components/task-template-controls.tsx`

- [ ] **Step 1: Translate task creation, state, tabs, and detail content**

Use consistent tab labels: `Sessions`, `Overview`, `Exploration Graph`, `Broadcast`, `Findings`, `Retests`, `Test Assets`, `Asset Coverage`, `Interception Approvals`, and `Report`. Keep task/status values, query parameters, tool names, Markdown content handling, and polling behavior unchanged.

- [ ] **Step 2: Run Biome on the batch**

Run:

```bash
cd web
npx biome check 'src/app/(main)/function/tasks' \
  src/components/approval-execution-focus.tsx src/components/approval-records.tsx \
  src/components/finding-retest-dialog.tsx src/components/finding-retest-panel.tsx \
  src/components/scope-text-editor.tsx src/components/task-llm-profile-chain.tsx \
  src/components/task-template-controls.tsx
```

Expected: Biome PASS.

- [ ] **Step 3: Commit**

```bash
git add 'web/src/app/(main)/function/tasks' web/src/components
git commit -m "refactor(web): translate task workflows"
```

### Task 5: Translate Findings, Assets, Traffic, Commands, and Workspace

**Files:**
- Modify: every Han-matching file under `web/src/app/(main)/function/findings/`
- Modify: `web/src/app/(main)/function/assets/page.tsx`
- Modify: `web/src/app/(main)/function/traffic/page.tsx`
- Modify: `web/src/app/(main)/function/commands/page.tsx`
- Modify: `web/src/app/(main)/function/llm-records/page.tsx`
- Modify: `web/src/app/(main)/function/sync/page.tsx`
- Modify: `web/src/app/(main)/function/workspace/page.tsx`
- Modify: `web/src/components/asset-dsl-search.tsx`
- Modify: `web/src/components/finding-traffic-panel.tsx`
- Modify: `web/src/components/http-code-block.tsx`
- Modify: `web/src/components/link-traffic-dialog.tsx`
- Modify: `web/src/components/traffic-evidence-viewer.tsx`
- Modify: `web/src/components/traffic-picker-dialog.tsx`
- Modify: `web/src/components/copy-button.tsx`

- [ ] **Step 1: Translate the operational feature pages**

Keep DSL tokens, HTTP methods, severity/status enums, traffic IDs, evidence formats, and ScopeSentry API terms unchanged. Translate labels, help text, empty states, errors, toasts, accessibility labels, comments, and examples.

- [ ] **Step 2: Run Biome on the batch**

Run:

```bash
cd web
npx biome check 'src/app/(main)/function' \
  src/components/asset-dsl-search.tsx src/components/finding-traffic-panel.tsx \
  src/components/http-code-block.tsx src/components/link-traffic-dialog.tsx \
  src/components/traffic-evidence-viewer.tsx src/components/traffic-picker-dialog.tsx \
  src/components/copy-button.tsx
```

Expected: Biome PASS.

- [ ] **Step 3: Commit**

```bash
git add 'web/src/app/(main)/function' web/src/components
git commit -m "refactor(web): translate operational pages"
```

### Task 6: Translate System Administration Pages

**Files:**
- Modify: every Han-matching file under `web/src/app/(main)/system/`
- Modify: `web/src/components/agent-editor.tsx`
- Modify: `web/src/components/asset-intercept-rules-editor.tsx`
- Modify: `web/src/components/table-pagination.tsx`
- Modify: `web/src/components/ui/dialog.tsx`
- Modify: `web/src/components/ui/sortable-head.tsx`

- [ ] **Step 1: Translate all administration UI**

Use official names for DingTalk, Feishu, WeCom, Telegram, email, webhook, MCP, LLM, and ScopeSentry. Keep provider formats, config keys, header names, retry values, API payloads, and all IDs unchanged.

- [ ] **Step 2: Run Biome on the batch**

Run:

```bash
cd web
npx biome check 'src/app/(main)/system' src/components/agent-editor.tsx \
  src/components/asset-intercept-rules-editor.tsx src/components/table-pagination.tsx \
  src/components/ui/dialog.tsx src/components/ui/sortable-head.tsx
```

Expected: Biome PASS.

- [ ] **Step 3: Commit**

```bash
git add 'web/src/app/(main)/system' web/src/components
git commit -m "refactor(web): translate system administration"
```

### Task 7: Translate the Complete Mock Experience

**Files:**
- Modify: `web/src/lib/mock/data.ts`
- Modify: `web/src/lib/mock/enabled.ts`
- Modify: `web/src/lib/mock/handler.ts`

- [ ] **Step 1: Translate mock records and responses**

Translate task names/descriptions, agents, intents, findings, evidence, report bodies, activity, errors, logs, notification examples, and comments. Preserve IDs, timestamps, IPs, hostnames, URLs, enum values, API response shapes, and cross-record references.

- [ ] **Step 2: Verify frontend zero-Han and production build**

Run:

```bash
cd web
npx biome check src/lib/mock
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- .
npm run check
NEXT_PUBLIC_MOCK=1 npm run build
```

Expected: no Han matches under `web`, Biome PASS, and production build PASS.

- [ ] **Step 3: Commit**

```bash
git add web
git commit -m "refactor(web): translate mock data to English"
```

### Task 8: Recreate the 13 Application Screenshots

**Files:**
- Replace: the 13 application PNG files listed in the File Map.

- [ ] **Step 1: Start the deterministic mock app**

Run from `web/`:

```bash
NEXT_PUBLIC_MOCK=1 npm run dev -- --hostname 127.0.0.1
```

Expected: Next.js reports a local URL and serves the mock app without a backend.

- [ ] **Step 2: Capture the fixed routes at 1440 by 900 CSS pixels**

Use Playwright CLI with `--hires`, which yields the existing 2880 by 1800 pixel format:

```bash
npx playwright-cli open http://127.0.0.1:3000/dashboard
npx playwright-cli resize 1440 900
npx playwright-cli screenshot --filename=../screenshots/dashboard.png --hires
npx playwright-cli goto http://127.0.0.1:3000/function/tasks
npx playwright-cli screenshot --filename=../screenshots/tasks.png --hires
npx playwright-cli goto 'http://127.0.0.1:3000/function/tasks/detail?id=t-acme-web'
npx playwright-cli screenshot --filename=../screenshots/sessions.png --hires
npx playwright-cli run-code "async page => { await page.getByRole('tab', {name: 'Exploration Graph'}).click(); await page.waitForTimeout(500); }"
npx playwright-cli screenshot --filename=../screenshots/graph.png --hires
npx playwright-cli run-code "async page => { await page.getByRole('tab', {name: 'Asset Coverage'}).click(); await page.waitForTimeout(500); }"
npx playwright-cli screenshot --filename=../screenshots/assets_test.png --hires
```

Repeat `goto`, wait for network/UI stability, and capture:

```text
/function/findings  -> findings.png
/function/assets    -> assets.png
/function/traffic   -> traffic.png
/chat               -> chat.png
/system/agents      -> agents.png
/system/llm         -> llm.png
/system/intercept   -> intercept.png
/system/logs        -> logs.png
```

- [ ] **Step 3: Inspect every screenshot**

Open all 13 files at original resolution. Confirm English-only visible text, no loading spinners, no clipped long labels, no modal/backdrop accident, no browser chrome, and no environment-specific credentials.

- [ ] **Step 4: Commit the application screenshots**

```bash
git add screenshots/dashboard.png screenshots/tasks.png screenshots/sessions.png \
  screenshots/graph.png screenshots/findings.png screenshots/assets.png \
  screenshots/assets_test.png screenshots/traffic.png screenshots/chat.png \
  screenshots/agents.png screenshots/llm.png screenshots/intercept.png screenshots/logs.png
git commit -m "docs: refresh English interface screenshots"
```

### Task 9: Translate the WeChat Promotional Banner

**Files:**
- Replace: `screenshots/wx.png`

- [ ] **Step 1: Preserve the source QR data**

Copy the original image outside the repository and record a lossless hash of a padded QR crop before editing. The preserved region must include the entire left QR code and its quiet zone.

- [ ] **Step 2: Use the image-generation workflow for the banner edit**

Invoke the `imagegen` skill and edit `screenshots/wx.png` with this intent: retain the white rounded banner, WeChat branding, green search field, and `SecSentry`; replace the Chinese call-to-action with `Search on WeChat`; do not invent additional text.

After generation, composite the original QR region back pixel-for-pixel so generative edits cannot corrupt it. Keep the original 1954 by 624 canvas size and RGBA output.

- [ ] **Step 3: Verify the banner**

Check the final dimensions, compare the preserved QR crop hash with the original, scan the QR using an available decoder, and visually confirm there is no Chinese call-to-action outside the encoded QR payload.

- [ ] **Step 4: Commit**

```bash
git add screenshots/wx.png
git commit -m "docs: translate WeChat promotional banner"
```

### Task 10: Complete Frontend Verification

**Files:**
- Verify: all files under `web/` and all 14 images under `screenshots/`.

- [ ] **Step 1: Run final frontend checks**

Run:

```bash
cd web
npm run check
NEXT_PUBLIC_MOCK=1 npm run build
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- .
```

Expected: Biome and build PASS, with no Han output.

- [ ] **Step 2: Verify image inventory and dimensions**

Run: `file ../screenshots/*.png`

Expected: 14 valid PNG files; the 13 app captures use the agreed fixed viewport output and `wx.png` remains 1954 by 624 RGBA.
