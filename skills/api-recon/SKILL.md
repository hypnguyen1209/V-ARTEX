---
name: api-recon
description: Use when collecting a website's API endpoints.
---

# API Recon (Frontend API Reconnaissance)

With **authorization**, discover as completely as possible: **backend APIs** (paths, methods, parameters, response bodies), **frontend routes**, and **UI feature triggers** (tabs, dialogs, table actions, etc.).

---

## Boundaries and Prohibitions (Required Reading for Agents; Violations Exceed Scope)

This skill covers **API / parameter surface reconnaissance only**, not vulnerability discovery or exploitation.

### Task Boundaries

| Area | Allowed | Prohibited |
|---|---|---|
| **Objective** | Enumerate paths, methods, parameters, routes, and UI triggers | SQLi/XSS/authorization attacks/brute force/vulnerability fuzzing, tampered-request attacks, destructive operations |
| **Authentication** | Use hooks + stubs/mocks to bypass **client-side** login gates | Ask users for or guess credentials; attempt actual login-form submissions |
| **Runtime** | Hook APIs without credentials; use mock responses to render the SPA's post-login shell | Workflows that require a real backend session to proceed |

### Dynamic Analysis Without Credentials (Phase 3 Default)

1. Use `preload.js` / `runtime_harvest.js` to **intercept and stub** login, permission, menu, and other bootstrap APIs;
2. Return mock bodies for business query APIs with **correct structure, successful business codes, and optionally empty data**;
3. Allow the frontend to render post-login pages even without a backend or with 401 responses, triggering more XHR/fetch/WebSocket traffic;
4. **Empty data, blank tables, and placeholder UI are expected**—do not switch to real login or vulnerability testing because of them.

**Principle**: Use mocks to expose frontend routes and mount components; **record outbound requests only**. What the backend returns matters less than **which additional APIs the frontend calls**.

### Strict Workflow Prohibitions

| Prohibited | Required Alternative |
|---|---|
| Before Phase 1 is complete, grep/curl/Read the main entry `index-*.js` to extract API paths | Run `OUTDIR/harvest_static.py` |
| Write `extract_apis.py` or another replacement for harvest | Modify `OUTDIR/harvest_static.py` and rerun it |
| Repeat the same grep/command after it has failed >=2 times | Change strategy: read tool_logs, modify harvest, consult reference |
| Skip Gate A/B and run the originals in `scripts/` directly | Copy them to OUTDIR and adapt them to the target |
| Use real usernames/passwords, OTP, OAuth, or other authentication | Use stubs/mocks (see above) |
| Skip stubs to obtain "real data" and conduct authorization/injection testing | Record outbound requests only; stay within recon boundaries |
| Delete, export sensitive data, perform bulk writes, or other irreversible operations | The same restriction applies to coverage clicks |
| Claim all pages and APIs were found without runtime analysis + dynamic enumeration | Follow "Completion Criteria" or state limitations |
| Claim all parameters are known without the parameter trigger matrix + diff | Complete the Phase 3b matrix + Phase 5 diff |
| Infer required/optional fields from a single runtime sample | Use multi-sample diffs or validation rules/error inference |

---

## Two-Layer Model and Runtime Modes

| Layer | Outputs | Limits |
|---|---|---|
| **Static** (JS bundles) | Complete endpoint paths, draft routes, candidate fields at payload construction sites | No HTTP methods; parameters require Phase 1b; misses URLs assembled at runtime |
| **Runtime** (live session) | Methods + bodies + responses + dynamic URLs + WS/SSE; multi-sample diffs complete parameter coverage | Pages must actually render to issue requests; one sample cannot establish required/optional fields |

| Runtime Mode | Engine | Use For |
|---|---|---|
| **depth** | `runtime_harvest.js` (Puppeteer) | API inventory, METHOD/params/response bodies, WS/SSE, reproducible batch runs |
| **coverage** | browser + `preload.js` | Clicking tabs/dialogs/tables for deeper feature coverage |
| **both** | depth followed by coverage | Most complete; takes the longest |

**Parameter methodology** (no universal script): find paths with harvest/regex; find parameters through **anchor-window expansion + UI binding chains + multi-sample diffs + error inference** (grep recipes in [reference.md](reference.md), section J).

---

## Completion Criteria

All conditions must be met before claiming recon is complete:

- [ ] **Static**: Phase 1 harvest produces `api_static.txt`, `routes.txt`, and `js/`
- [ ] **Runtime**: At least depth or coverage; coverage/both require **working hooks + the dynamic enumeration loop**
- [ ] **App shell rendered**: Business paths stay outside `/login` (account for hash routing)
- [ ] **Parameters**: coverage/both complete the parameter trigger matrix + `param_samples.json`; Phase 5 merges `params_merged.json`
- [ ] **Depth** (if module pages are blank): Complete Phase 4 permission-tree reconstruction and rerun until **module-level APIs** appear (not just locale/bootstrap)
- [ ] **Delivery**: All Phase 5 outputs exist (see the Phase 5 output table); `insert_assets` writes service and endpoint assets

