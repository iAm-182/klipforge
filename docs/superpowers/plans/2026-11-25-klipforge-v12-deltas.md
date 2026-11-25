# KlipForge v1.2a + v1.2b Delta Implementation Plan (Deltas 1–3)

> **Date:** 2026-11-25
> **Spec Source:** `klipforge-improve-gabungan-siap-paste.md` (v1.2a + Appendix v1.2b verified GLM 7/7)
> **Method:** Superpowers (Plan -> Strict TDD RED/GREEN/REFACTOR -> Fresh Subagent per Task + Independent Review -> Fresh Verification)
> **Hard Locks Honored:**
> - P2 (`I-11`..`I-16`: LAION/PANNs, recall@K harness, TransNetV2, IndoBERT, heatmap fix, `klip.json`) stays LOCKED until `I-11` (1 held-out VOD) passes + owner go.
> - `internal/analyzer/virality.go` FROZEN (zero edits).
> - `cloudflare/src/index.js` UNTOUCHED (zero edits).
> - Posting STAYS MANUAL (zero auto-post).
> - NO `git push`, NO deploy, NO deletion until explicit owner go.

### Explicit Ruling
- **Ruling (Proven 16:9 Watermark Preset Coordinates):** Use `{ aspectRatio: "16:9", cropMode: "original", resolution: "1080p", subtitlesEnabled: false, sceneSnap: false, silenceTrim: false, watermark: { enabled: true, position: "custom", customX: 0.375, customY: 0.0, scale: 0.25, opacity: 0.96, onOutro: false } }` — extracted directly from all 3 approved clips in live InstaBox `jobs.json` (`Red Hulk Smash`, `Mancing Dapet Ikan Loncat`, `Sepeda Mental Ragdoll`, `IN-SAMPLE`). Risk if wrong: low (1-line constant in `cutter_helpers.js`, plus drag-watermark stays editable in Studio).

---

## Delta 1 — Complete I-04 Views Ledger Schema (4 H1 Flags, Confounders, Cohort, Adaptive Ramai) + Candidate H1/HookLine Metadata
- **Files:** `internal/model/types.go`, `internal/store/ledger.go`, `internal/store/ledger_test.go`, `internal/server/p0_ops.go`, `internal/server/p0_ops_test.go`, `internal/server/server.go` (`handleCandidateVerdict`), `skills/klipforge-orchestrator/SKILL.md`.
- **Exact Fields Added to `model.ViewsLedgerEntry`:**
  - `CohortTag string` (`json:"cohortTag,omitempty"` — `"PRE-v1.2"` | `"POST-v1.2"`, default `"POST-v1.2"`)
  - `HookType string` (`json:"hookType,omitempty"` — `""` | `"tableau"` | `"reaksi"` | `"aksi"` | `"text_on_screen"`)
  - `TriggerSource string` (`json:"triggerSource,omitempty"` — `""` | `"manual"` | `"transcript"` | `"chat"` | `"audio"` | `"llm"`)
  - `Views24h int64` (`json:"views24h,omitempty"`), `Views7d int64` (`json:"views7d,omitempty"`)
  - `ChannelState string` (`json:"channelState,omitempty"`), `PostedDayTime string` (`json:"postedDayTime,omitempty"`)
  - `ThumbnailTs string` (`json:"thumbnailTs,omitempty"`), `CueSelectionUsed bool` (`json:"cueSelectionUsed,omitempty"`)
  - `H1TawaFacecam bool` (`json:"h1TawaFacecam,omitempty"`), `H1VisualKejadian bool` (`json:"h1VisualKejadian,omitempty"`), `H1ArcUtuh bool` (`json:"h1ArcUtuh,omitempty"`), `H1HookMutePass bool` (`json:"h1HookMutePass,omitempty"`), `H1PassCount int` (`json:"h1PassCount,omitempty"` — auto-computed `0..4`)
