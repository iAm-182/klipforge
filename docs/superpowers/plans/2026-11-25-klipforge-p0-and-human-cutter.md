# KlipForge P0 Operations + Human-Friendly Cutter Implementation Plan

> **Date:** 2026-11-25
> **Method:** Superpowers (Plan -> Strict TDD RED/GREEN/REFACTOR -> Fresh Subagent per Task + Review -> Fresh Verification)
> **Hard Safety Gates (NO execution without explicit owner `go`):**
> - NO `git push`, NO `merge`, NO live InstaBox/Cloudflare deploy, NO `wrangler deploy`.
> - NO deletion of existing clips/jobs or mutation of production settings.
> - Posting STAYS MANUAL (`0 auto-post`).
> - NO AGPL/noncommercial code; NO score leakage into `internal/analyzer/virality.go`.
> - Every metric labeled `IN-SAMPLE` / `HELD-OUT` / `DUGAAN`.

---

## Context & Problem Statement

Audit of live InstaBox production data (`jobs.json`, `IN-SAMPLE` as of 2026-11-25):
- 37 jobs total: **36 `done`** on disk (`/data/workspace/klipforge/outputs/Windah_Basudara_GTA_VI`), 1 `error`/missing (`comp-1795478621750617067`).
- Only **3 real clips approved** (`Red Hulk Smash`, `Mancing Dapet Ikan Loncat`, `Sepeda Mental Ragdoll` — all `16:9` with top-center watermark).
- **33 clips rejected** with real owner reasons (`jelek`, `masih kurang momentnya`, `momentnya gak pas`, `walaupun peak kuat, aneh momentnya kalau ditonton`).
- Owner UX blocker:
  1. Auto-cut moments miss Windah's comedic timing (jokes often decoupled from gameplay visuals).
  2. Manual cutting forces raw-second math (`7450`, `5335`) in `<input type="number">` without `HH:MM:SS`, Mark In/Out, transcript click-to-cut, or 1-click range storyboard verification.

### Explicit Architectural Rulings
1. **Ruling (I-05 Score-Ban Guardrail):** Protect agent & autopilot pipelines from blind score-based selection without breaking manual human range cuts (`candidateId == ""`). Specifically:
   - Agent render calls (`X-Agent-Key`) or explicit `requireVerified=true` calls referencing a `candidateId` MUST have `verdict == "keep"` AND visual verification evidence (candidate `startFrameUrl`/`peakFrameUrl`, `frameVerified=true`, or existing project storyboard); otherwise return `409 Conflict`.
   - `internal/autopilot/scheduler.go` MUST NOT auto-enqueue unverified candidates by top-N score; only candidates with `Verdict.Verdict == "keep"` may be queued (defaulting new VODs to human review/selection rather than flooding Review Inbox with auto-cuts).
   - Keep `internal/analyzer/virality.go` untouched.
   - *Risk if wrong:* Existing tests or human UI clicking candidate render with `owner` principal still work; `autopilot` no longer auto-renders unverified clips (which is the exact intent of score-ban).
2. **Ruling (I-02 Read-Only Inventory + Rescue):** Provide non-destructive `GET /api/inventory` auditing every job against disk files and detecting orphan MP4s in `OutputDir`. Never auto-delete missing/rejected jobs.
3. **Ruling (I-03 Manual Publishable Queue):** Add `GET /api/publishable` (only `status=="done" && reviewStatus=="approved"` with existing MP4 on disk, FIFO) and `POST /api/jobs/{id}/mark-posted` (Owner/QC only, `method: "manual"`, `status: "published"`, moves job to `diposting` — zero auto-posting).
4. **Ruling (I-04 Views Ledger):** Persist manual post performance entries in `data/views_ledger.json` via atomic writes (`writeJSONAtomic`), exposed via `GET /api/ledger/views` and `POST /api/ledger/views` with explicit `sampleSplit` (`IN-SAMPLE` | `HELD-OUT`, defaulting to `IN-SAMPLE`).
5. **Ruling (Human-Friendly Cutter):** Build pure testable `web/cutter_helpers.js` (dual Browser + Node CommonJS exports) with `HH:MM:SS` parsing/formatting, range nudging, duration validation, and `16:9 WM Tengah-Atas` preset; wire into Studio UI + interactive transcript browser (`GET /api/projects/{id}/transcript`) + Mark In (`I`) / Mark Out (`O`) + 1-click range Storyboard preview; sync all web static files to `cloudflare/public/` without touching `cloudflare/src/index.js`.