---

## Scripts and Gates

`scripts/` contains reference templates only. **Do not** run an original directly and treat it as the final result.

**Rule**: Read first → adapt to the target → write to `OUTDIR` (e.g. `recon/`) → record `CHANGES.md`. If a template does not fit, rewrite it using the methodology, borrowing only its structure.

| Gate | When | Reference Script → OUTDIR Copy | Common Required Adaptations |
|---|---|---|---|
| **A (static)** | After Phase 0, before the **first** harvest/spider run | `harvest_static.py` / `spider_mpa.py` | **For most sites the default regex works as is**; change endpoint regex, webpack/Vite `publicPath`, or MPA exclude/cookie only when the manifest/dialect does not match |
| **B (runtime)** | After Phase 2, before depth/coverage | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage keys, neutralize success values, stubs, login regex, API prefixes, hash/history |

**Mandatory SPA sequence** (cannot be reordered; phase numbers take precedence over "explore before scripting"):

| Step | Required | Prohibited |
|---|---|---|
| After Phase 0 | Next Bash command = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read the main entry `index-*.js` (usually >500KB) |
| Gate A | Copy script → make small changes if needed → **run immediately** | Extract APIs manually before deciding whether to harvest |
| Before Phase 1 completes | Verify outputs with `wc -l`; fix harvest and retry on 404 | Write extraction scripts; repeatedly grep URLs that have not been downloaded |
| From Phase 1b onward | Grep only `OUTDIR/js/*.js` | Use the main bundle instead of harvest |

- ✅ Copy `harvest_static.py` → (optionally) adjust regex → **run immediately**
- ❌ curl main bundle → grep repeatedly → write temporary extractor → harvest last
- **MPA**: After Phase 0, next Bash command = `python3 OUTDIR/spider_mpa.py ...`

---

## Tool and Output Constraints

| Constraint | Explanation |
|---|---|
| Large files | **Do not** Read/grep `index-*.js` >100KB into context; batch-process with OUTDIR scripts |
| Grep output | Must include `\| head -20` or `-m 5`; keep only path summaries in conversation, never bundle excerpts |
| Validation | Use `wc -l`, `ls \| wc -l`; do not Read an entire directory |
| Initial regex probe | Optional, at most once, only on a small chunk or HTML <=50KB; harvest is authoritative for formal static analysis |
| Reference | Recipes/templates/troubleshooting: [reference.md](reference.md); do not repeat its full contents inline |

---

## Execution Roadmap

```
Phase 0 classify + OUTDIR
  → Gate A → Phase 1 harvest (★ run immediately ★)
  → Phase 1b parameter reverse engineering
  → Phase 2 three authentication gates → config.json
  → Gate B → Phase 3 runtime + parameter matrix
  → Phase 4 permission tree (when needed) → rerun Phase 3
  → Phase 5 merged report + insert_assets: batch-insert every discovered service and API endpoint asset; never omit a discovered asset from insertion
```

Check off in order; **do not enter the next phase until the preceding item is complete**.

