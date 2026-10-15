# api-recon — Reference Manual

Grep recipes, `config.json` templates, and troubleshooting. Run all grep commands against the `js/` directory. For single-line bundles, optionally run `js-beautify` or `sed 's/}/}\n/g'` first; raw grep with context windows is usually sufficient.

## Script Notes

Every file in `scripts/` is a **reference template** and must be adapted to the target site before execution. Typical changes:

| Script | Common Adaptations |
|---|---|
| `harvest_static.py` | Endpoint regex, webpack/Vite manifest parsing, micro-frontend publicPath, retries/concurrency |
| `runtime_harvest.js` | neutralize field names and success values, stub matching and body shapes, route sources, WS capture, `waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, whether to enable L3, `recordDetail`, `observe.*`, `neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` destructive links, cookie, depth/max, same-origin filtering |
| `extract_route_map.py` | `routeMap` / `routeLink` regex, KEY naming patterns |
| `build_perm_tree.py` | `userRouteAuth` parsing, `ROOTS`/`PREFIX_PARENT` hierarchy heuristics, outer stub field names |
| `config.json` | Central entry point for all site-specific parameters above |

Prefer placing adapted files in the task working directory (e.g. `recon/`); state the specific differences from the reference scripts in the report.

---

## A. Reverse Engineering the Three Gates

### A1. Rendering Gate — "How Is Login State Determined?"

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

Trace `isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))` to identify the **storage key**, **container** (Cookie vs localStorage), and **encoding**:

| Encoding | Forged Config Value |
|---|---|
| Plain string / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | Unsigned/`alg:none` JWT, or sign with a key found in the bundle |
| Encryption (SM2/AES/RSA) | Find hardcoded keys; forge if the rendering gate only needs a decodable blob; otherwise fall back to static analysis |

→ Write to `cookies` / `localStorage`.

### A2. Interceptor Gate — "What Triggers a Redirect to /login?"

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE $'.{0,40}(\u672a\u767b\u5f55|\u8bf7\u91cd\u65b0\u767b\u5f55|\u767b\u5f55\u5df2\u8fc7\u671f|unauthorized|\u767b\u5f55\u5931\u6548|\u6388\u6743|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

Identify the **field names**, **success values** (usually `0` or `200`), and **failure values that trigger redirects**. Verify with a junk session:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ Write `neutralize.fields` + `neutralize.success`.

### A3. Content Gate — "Where Do Menus/Permissions Come From?"

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**Two data layers** (common in enterprise admin apps):

| API | Typical Payload | Consumer |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | Route guards, button-level ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | Sidebar menu rendering |
| `userRouteAuth` in bundle | `{ CODE: { url, name? } }` | code → frontend path |
| `routeMap` in bundle | `{ KEY: { name, link } }` | Alias resolution (webpack `o.DASHBOARD`) |

Read consumer code to confirm how `getResultTree(tree, permissions)` filters and which fields `v-if` / `hasAuth(code)` checks.

**Manual forgery** (small sites): Build a permissive payload → `stubs`.

**Full permission-tree reconstruction** (large sites, sidebar/submodules remain blank): See **section I**.

---

## B. config.json Template

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

Field notes:
- `runtimeMode`: `depth` (Puppeteer), `coverage` (browser MCP), `both`
- `cookies[].value` prefixes: `b64json:` → base64(JSON); `json:` → raw JSON; no prefix → literal
- `forward: true` forwards real requests and rewrites code fields; `false` uses entirely offline stubs
- `mockTier`: Enabled preload layers in coverage mode, e.g. `L1+L2`, `L1+L2+L3`
- `routes` comes from `routes.txt`; after menu forgery, the harness automatically appends `<a href>` links
- `captureResponses` / `recordWs` apply only in depth mode
- `waitUntil`: Use `domcontentloaded` for large SPAs to avoid hangs with `networkidle2`
- `routeTimeout`: Per-route `page.goto` timeout (milliseconds)
- `proxy`: Puppeteer `--proxy-server`; alternatively set `HTTP_PROXY` / `HTTPS_PROXY`

### B1. Dual-Stub Template (role_permissions + permissions/all)

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

Outer field names (`response_code` / `code` / `data`) must match the A2 interceptor gate; `permissions` must cover every leaf code in the tree.

---

## C. coverage Mode: preload Configuration

Edit the `CONFIG` object at the top of `scripts/preload.js`, or replace it before CDP injection:

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* Same as config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

Verify: `window.__API_RECON_PRELOAD__ === true` and the pathname remains stable.

Export recordings:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime Hook Capabilities

Browser hooks built into preload (coverage) and runtime_harvest (depth), with their coverage:

| Hook Capability | API Discovery Value | Coverage |
|---|---|---|
| Hook fetch / XHR.open | Record request URLs/methods | ✅ `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | Discover Authorization and other headers | ✅ `observe.xhrHeaders` |
| Hook localStorage/cookie reads | Confirm session key names | ⚠️ Optional `observe.storageReads/cookieReads` |
| Extract Vue routes | Complete frontendRoutes | ✅ `__API_RECON_ROUTES__` (loaded routes) |
| Neutralize Vue route guards / block login redirects | Render modules to trigger APIs | ✅ `neutralizeVueRouter` + native navigation neutralization |
| Extract React routes | Supplement routes | ⚠️ Static analysis + clicks; no dedicated hook |
| Block page redirects (login paths) | Keep the page open for analysis | ⚠️ Only blocks login paths to preserve business navigation |
| Hook crypto libraries (CryptoJS/SM, etc.) | Encrypted parameters → plaintext API body | ❌ Manually hook encryption function inputs; record conclusions in config |
| Anti-debugging bypass | Otherwise runtime cannot record APIs | ❌ Handle manually; static analysis remains available |