---

## Task Breakdown (Independent Units, Strict TDD per Task)

### Task 1 — Views Ledger Store & Model (`I-04`)
- **Owner/Scope:** Pure persistence layer in `internal/model` & `internal/store`.
- **Files to edit/create:**
  - `internal/model/types.go` — add `ViewsLedgerEntry` struct.
  - `internal/store/ledger.go` (NEW) — `AddViewsLedgerEntry`, `ListViewsLedger`, `SummarizeViewsLedger`, atomic JSON persistence (`data/views_ledger.json`).
  - `internal/store/store.go` — initialize `viewsLedger []*model.ViewsLedgerEntry` field and load in `loadAll()`.
  - `internal/store/ledger_test.go` (NEW) — write FIRST (RED): test add/list/filter by `jobId`/`platform`/`split`, reload persistence across `NewStore`, invalid platform/negative views validation, median/totals summary & default `IN-SAMPLE` label.
- **Verification:** `go test -v ./internal/store`

### Task 2 — Score-Ban Guardrail (`I-05`) in Server & Autopilot
- **Owner/Scope:** Enforce transcript+frame+keep-verdict gate on score-selected candidates & autopilot.
- **Files to edit/create:**
  - `internal/model/types.go` — add `RequireVerified bool` and `FrameVerified bool` to `RenderOptions`.
  - `internal/server/guardrail.go` (NEW) — `validateCandidateRenderGuardrail(p model.Principal, opts model.RenderOptions) error`.
  - `internal/server/server.go` — wire guardrail into `handleRenderSingle` and `handleRenderBatch` (return `409 Conflict` when blocked).
  - `internal/autopilot/scheduler.go` — replace blind top-N score loop with `selectVerifiedCandidatesForAutopilot(cands []model.ClipCandidate, limit int) []model.ClipCandidate` that ONLY selects candidates with `Verdict != nil && Verdict.Verdict == "keep"`.
  - `internal/autopilot/scheduler_test.go` (NEW) & `internal/server/guardrail_test.go` (NEW) — write FIRST (RED):
    - Agent rendering unverified candidate -> `409 Conflict`.
    - Agent rendering candidate with `verdict=keep` + frame URL (or storyboard) -> `202 Accepted`.
    - Manual human cut (`candidateId == ""`) -> always allowed (`202 Accepted`).
    - Autopilot with 5 high-score unverified candidates -> queues `0` clips (score-ban!); queues ONLY `keep`-verified candidate.
- **Verification:** `go test -v ./internal/autopilot ./internal/server -run "TestScoreBan|TestAutopilotScoreBan"`