- **Exact Fields Added to `model.ViewsLedgerSummary`:**
  - `RamaiThreshold int64` (`json:"ramaiThreshold"` — `2 * MedianViews` when `TotalEntries >= 3`, else `0`)
  - `RamaiStatus string` (`json:"ramaiStatus"` — `"PLACEHOLDER_DUGAAN_N_LT_3"` | `"BASELINE_AWAL_DUGAAN_N_LT_10"` | `"AKTIF_2X_MEDIAN_N_GE_10"`)
  - `ByCohort map[string]ViewsBucketStats` (`json:"byCohort,omitempty"`)
  - `ByTrigger map[string]ViewsBucketStats` (`json:"byTrigger,omitempty"`)
  - `ByHookType map[string]ViewsBucketStats` (`json:"byHookType,omitempty"`)
  - `H1FullPassCount int` (`json:"h1FullPassCount"`)
- **Exact Fields Added to `model.CandidateVerdict` (P1 format `I-15` + H1 checklist — metadata only, zero score selection):**
  - `HookLine string` (`json:"hookLine,omitempty"` — standalone 1-line hook)
  - `H1TawaFacecam bool`, `H1VisualKejadian bool`, `H1ArcUtuh bool`, `H1HookMutePass bool`
- **TDD Gate:** Write failing tests FIRST in `internal/store/ledger_test.go` & `internal/server/p0_ops_test.go` (RED), implement minimal code (GREEN), verify `go test -v ./internal/store ./internal/server -run "TestViewsLedger|TestSummarize|TestP0Ops|TestPhase4_4"`.

## Delta 2 — Proven 16:9 Preset + H1/AutoClip 1.5s Mute-Test Checklist + Full Ledger UI + Static Sync
- **Files:** `web/cutter_helpers.test.js`, `web/cutter_helpers.js`, `web/index.html`, `web/app.js`, `web/b1_dashboard.js`, `web/b1_theme.css`, `cloudflare/public/*`.
- **TDD Gate (Node):** Write failing tests in `web/cutter_helpers.test.js` FIRST (RED):
  - `preset16x9TopCenterWM()` returns proven 3-clip recipe (`customX: 0.375, customY: 0.0, scale: 0.25, opacity: 0.96, onOutro: false, subtitlesEnabled: false, sceneSnap: false, silenceTrim: false`).
  - `evaluateH1Checklist(flags)` computes `{ passedCount, total: 4, publishable: passedCount === 4, missing: [...] }` for the 4 owner H1 criteria (F1 tawa facecam, F2 kejadian/fisika terlihat di frame, F3 arc 20–55 dtk utuh, F4 hook 1–3 dtk lolos mute test / aturan 1,5 dtk AutoClip).
- **UI Wiring:**
  - `#btnPreset16x9WM` applies the full proven 16:9 recipe (including `chkSubtitles=false`, `chkSceneSnap=false`, `chkSilenceTrim=false`, `customX=0.375`, `customY=0.0`, `scale=0.25`).
  - Candidate cards & Review Sheet show compact **4 Flag H1 + Mute Test 1,5 dtk** checklist + **Hook Line & Alasan 1 Kalimat** (`I-15` format without score-based selection!).
  - Views Ledger form & table in `#tab-queue` expose `cohortTag` (`PRE-v1.2`/`POST-v1.2`), `hookType`, `triggerSource`, `views24h`/`views7d`, `channelState`, `thumbnailTs`, `cueSelectionUsed`, 4 H1 checkboxes, and the adaptive `ramaiThreshold` status badge (`PLACEHOLDER / DUGAAN` when `n < 10`).
  - Sync all 5 static assets (`cutter_helpers.js`, `index.html`, `app.js`, `b1_dashboard.js`, `b1_theme.css`) to `cloudflare/public/`.

## Delta 3 — Third-Party License Notices & Register (`v1.2b §4(c)`)
- **File:** `THIRD-PARTY-NOTICES.md` at repo root documenting all MIT/Apache/BSD tools & libraries actually used in KlipForge, plus the explicit v1.2b compliance register (🟩 Allowed MIT/Apache, 🟨 Conditional Design/Dual-License Code+Weights, 🟥 Prohibited AGPLv3/Noncommercial/Research-Only) and Clean-Room protocol.