1. [ ] **Phase 0**: Identify SPA/MPA; create `OUTDIR` → [Phase 0](#phase-0--classification)
2. [ ] **Gate A + Phase 1**: Copy scripts → harvest **immediately** → verify with `wc -l` → [Phase 1](#phase-1--static)
3. [ ] **Phase 1b**: Anchor-window expansion + binding layer → `param_candidates.json` → [Phase 1b](#phase-1b--parameter-reverse-engineering)
4. [ ] **Phase 2**: Three authentication gates → `config.json` → [Phase 2](#phase-2--three-authentication-gates)
5. [ ] **Gate B**: Adapt runtime scripts → [Phase 3](#phase-3--runtime)
6. [ ] **Phase 3**: depth / coverage / both; confirm the app shell renders; parameter trigger matrix → `param_samples.json`
7. [ ] **Phase 4** (when needed): Permission tree → patch stubs → rerun Phase 3 → [Phase 4](#phase-4--permission-tree-reconstruction)
8. [ ] **Phase 5**: Merge outputs + report + `insert_assets` → [Phase 5](#phase-5--merge-and-report)

---

## Phase 0 — Classification

Fetch entry HTML and **create `OUTDIR`** (do not modify the skill's `scripts/`):

- **SPA**: Empty shell + `<div id=app>` + chunks → Phases 1–5
- **MPA**: SSR + `<form>`, no endpoint bundle → after Gate A:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

Produces `forms.txt`, `links.txt`, and `api_inline.txt`. For an SPA with approximately 0 forms → switch to Phase 1.

---

## Phase 1 — Static

Follow [Scripts and Gates](#scripts-and-gates) · [Tool and Output Constraints](#tool-and-output-constraints).

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest: Parse HTML scripts → webpack/Vite manifest → download all lazy chunks → produce `js/`, `api_static.txt`, `routes.txt`, and `chunkmap.txt`.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- Compare chunk count with manifest: on 404, modify harvest and retry; do not manually curl chunks one by one
- Too few entries in `api_static.txt` → relax the endpoint regex in OUTDIR and rerun (see reference)

### Phase 1b — Parameter Reverse Engineering

Paths come from Phase 1; parameter fields need separate recon. Follow the grep rules in [Tool and Output Constraints](#tool-and-output-constraints).

**Completion standard**: For important APIs, identify field names, transport locations, inferred types, requiredness, sample values, and confidence.

#### 1b.0 — Transport Shapes

| Shape | Parameter Location | Static Analysis Priority |
|---|---|---|
| REST JSON | body + query | `(params\|data\|body)\s*:\s*\{` near the path anchor |
| GraphQL | `variables` | gql templates, `$page: Int` |
| Traditional form | urlencoded | `<form>`, `FormData` |
| File upload | multipart | `FormData.append` |
| Path parameters | `/user/:id` | Route table + `useParams` / `$route.params` |
| Encryption/signatures | Wrapped in `sign`/`data` | Hook encryption function inputs (reference section D) |

Output: Mark each API with `transport: query|json|form|graphql|encrypted`.

#### 1b.1 — Anchor-Window Expansion

Use a known path as an anchor and expand the window to find payload objects:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| Wrapper | Parameter Clues |
|---|---|
| axios instance | `data` / `params` |
| Shared request wrapper | Interceptors inject global fields |
| OpenAPI client | Generated method signatures |
| React Query / SWR | Second hook argument |
| Vue composable | Composable inputs |

Residual type information: `yup`/`zod`/rules, `Form.Item name=`, embedded Swagger.

→ `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — Binding Layer

```
Form field → onFinish/handleSubmit → transform → API payload
```

| Binding Source | Technique |
|---|---|
| Form submit | Follow submit → transform → API |
| Table search | `getFieldsValue()` → `params` |
| Route | `:id` / `?tab=` |
| Interceptor | Global `tenantId`, pagination, sign |
| Enum select | `options` → API enum values |

Trace the DevTools call stack upward from `fetch`/`XHR.send` to the payload construction function.

#### 1b.3 — Three Payload Questions (Distinct from Phase 2's Three Authentication Gates)

| Question | Required Answer |
|---|---|
| **Assembly** | Where the payload is built; evidence of transforms |
| **Validation** | required, pattern, enum |
| **Transport** | path / query / body / multipart / headers |

While investigating the interceptor gate (Phase 2), also inspect globally injected fields (Authorization, `X-Tenant-Id`, sign).

#### 1b.4 — Handoff to Phase 3

Candidate fields come from static analysis/bindings; **required/optional/conditional dependencies** need the Phase 3 parameter matrix + diff + Phase 5 error inference.

---

## Phase 2 — Three Authentication Gates

Grep `OUTDIR/js/` (with `head`) and write `config.json` (recipes in reference):

| Gate | Question | Keywords |
|---|---|---|
| **Rendering gate** | How does the app determine login state? | `isLogin`, `getToken`, Cookie/localStorage |
| **Interceptor gate** | What triggers a redirect to `/login`? | `response_code`, `errno`, axios interceptor |
| **Content gate** | Where do menus/permissions come from? | `menu`, `permission`, `role`, `acl`, `routes` |

Do not treat localStorage key names as credentials—confirm them from chunks/request chains.

**Exit = Gate B**: Record conclusions in `config.json` and adapt `OUTDIR/runtime_harvest.js` / `preload.js`.

### Phase 2b — API Observation (Optional)

Use the OUTDIR copy of `preload.js` to confirm session key names, Authorization, and nested API URLs:

| Configuration | Output |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | Header observations |
| `extractUrlsFromResponse: true` | Child APIs within responses |
| `observe.storageReads/cookieReads: true` | Populate config from observations |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

Export each coverage round: `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__`.

---

## Phase 3 — Runtime

Gate B must be complete; follow [Boundaries and Prohibitions](#boundaries-and-prohibitions-required-reading-for-agents-violations-exceed-scope) · the credential-free mock strategy.

Set `"runtimeMode": "depth" | "coverage" | "both"` in `config.json` (template in reference).

### Hooks and Stubs (Shared by depth + coverage)

| Layer | Scope | Purpose |
|---|---|---|
| L1 precise | auth/permission/bootstrap stubs | Pass initial authentication checks |
| L2 negative-response correction | All JSON responses | Not-logged-in codes → success |
| L3 fallback | `/api` etc. not matched by L1 | Empty success bodies to render UI |

- **depth**: Fake auth + `forward` business-code rewriting + `stubs`; traverse `routes` (hash/history); produce `runtime_api.json`
- **coverage**: Inject `preload.js` at **document start** (CDP `addScriptToEvaluateOnNewDocument` or Userscript)

Verify: `window.__API_RECON_PRELOAD__` exists; business paths do not return to `/login`.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — coverage Dynamic Enumeration (Mandatory)

1. Main navigation/sidebar — click every item; wait 1–3s for network traffic
2. Tabs — `role=tab`, `.ant-tabs-tab`
3. Tables — first-row view/edit/details
4. Toolbar — export, filter, create (**avoid irreversible deletion**)
5. On entering each module — merge APIs/routes
6. SPA — controlled `pushState` for uncovered paths in `routes.txt` (prohibited for MPAs)

**Parameter trigger matrix** (mandatory): Record each operation type once per module; **diff multiple samples**:

| Operation | Typical Additional Parameters |
|---|---|
| Initial list load | Pagination + default filters |
| Search | keyword, filter |
| Advanced filters | More optional fields |
| Create/edit | Complete entity |
| Bulk/export/sort | `ids[]`, `exportType`, `sortField` |

**Outbound bodies/headers remain real under stubs**—use the requests as evidence. Record → `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**: `neutralizeVueRouter: true` + document-start preload
- **React**: `routes.txt` + sidebar clicks + `pushState`
- **both**: 3a depth first, then 3b coverage

---

## Phase 4 — Permission-Tree Reconstruction

**Trigger**: Blank module pages / only bootstrap calls (e.g. locale) per route → content gate unresolved.

| Symptom | Meaning |
|---|---|
| Shell renders | Rendering + interceptor gates passed |
| Missing sidebar items/blank pages after clicks | Incomplete stub shape or permission codes |
| Same tiny API set on every route | `v-if permission` checks have not passed |
| `routes.txt` has far fewer routes than the bundle | Supplement from the auth module |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

Typical chain: `role_permissions` (flat codes) + `permissions/all` (tree) → `getResultTree` → `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Intermediate outputs: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

Stub checks: Outer `response_code` matches the interceptor gate; flat codes align with the tree; `routes` covers every link in `route_map`.

After updating `config.json`, **rerun Phase 3**. For large SPAs, adjust `waitUntil`, `routeTimeout`, and `perRouteMs` (reference sections A3/I).

---

## Phase 5 — Merge and Report

### Output Table

| File | Phase | Contents |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | Static bundles and paths |
| `param_candidates.json` | 1b | Static parameter-field candidates |
| `config.json` | 2 | Three gates + runtime configuration |
| `runtime_api.json` | 3a | Detailed depth recording (including WS/SSE) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | Multiple samples, click logs, detail |
| `route_map.json` etc. | 4 | Permission-tree intermediate files (if run) |
| `params_merged.json` | 5 | Merged parameter fields + confidence |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | Routes, APIs, params, features, limitations |
| **insert_assets** | 5 | Write every service and endpoint asset to the asset store |

### 5b — Parameter Merge

Diff `param_samples.json`; **there is no universal merge script**. Confidence rules: reference J7 (high/medium/low/pending trigger).

### 5c — Error Inference

Within authorization, you may send incomplete requests and read 400 responses (**parameter recon, not vulnerability testing**): `field 'x' is required`, enum errors, etc. Account for `data` wrappers, `variables`, and pre-encryption `bizData`.

The report must state: runtimeMode, static/runtime API counts, parameter confidence, uncovered modules, and a `CHANGES.md` summary of differences from the reference scripts.

Suggested `site_map.json` structure:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

More fields and grep recipes: [reference.md](reference.md).

---

## General Notes

- **Framework-agnostic**: Same lazy-loading methodology for webpack/Vite/Angular
- **Transports**: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web is out of scope
- **SSR**: Client fetch can be recorded; RSC/Server Actions cannot be fully enumerated
- **Blind spots**: JSVMP, WASM, strong HMAC/mTLS validation → static analysis + stated limitations
- **Parameter blind spots**: Conditional dependencies, hidden params, WASM payload construction → "pending trigger"/"unreachable"
- **Static analysis is the safety net**: Endpoints can still be enumerated when runtime is blocked

---

## Additional Resources

- Grep recipes, `config.json` template, troubleshooting, hooks, parameter reverse engineering in section J, site_map template: **[reference.md](reference.md)**
- Reference script paths: [Scripts and Gates](#scripts-and-gates) table
