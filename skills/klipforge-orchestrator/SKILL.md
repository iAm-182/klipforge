---
name: klipforge-orchestrator
description: Orchestrate KlipForge Go clipping engine via its native Streamable HTTP MCP server (POST /api/mcp) or REST API using least-privilege X-Agent-Key for multi-signal analysis, facecam detection, CapCut-Pro editing, Kanban workflow, and publish prep.
---

# KlipForge Orchestrator Skill (Carbon / Muse / MCP Clients)

Use this skill whenever orchestrating clipping workflows on **KlipForge** (`https://klip.breakthematrix.id` or local Go engine).

## 1. MCP Connection (Streamable HTTP JSON-RPC 2.0)

- **Endpoint**: `POST / GET / DELETE https://klip.breakthematrix.id/api/mcp`
- **Header**: `X-Agent-Key: <member-or-owner-agent-key>`
- **Protocol**: MCP Streamable HTTP (`2025-03-26`, JSON-RPC `2.0`, single & batch arrays `[...]`)
- Never send destructive requests (`DELETE` except session teardown `/api/mcp`, QC approve/reject, `/api/team`, `/api/publish/accounts`, `/api/settings/cookies/refresh`) — they are strictly Owner/QC-only and return `403`.

## 2. Standard End-to-End Viral Clipping SOP

1. **Preflight Engine & Cookies**:
   - Call `klipforge_home_metrics` and `klipforge_cookie_status`.
   - If `cookieHealth.state` is `expired` or `missing`, alert the owner to upload fresh Chrome Incognito cookies before YouTube ingestion.
2. **Audio-First Ingestion**:
   - Call `klipforge_analyze_video` with `audioFirst: true` (saves ~95% disk on InstaBox Cloud).
3. **Pre-Edit Intelligence (Optional / Recommended for VODs)**:
   - `klipforge_detect_facecam` → finds gaming webcam box (`top-left`, `top-right`, `bottom-left`, `bottom-right`) so split layouts stay accurately framed.
   - `klipforge_detect_scenes` → aligns candidate cut boundaries to real shot cuts (`PySceneDetect` / FFmpeg scene fallback).
   - `klipforge_diarize_project` → assigns `SPEAKER_0` (main streamer) vs `SPEAKER_1` (NPC/guest) for speaker-colored karaoke.
4. **Frame Verification + Agent Verdict**:
   - Call `klipforge_verify_frames` (or move candidate via `klipforge_move_board` to `verifikasi_frame`).
   - Save evaluation with `klipforge_set_verdict` (`verdict: "keep" | "drop" | "maybe"`, `funny: 0..5`, `momentType: "fail" | "meme" | "crash" | "victory" | "hype" | "twist"`). Verdicts never auto-render.
5. **Single-Pass CapCut-Pro Render (`klipforge_render_edit_script`)**:
   - Use `layout.cropMode`: `face_track` (full vertical speaker follow) or `facecam_top_neon` / `gaming_cinema` (split gameplay + auto facecam).
   - Use `subtitles`: `enabled: true, preset: "karaoke"`.
   - Use `audioFx`: `sfxAutoPreset: "auto_by_moment", autoDuck: true, vocalEnhance: true` + top-level `audioNorm: true` (`-14 LUFS`).
   - Keep `useWatermark: true` and `useOutro: true`. Watch Fair Queue WIP limit (max 3 active jobs/member -> `429`).
6. **Review Handoff & Publish Prep**:
   - Poll with `klipforge_get_job` (or `klipforge_list_jobs` which always enforces `summary=1`).
   - Completed jobs land in `review` for human QC approval. Do **not** try to self-approve.
   - Once approved, generate `klipforge_generate_publish_kit` (TikTok + YT Shorts <=100 chars + IG Reels) and optionally `klipforge_export_jianying_draft` (`.zip` CapCut desktop draft).

## 3. All 23 Least-Privilege MCP Tools

- **Read/Observe**: `klipforge_home_metrics`, `klipforge_cookie_status`, `klipforge_list_projects`, `klipforge_get_project`, `klipforge_search_transcript`, `klipforge_list_board`, `klipforge_list_jobs`, `klipforge_get_job`, `klipforge_activity`, `klipforge_team_metrics`, `klipforge_audio_and_moment_map`
- **Analyze/Pre-Edit**: `klipforge_analyze_video`, `klipforge_detect_scenes`, `klipforge_detect_facecam`, `klipforge_diarize_project`, `klipforge_verify_frames`, `klipforge_set_verdict`
- **Produce/Deliver**: `klipforge_move_board`, `klipforge_render_single`, `klipforge_render_edit_script`, `klipforge_post_edit`, `klipforge_generate_publish_kit`, `klipforge_export_jianying_draft`
