---
name: klipforge-orchestrator
description: Orchestrate KlipForge Go clipping engine via its native Streamable HTTP MCP server (POST /api/mcp, 29 least-privilege tools) or REST API using X-Agent-Key for Live Chat Replay Pro, AutoClip one-pass transcript windows, Vision Storyboards, I-05 Score-Ban Guardrail, shot-aware YuNet framing, CapCut-Pro editing, Kanban workflow, and P0 manual-posting/views-ledger observability.
---

# KlipForge Orchestrator Skill (Carbon / Muse / MCP Clients)

Use this skill whenever orchestrating clipping workflows on **KlipForge** (`https://klip.breakthematrix.id` or local Go engine).

## 1. MCP Connection (Streamable HTTP JSON-RPC 2.0)

- **Endpoint**: `POST / GET / DELETE https://klip.breakthematrix.id/api/mcp`
- **Header**: `X-Agent-Key: <member-or-owner-agent-key>`
- **Protocol**: MCP Streamable HTTP (`2025-03-26`, JSON-RPC `2.0`, single & batch arrays `[...]`)
- Never send destructive or human-only requests (`DELETE` except session teardown `/api/mcp`, QC approve/reject, `POST /api/jobs/{id}/mark-posted`, `POST /api/ledger/views`, `/api/team`, `/api/publish/accounts`, `/api/settings/cookies/refresh`) — they are strictly Owner/QC/human-only and return `403`.

## 2. Hard Operational Guardrails (P0)

1. **I-05 Score-Ban Guardrail (Mandatory Before Candidate Render)**:
   - NEVER render a `candidateId` blindly from auto-detection hype/virality scores alone.
   - Any agent render (`X-Agent-Key`) referencing a `candidateId` MUST FIRST have:
     1. Explicit verdict `verdict: "keep"` saved via `klipforge_set_verdict` (after reading transcript context), **AND**
     2. Visual verification evidence on frames (`klipforge_verify_frames` populating `startFrameUrl`/`peakFrameUrl`, `klipforge_sample_storyboard` producing a project storyboard JPEG, or explicit `frameVerified: true`).
   - Rendering an unverified `candidateId` is strictly rejected with **`409 Conflict`**.
   - Direct timestamp range cuts (`candidateId == ""`) chosen from transcript/storyboard verification remain allowed.
2. **Manual Posting Only & Views Ledger Discipline (I-02 / I-03 / I-04)**:
   - Posting is **100% manual by Owner/QC** (`0 auto-post`). Agents may **only observe** via read-only tools:
     - `klipforge_inventory` (`GET /api/inventory`, `dataScope: "IN-SAMPLE"`) — audit jobs vs MP4 files on disk, orphan MP4s, and rejection reason histogram.
     - `klipforge_publishable_queue` (`GET /api/publishable`) — inspect FIFO queue of `done + approved` clips with files on disk (`postedPlatforms`, `remainingPlatforms`, captions, download URLs, recorded views).
     - `klipforge_views_ledger` (`GET /api/ledger/views`) — inspect manually logged views/likes/comments/shares and summary splits.
   - **Data Scope Rule**: Always preserve `IN-SAMPLE` vs `HELD-OUT` labels; NEVER treat `IN-SAMPLE` metrics as `HELD-OUT` proof.
   - Agents **cannot** call `POST /api/jobs/{id}/mark-posted` (Owner/QC only -> `403`) or `POST /api/ledger/views` (Owner/QC/Clipper only -> `403`).

## 3. Standard End-to-End Viral Clipping SOP

1. **Preflight Engine, Cookies & Inventory**:
   - Call `klipforge_home_metrics`, `klipforge_cookie_status`, and `klipforge_inventory` / `klipforge_publishable_queue`.
   - If `cookieHealth.state` is `expired` or `missing`, alert the owner to upload fresh Chrome Incognito cookies before YouTube ingestion.
2. **Audio-First Ingestion + Live Chat Replay Pro**:
   - Call `klipforge_analyze_video` with `audioFirst: true` (saves ~95% disk on InstaBox Cloud).
   - For existing or 3–5 hour livestream VODs where `signals.hasLiveChat` is `false`, call **`klipforge_scan_livechat`** (`sync: true`) to fetch parallel Innertube Live Chat Replay with trailing reaction lag (`+3s..+15s`), unique-chatter anti-spam, and Visual Gag Boost.
