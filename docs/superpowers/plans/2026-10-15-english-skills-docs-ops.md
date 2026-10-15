# Skills, Documentation, and Operations English Translation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Translate built-in skills, operational scripts/configuration, repository documentation, and the current changelog to English without changing executable behavior.

**Architecture:** Translate prose in place, preserve commands and structured examples, rename the sole tracked non-English documentation filename, and validate every script type with its native parser plus a final scoped Han scan.

**Tech Stack:** Markdown, Bash, Windows batch, YAML, JSON, Docker Compose, Python, JavaScript, Git.

---

## File Map

- Modify: `skills/api-recon/SKILL.md`, `skills/api-recon/reference.md`, `skills/api-recon/scripts/harvest_static.py`, `skills/api-recon/scripts/preload.js`, `skills/api-recon/scripts/runtime_harvest.js`, `skills/api-recon/scripts/spider_mpa.py`, and `skills/scopesentry/SKILL.md`.
- Modify: `sidequestion/README.md`, `sidequestion/CONTEXT_BUDGET.md`, `sidequestion/VALIDATION.md`, and `sidequestion/validation-2026-09-10.json`.
- Rename: the sole tracked legacy Markdown file directly under `docs/` to `docs/vulnerability-traffic-evidence.md`, then translate it.
- Modify: `README.md`, `CHANGELOG.md`, `.env.example`, `.gitignore`, `.github/workflows/release.yml`, `Dockerfile`, `build.sh`, `dev.sh`, `install.sh`, `reset-password.sh`, `start.sh`, `start.bat`, `update.sh`, `config.example.json`, `docker-compose.yml`, and `docker-compose.bench.yml`.

### Task 1: Freeze the Skills and Operations Inventory

**Files:**
- Read: all files in the File Map.

- [ ] **Step 1: Save the exact inventory and RED scan**

Run:

```bash
git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- \
  skills sidequestion README.md CHANGELOG.md .env.example .gitignore \
  .github/workflows/release.yml Dockerfile build.sh dev.sh install.sh \
  reset-password.sh start.sh start.bat update.sh config.example.json \
  docker-compose.yml docker-compose.bench.yml docs \
  > /tmp/artex-docs-ops-han-before.txt
test -s /tmp/artex-docs-ops-han-before.txt
```

Expected: exit 0 with a non-empty baseline.

### Task 2: Translate the API Recon Skill

**Files:**
- Modify: `skills/api-recon/SKILL.md`
- Modify: `skills/api-recon/reference.md`
- Modify: `skills/api-recon/scripts/harvest_static.py`
- Modify: `skills/api-recon/scripts/preload.js`
- Modify: `skills/api-recon/scripts/runtime_harvest.js`
- Modify: `skills/api-recon/scripts/spider_mpa.py`

- [ ] **Step 1: Translate instructions without weakening gates**

Preserve frontmatter keys, phase numbers, mandatory ordering, boundary rules, output filenames, regular expressions, commands, JSON keys, and completion criteria. Translate comments and runtime messages in scripts without changing selectors, matching logic, exit codes, or generated schemas.

- [ ] **Step 2: Validate the skill and scripts**

Run:

```bash
python3 -m py_compile skills/api-recon/scripts/harvest_static.py \
  skills/api-recon/scripts/spider_mpa.py
node --check skills/api-recon/scripts/preload.js
node --check skills/api-recon/scripts/runtime_harvest.js
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- skills/api-recon
```

Expected: all parsers exit 0 and the scoped scan has no output.

- [ ] **Step 3: Commit**

```bash
git add skills/api-recon
git commit -m "docs(skill): translate API recon workflow"
```

### Task 3: Translate the ScopeSentry Skill and Side-Question Documents

**Files:**
- Modify: `skills/scopesentry/SKILL.md`
- Modify: `sidequestion/README.md`
- Modify: `sidequestion/CONTEXT_BUDGET.md`
- Modify: `sidequestion/VALIDATION.md`
- Modify: `sidequestion/validation-2026-09-10.json`

- [ ] **Step 1: Translate prose and fixture text**

Preserve skill frontmatter, MCP server names, tool names, JSON keys, IDs, numeric validation results, commands, and code examples. Translate only human-readable explanations and fixture content.

- [ ] **Step 2: Validate structure**

Run:

```bash
python3 -m json.tool sidequestion/validation-2026-09-10.json >/dev/null
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- \
  skills/scopesentry sidequestion
```

Expected: JSON validation exits 0 and no Han matches remain in this scope.

- [ ] **Step 3: Commit**