---

## E. Endpoint Extraction Regex (When Static Results Are Sparse)

Relax `extract_endpoints` in `harvest_static.py`, or extract manually:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. Troubleshooting

| Symptom | Cause → Action |
|---|---|
| Few static APIs | Endpoint dialect mismatch → relax regex (section D) |
| Chunk count ≪ manifest | CSS-only or undeployed chunks; 404s have already been retried |
| Runtime still shows login | Wrong rendering-gate assumptions → recheck A1: keys, container, encoding, domain |
| Shell renders but modules are blank | Content gate → forge menus (A3); `routes` paths may be wrong |
| Only bootstrap/locale per route | Incomplete permission codes → section I permission-tree reconstruction; check dual `role_permissions` + `permissions/all` stubs |
| Sidebar entries exist but child pages are blank | Tree lacks intermediate nodes, or codes disagree with `userRouteAuth` |
| Every API redirects to login | Interceptor gate → confirm `neutralize`; extend walk logic for nested fields |
| Zero WS frames | Subscription needs user interaction; increase `perRouteMs` |
| Empty response bodies | Real responses require `forward: true` |
| Missing Chromium | Install chromium or set `config.chromium` / `CHROMIUM` |
| Many mocks but still redirected to login | Hooks run too late or lack the `location.href` setter → document-start + preload |
| All lists empty | L3 empty arrays are normal; continue clicking tabs/settings/details |
| Redux actions mistaken for routes | Filter internal paths containing get/set/change/clear/toggle/upload |
| Vue still redirects to login | Preload is not at document start → change injection timing; or manually clear guards if `neutralizeVueRouter: false` |
| URLs in responses absent from logs | Enable `extractUrlsFromResponse`; or extract manually from `__API_RECON_DETAIL__` |
| Unknown Authorization header name | Enable `observe.xhrHeaders` or inspect request headers in DevTools |
| Runtime very slow / timeouts | Set `waitUntil: domcontentloaded`; lower `routeTimeout`; avoid `networkidle2` |
| Proxy connection failure | Check `proxy` / environment variables; Puppeteer and curl must use the same proxy port |

---

## G. Hardened Targets

When the server validates the session step by step (unforgeable signed cookies, server-rendered menus that cannot be stubbed), runtime gets stuck at the shell. Expected behavior:

- **Static analysis is sufficient for endpoint enumeration** — module paths are in the code
- If authorized, run the same harness with a **real session**: `forward: true`, no neutralize needed, capturing real methods/params/responses

---

## H. Per-Task Checklist

1. Confirm authorization scope
2. **Read** `scripts/harvest_static.py` → adapt to the target → run → review `api_static.txt`, `routes.txt`
3. **Phase 1b**: Path anchor-window expansion + binding layer → `param_candidates.json` (section J)
4. Reverse engineer A1/A2/A3 → write site-specific `config.json`
5. **Read and adapt** `runtime_harvest.js` / `preload.js` before execution
6. `runtimeMode=depth`: `npm install` → run the adapted harvest script
7. `runtimeMode=coverage/both`: Inject adapted preload at document start → browser MCP dynamic enumeration + **parameter trigger matrix**
8. Modules fail to render → **section I permission-tree reconstruction** → patch stubs → rerun
9. Multi-sample parameter diffs + error inference → `params_merged.json`
10. Merge → `site_map.json` + `api_merged.txt`; state coverage, gaps, and script changes honestly

---

## I. Permission-Tree Reconstruction (Phase 4 Deepening)

Use when forging simple `menus: [{ path, show: true }]` does not work and submodules still fail to mount.

### I1. Locate the Auth Module

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

Record: **Permission API paths**, **response field names**, **consumer chunk filenames**.

### I2. Extract routeMap

```bash
python3 scripts/extract_route_map.py recon/js recon/
# Produces recon/route_map.json
```