3. **One-Pass Whole-VOD Picking + Vision Storyboard Verification (AutoClip Techniques)**:
   - Call **`klipforge_transcript_windows`** (`window: 0`, `windowMin: 25`, `overlapSec: 90`) to read AutoClip-style `id|MM:SS|text` lines enriched with `[SPEAKER_X]`, `[CHAT:N]`, `[AUD:N]`, and synthetic `[VISUAL_CHAT_SPIKE]` silent-visual-gag markers.
   - Call **`klipforge_sample_storyboard`** (`startSec`, `endSec`, `count: 9`, `cols: 3`, `includeBase64: true`) to visually inspect a labeled contact-sheet JPEG (works even in Audio-First mode!) so menus/loading screens are dropped and crashes/visual jokes are verified directly on frames.
4. **Pre-Edit Intelligence (Optional / Recommended for VODs)**:
   - `klipforge_detect_facecam` → finds gaming webcam box (`top-left`, `top-right`, `bottom-left`, `bottom-right`) so split layouts stay accurately framed.
   - `klipforge_detect_scenes` → aligns candidate cut boundaries to real shot cuts (`PySceneDetect` / FFmpeg scene fallback).
   - `klipforge_diarize_project` → assigns `SPEAKER_0` (main streamer) vs `SPEAKER_1` (NPC/guest) for speaker-colored karaoke.
5. **Frame Verification + Mandatory Agent Verdict (I-05 Gate)**:
   - Call `klipforge_verify_frames` (or `klipforge_sample_storyboard`) and inspect visual proof.
   - Save evaluation with `klipforge_set_verdict` (`verdict: "keep" | "drop" | "maybe"`, `funny: 0..5`, `momentType: "fail" | "meme" | "crash" | "victory" | "hype" | "twist"`). Verdicts never auto-render, and only `verdict: "keep"` + visual proof can pass I-05 candidate render checks.
6. **Single-Pass CapCut-Pro Render (`klipforge_render_edit_script` / `klipforge_render_single`)**:
   - Use `layout.cropMode`: **`face_track_smart`** (shot-aware YuNet crop on face shots + blurred full-frame `fit` on landscape/menu shots), `face_track`, `facecam_top_neon` / `gaming_cinema` (split gameplay + auto facecam), or `16:9` widescreen when requested by campaign.
   - Use `subtitles`: `enabled: true, preset: "karaoke"`.
   - Use `audioFx`: `sfxAutoPreset: "auto_by_moment", autoDuck: true, vocalEnhance: true` + top-level `audioNorm: true` (`-14 LUFS`).
   - Keep `useWatermark: true` and `useOutro: true`. Watch Fair Queue WIP limit (max 3 active jobs/member -> `429`).
7. **Review Handoff, Manual Publish Queue & Views Ledger Observation**:
   - Poll with `klipforge_get_job` (or `klipforge_list_jobs` which always enforces `summary=1`).
   - Completed jobs land in `review` for human QC approval. Do **not** try to self-approve or auto-post.
   - Once approved, generate `klipforge_generate_publish_kit` (TikTok + YT Shorts <=100 chars + IG Reels) and optionally `klipforge_export_jianying_draft` (`.zip` CapCut desktop draft).
   - Track manual posting readiness via `klipforge_publishable_queue` and observe logged `IN-SAMPLE` / `HELD-OUT` results via `klipforge_views_ledger`.

## 4. All 29 Least-Privilege MCP Tools

- **Read/Observe (15)**: `klipforge_home_metrics`, `klipforge_cookie_status`, `klipforge_list_projects`, `klipforge_get_project`, `klipforge_search_transcript`, `klipforge_transcript_windows`, `klipforge_list_board`, `klipforge_list_jobs`, `klipforge_get_job`, `klipforge_activity`, `klipforge_team_metrics`, `klipforge_audio_and_moment_map`, `klipforge_inventory`, `klipforge_publishable_queue`, `klipforge_views_ledger`
- **Analyze/Pre-Edit (8)**: `klipforge_analyze_video`, `klipforge_scan_livechat`, `klipforge_sample_storyboard`, `klipforge_detect_scenes`, `klipforge_detect_facecam`, `klipforge_diarize_project`, `klipforge_verify_frames`, `klipforge_set_verdict`
- **Produce/Deliver (6)**: `klipforge_move_board`, `klipforge_render_single`, `klipforge_render_edit_script`, `klipforge_post_edit`, `klipforge_generate_publish_kit`, `klipforge_export_jianying_draft`