```bash
git add skills/scopesentry sidequestion
git commit -m "docs: translate ScopeSentry and side-question guides"
```

### Task 4: Translate Operational Configuration and Scripts

**Files:**
- Modify: `.env.example`
- Modify: `.gitignore`
- Modify: `.github/workflows/release.yml`
- Modify: `Dockerfile`
- Modify: `build.sh`
- Modify: `dev.sh`
- Modify: `install.sh`
- Modify: `reset-password.sh`
- Modify: `start.sh`
- Modify: `start.bat`
- Modify: `update.sh`
- Modify: `config.example.json`
- Modify: `docker-compose.yml`
- Modify: `docker-compose.bench.yml`

- [ ] **Step 1: Translate prompts, comments, and status output**

Preserve environment variable names, image tags, service names, ports, paths, flags, quoting, exit codes, and control flow. Keep ASCII menu choices compatible with existing input handling; translate only their displayed labels.

- [ ] **Step 2: Validate parsers**

Run:

```bash
for script in build.sh dev.sh install.sh reset-password.sh start.sh update.sh; do
  bash -n "$script" || exit
done
python3 -m json.tool config.example.json >/dev/null
docker compose -f docker-compose.yml config >/dev/null
docker compose -f docker-compose.bench.yml config >/dev/null
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- \
  .env.example .gitignore .github/workflows/release.yml Dockerfile build.sh \
  dev.sh install.sh reset-password.sh start.sh start.bat update.sh \
  config.example.json docker-compose.yml docker-compose.bench.yml
```

Expected: all available parsers exit 0 and the scoped scan has no output. If Docker is unavailable, record that limitation and still validate YAML syntax through the repository's available tooling.

- [ ] **Step 3: Commit**

```bash
git add .env.example .gitignore .github/workflows/release.yml Dockerfile build.sh \
  dev.sh install.sh reset-password.sh start.sh start.bat update.sh config.example.json \
  docker-compose.yml docker-compose.bench.yml
git commit -m "refactor(ops): translate operational scripts"
```

### Task 5: Rename and Translate the Tracked Design Note

**Files:**
- Rename: the single legacy Markdown file directly under `docs/` to `docs/vulnerability-traffic-evidence.md`
- Modify: every tracked reference to that document.

- [ ] **Step 1: Resolve and rename the exact source file**

Run:

```bash
git ls-files 'docs/*.md'
```

Expected: exactly one legacy document outside `docs/superpowers/`. Rename that file with `git mv` to `docs/vulnerability-traffic-evidence.md`.

- [ ] **Step 2: Translate the document and references**

Preserve headings, code blocks, schemas, IDs, commands, and link targets. Update every repository reference to the new English path. Also translate stale non-English documentation path mentions in comments to descriptive English paths without creating nonexistent files.

- [ ] **Step 3: Verify the rename**

Run:

```bash
test -f docs/vulnerability-traffic-evidence.md
git ls-files | LC_ALL=C grep -P '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' && exit 1 || true
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- docs
```

Expected: the English path exists, no tracked filename contains Han, and docs contain no Han text.

- [ ] **Step 4: Commit**

```bash
git add -A docs
git commit -m "docs: translate vulnerability traffic evidence guide"
```

### Task 6: Translate README and Changelog

**Files:**
- Modify: `README.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Translate current repository documentation**

Keep release numbers, dates, issue/PR references, URLs, commands, code blocks, environment variables, image paths, anchors, and API identifiers unchanged. Translate all headings, prose, image alt text, tables, warnings, and the full current changelog history. Update the `wx.png` description to English.

- [ ] **Step 2: Verify documentation text and local links**

Run:

```bash
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- README.md CHANGELOG.md
rg -o 'screenshots/[A-Za-z0-9_.-]+\.png' README.md | while read -r image; do test -f "$image" || exit 1; done
```

Expected: no Han matches and every referenced screenshot exists.

- [ ] **Step 3: Commit**

```bash
git add README.md CHANGELOG.md
git commit -m "docs: translate project documentation to English"
```

### Task 7: Complete Skills and Documentation Verification

**Files:**
- Verify: every file in this plan's File Map.

- [ ] **Step 1: Run the complete scoped scan**

Run:

```bash
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- \
  skills sidequestion README.md CHANGELOG.md .env.example .gitignore \
  .github/workflows/release.yml Dockerfile build.sh dev.sh install.sh \
  reset-password.sh start.sh start.bat update.sh config.example.json \
  docker-compose.yml docker-compose.bench.yml docs
git diff --check
```

Expected: no grep output and clean whitespace validation.