If `[!] no routeMap pattern found`: Relax the regex in `extract_route_map.py`, or grep manually:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. Build the Permission Tree + Stubs

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Script logic:
1. Parse `userRouteAuth={MONITOR:{url:...},...}` (including webpack aliases such as `He=o.DASHBOARD`)
2. Resolve aliases to actual paths using `route_map.json`
3. Infer parents from code prefixes (`MONITOR_ALERT` → `MONITOR`)
4. Write `permissions_tree.json`, `permissions_all_stub.json`, and `role_permissions_stub.json`
5. With `--config`, automatically update `stubs` and extend `routes` in `config.json`

**Target-specific adaptations** (at the top of the script):
- `DEFAULT_ROOTS`: Top-level module code list
- `DEFAULT_PREFIX_PARENT`: `PREFIX_` → parent mapping
- `DEFAULT_EXTRA_PARENT`: Orphan nodes without prefix-based parent relationships

### I4. Validate Stub Consistency

```bash
# Permission count should approximately match the number of userRouteAuth entries
wc -l recon/perm_codes_all.txt
# routes should cover every link in route_map
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. Rerun Runtime and Compare

```bash
node recon/runtime_harvest.js recon/config.json
# Compare runtime_api.json entry counts before/after forgery; check for module APIs under /attack, /asset, etc.
```

| Before Forgery | After Forgery (Success) |
|---|---|
| Same 3–5 bootstrap calls per route | Different routes trigger different module APIs |
| Only `/api/locale/language` | Module endpoints under `/api/web/...` appear |
| Single-digit routes in `routes.txt` | 80–110+ `routes` from route_map |

### I6. If It Still Fails

- **coverage mode**: Click sidebar entries + tabs; permission gating may issue requests only after interaction
- **Stub fields**: Compare the nesting of a real API response (curl + real session) with the stub
- **Additional guards**: Grep button-level checks such as `hasPermission|checkRole|func.` and extend `role_permissions.permissions`
- **Static fallback**: Module API paths remain in `api_static.txt`; runtime only adds METHOD/body; retain `param_candidates.json` + recorded samples for parameters

---

## J. Parameter Reverse Engineering (Phases 1b / 5b / 5c)

**Methodology, not a universal script.** Find paths with regex; find parameters with anchor-window expansion + UI binding chains + multi-sample diffs + error inference.

### J1. Anchor-Window Expansion — From Path to Payload Object

```bash
# Anchor on a path already known from Phase 1
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. Wrappers and Transport Shapes

```bash
# axios / shared request wrapper
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# Path parameters
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. Validation Gate — Required / Format / Enum

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. Binding Layer — Form → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

Runtime support: DevTools → Network → request → **Initiator** (call stack); trace upward from `fetch`/`send` to the payload construction function.

### J5. Encrypted Parameters

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**Do not guess fields from ciphertext** — hook encryption function **inputs** to record the plaintext payload before encryption; record conclusions in `config.json` / `param_candidates.json`.

### J6. Parameter Trigger Matrix (Mandatory in Phase 3)

Record each operation once per module and diff request bodies/queries:

| Operation | Focus |
|---|---|
| Initial list load | Pagination defaults |
| Search | keyword, filters |
| Advanced filters | Optional fields |
| Create/edit | Complete entity |
| Bulk/export | `ids[]`, `exportType` |
| Sort/paginate | `sortField`, `order` |

Produce `param_samples.json`: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. Confidence Rules

| Confidence | Condition |
|---|---|
| **High** | Static callsite + >=2 consistent runtime samples |
| **Medium** | Static only, or only one runtime sample |
| **Low** | Response/error inference without a second verification |
| **Pending trigger** | Known static fields whose UI/permission conditions were not reached |

### J8. Scenario Quick Setup

| Scenario | Sequence |
|---|---|
| REST list page | J1 payload object → J6 four diffs → J3 rules |
| Create/edit form | J3 Form name → J4 submit chain → runtime submission + intentionally leave fields blank to inspect 400 |
| GraphQL | J2 variables declarations → record variables for each runtime operation |
| Encrypted body | J5 hook inputs → pre-encryption fields are the actual params |

### J9. Mapping to api-recon Phases

| api-recon | Parameter Recon |
|---|---|
| Phase 1 static | J1 anchor-window expansion |
| Phase 2 A2 interceptor | Globally injected fields (tenantId, sign) |
| Phase 3 runtime | J6 trigger matrix + `param_samples.json` |
| Phase 4 permission tree | Forms differ by module → sufficient permissions are needed to trigger every field |
| Phase 5 merge | `params_merged.json` + confidence; never infer requiredness from one sample |

### J10. Troubleshooting

| Symptom | Action |
|---|---|
| Static fields never appear at runtime | Mark "pending trigger"; complete the permission tree / click advanced filters / try each dependent select option |
| Different body shapes for the same path | Normal — record separately by `action`; do not force them into one schema |
| Stub responses are fake but parameters are needed | **Inspect outbound request** bodies/headers; do not infer from stub responses |
| 400 reports a nested field | Account for outer `data`/`bizData`/`variables` wrappers |
| GraphQL only shows operation names | Expand the `variables` JSON; find `$var: Type` statically |

---
