# Third-Party Notices & License Compliance Register (KlipForge)

> Maintained per KlipForge Integration v1.2b §4(a)–(d).
> Policy: **Zero AGPL/GPL-copyleft code, zero noncommercial code, zero research-only model weights** in KlipForge production builds.

---

## 1. Third-Party Components Used or Bundled by KlipForge

| Component | Usage in KlipForge | Code License | Model/Asset License | Upstream |
|---|---|---|---|---|
| **Go Standard Library** | Core HTTP server, JSON store, concurrency, Innertube HTTP/2 client | BSD-3-Clause | N/A | https://go.dev |
| **SortableJS** (`web/vendor/sortable.min.js`) | Drag-and-drop Kanban board UI | MIT | N/A | https://github.com/SortableJS/Sortable |
| **Anton Font** (`Anton-Regular.ttf`, system package on Linux/Docker) | Bold condensed subtitle / hook typography | SIL Open Font License 1.1 (OFL-1.1) | OFL-1.1 | https://fonts.google.com/specimen/Anton |
| **OpenCV (`opencv-python-headless`)** (optional sidecar `.venv`) | Face detection & frame sampling (`face_track`, `face_track_smart`, `detect-facecam`) | Apache-2.0 | N/A | https://github.com/opencv/opencv-python |
| **YuNet ONNX** (`face_detection_yunet.onnx`, optional local model) | Lightweight CPU face detection | MIT | MIT (OpenCV Zoo) | https://github.com/opencv/opencv_zoo |
| **PySceneDetect** (optional CLI sidecar `.venv`) | Shot-boundary detection (`scene.go`) | BSD-3-Clause | N/A | https://github.com/Breakthrough/PySceneDetect |
| **FFmpeg / FFprobe** (external CLI binary, invoked via subprocess) | Video/audio transcoding, contact-sheet storyboard, RMS audio energy | LGPL/GPL (external binary only, never linked into Go binary) | N/A | https://ffmpeg.org |
| **yt-dlp** (external CLI binary, invoked via subprocess) | YouTube metadata, subtitle, audio/video download | Unlicense | N/A | https://github.com/yt-dlp/yt-dlp |
| **AutoClip** (techniques/empirical findings reference only; zero verbatim code copied) | One-pass `id\|time\|text` transcript windowing, contact-sheet vision scan, shot-aware crop/fit, 1.5s face/event checklist rule | MIT | N/A | https://github.com/zhouxiaoka/autoclip |

---

## 2. KlipForge v1.2b Selective Reuse License Register

### 🟩 Allowed (Permissive MIT / Apache-2.0 / BSD — subject to Dual-License Check when models exist)
- **zhouxiaoka/autoclip** (`MIT`) — Empirical findings & checklist rules adopted (P1); clean-room native Go implementation for transcript windowing, storyboard, and shot-aware reframe.
- **modelscope/FunClip** (`MIT`) — Design reference only (P2, locked behind `I-11`).
- **AI YouTube Shorts Generator** (`MIT`) — Candidate output format adopted (`hookLine` + 1-sentence reason, P1); viral score selector & 9:16 default REJECTED per `I-05`.
- **TransNetV2** (`MIT` code — weights license MUST be verified before any download) — P2 BLOCKED behind `I-13` + disk gate `>= 2 GB`.

### 🟨 Conditional / Design-Only / Dual-License (Code + Model Weights Both Required)
- **LAION vocal-burst-annotation-pipeline** (`Apache-2.0` code — annotation/checkpoint dataset license MUST be verified separately; pin commit + archive snapshot) — P2 candidate #1 (single vocal-cue slot, never run simultaneously with PANNs).
- **PANNs / FlexSED** (`MIT` code — checkpoint weights license MUST be verified separately) — P2 candidate #2 (single vocal-cue slot, mutually exclusive with LAION).
- **ShortsFlow** (`MIT` — small repo, pin commit + archive snapshot) — Human mute-test rubric reference (P1); automated hook-score selector REJECTED (`I-05`).
- **clip-virality-scorer** (`MIT` — small repo) — Design note only (`sigmoid(log2(views_24h/creator_median))` at `n >= 50`); heuristic weights REJECTED (`I-05`).
- **danmaku-highlight-pipeline** (`MIT`) — Timing rhythm design note only; zero accuracy claims.
- **IndoBERT (`IndoNLP/indonlu`)** (`Apache-2.0`) — Lowest-priority P2 conditional metadata only.

### 🟥 Strictly Prohibited (ZERO Code Permitted in KlipForge)
- **KnowClip** (`AGPL-3.0`) — Clean-room conceptual schema notes ONLY; zero lines copied, never vendored.
- **hotclip** (`AGPL-3.0`) — Clean-room architecture inspiration ONLY; zero lines copied, never vendored.
- **SupoClip** (`NONCOMMERCIAL`, Shiori LLC) — Permanent design-only; zero code permitted without written commercial license.
- **QD-DETR / Atlantic-Records / LaughterSegmentation** — Unclear license / research-only weights; zero tolerance in production.

---

## 3. Clean-Room & Dual-License Protocol
1. **Dual-License Rule:** Any future P2 model integration MUST document **both** code license and model-weight/dataset license in this file before download or merge. Missing either = automatic reject.
2. **Clean-Room Rule:** Reimplementations of conceptual patterns from 🟥 repos MUST be authored strictly from written design specifications, never with upstream source code open alongside implementation, and never copying comments or snippets.
3. **Archive & Pin Rule:** Before adopting any small permissive repo (`< 100` stars) in P2, pin the exact commit hash and archive a dated snapshot alongside its license file.