### Task 3 — Inventory (`I-02`), Publishable Queue (`I-03`), Manual Mark-Posted, Views Ledger & Transcript Browser APIs + 29 MCP Tools
- **Owner/Scope:** REST API endpoints, RBAC rules, read-only MCP observability tools (26 -> 29), and `SKILL.md`.
- **Files to edit/create:**
  - `internal/server/p0_ops.go` (NEW):
    - `GET /api/inventory` -> `handleGetInventory`
    - `GET /api/publishable` -> `handleListPublishable`
    - `POST /api/jobs/{id}/mark-posted` -> `handleMarkJobPosted` (Owner/QC only; 403 for agent/clipper; 409 if not approved or file missing)
    - `GET /api/ledger/views` -> `handleListViewsLedger`
    - `POST /api/ledger/views` -> `handleAddViewsLedger` (Owner/QC/Clipper; 403 for agent)
    - `GET /api/projects/{id}/transcript` -> `handleGetProjectTranscript` (`offset`, `limit`, `fromSec`, `toSec`, `q`)
  - `internal/server/server.go` — register all 6 routes.
  - `internal/server/team.go` — allow `qc`/`clipper` on `POST /api/ledger/views`, allow `qc` on `POST /api/jobs/{id}/mark-posted` (keep `agent` strictly denied on both!).
  - `internal/server/mcp.go` — add 3 read-only tools (`klipforge_inventory`, `klipforge_publishable_queue`, `klipforge_views_ledger` -> 29 total).
  - `internal/server/p0_ops_test.go` (NEW) & `internal/server/mcp_test.go` — write FIRST (RED): test all 6 routes, RBAC enforcement, orphan MP4 detection, manual mark-posted flow, ledger summary, transcript pagination, and 29 MCP tools.
  - `skills/klipforge-orchestrator/SKILL.md` — document 29 MCP tools + score-ban + manual-post rules.
- **Verification:** `go test -v ./internal/server -run "TestP0Ops|TestMCP"`

### Task 4 — Pure Frontend Cutter Helpers (`web/cutter_helpers.js`) with Node TDD
- **Owner/Scope:** Timecode parsing/formatting, range nudging, duration summary, and `16:9 WM Tengah-Atas` preset logic.
- **Files to create:**
  - `web/cutter_helpers.test.js` (NEW) — write FIRST (RED) using Node's built-in `node:assert/strict`.
  - `web/cutter_helpers.js` (NEW) — UMD/dual export (`window.KFCutter` + `module.exports`) implementing `parseTimecode`, `formatTimecode`, `nudgeRange`, `formatRangeSummary`, `preset16x9TopCenterWM`.
- **Verification:** `node web/cutter_helpers.test.js`

### Task 5 — Studio Human-Friendly Cutter + Transcript Browser + Publishable & Views Ledger UI (+ Sync `cloudflare/public/`)
- **Owner/Scope:** Wire `cutter_helpers.js` into Studio UI, add `HH:MM:SS` manual cutter with Mark In (`I`) / Mark Out (`O`) + nudge buttons + 1-click Storyboard Range Preview + `16:9 WM Tengah-Atas` preset + interactive Transcript Browser with click-to-set Start/End + demote score badge to "Radar Sinyal" + Publishable Queue / Mark Posted / Views Ledger panel in Library/Review; sync static files to `cloudflare/public/` (NEVER touch `cloudflare/src/index.js`).
- **Files to edit:**
  - `web/index.html`
  - `web/app.js`
  - `web/b1_dashboard.js`
  - `web/b1_theme.css`
  - Sync -> `cloudflare/public/{cutter_helpers.js,index.html,app.js,b1_dashboard.js,b1_theme.css}`
- **Verification:** `node web/cutter_helpers.test.js`, `node --check web/cutter_helpers.js web/app.js web/b1_dashboard.js cloudflare/public/cutter_helpers.js cloudflare/public/app.js cloudflare/public/b1_dashboard.js`, `git diff cloudflare/src/index.js` empty.

### Final Branch Review & Fresh Full Verification
- Run complete fresh verification suite:
  1. `node web/cutter_helpers.test.js`
  2. `node --check` on all edited JS files (`web/` + `cloudflare/public/`)
  3. `go vet ./...` (Windows) + `GOOS=linux GOARCH=amd64 go vet ./...` (Linux)
  4. `go test -p 1 ./...` (0 failures)
  5. `go build -ldflags="-s -w" -o klipforge.exe .` (exit 0)
  6. `git diff --stat` & `git log` review — verify zero changes to `cloudflare/src/index.js`, zero changes to `internal/analyzer/virality.go`, zero secrets, zero push/deploy actions.
