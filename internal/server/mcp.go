package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"
)

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

func schemaObj(props map[string]interface{}, required ...string) map[string]interface{} {
	s := map[string]interface{}{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": true,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func propStr(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": desc}
}

func propInt(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "integer", "description": desc}
}

func propNum(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "number", "description": desc}
}

func propBool(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "boolean", "description": desc}
}

// mcpTools defines all 29 strictly least-privilege MCP tools exposed at /api/mcp.
// Zero destructive tools: NO DELETE, NO QC approve/reject, NO manual mark-posted/ledger writes, NO /api/team management, NO /api/publish/accounts, NO cookie refresh.
var mcpTools = []mcpToolDef{
	{
		Name:        "klipforge_home_metrics",
		Description: "Get live KlipForge home state: 4 KPI tiles with 7-day deltas, active renders, pending reviews, and engine readiness.",
		InputSchema: schemaObj(map[string]interface{}{}),
	},
	{
		Name:        "klipforge_cookie_status",
		Description: "Get non-secret server-side YouTube cookies.txt keep-alive health status (healthy/expired/missing, count, next check time).",
		InputSchema: schemaObj(map[string]interface{}{}),
	},
	{
		Name:        "klipforge_list_projects",
		Description: "List all ingested video/VOD projects with duration, campaign assignment, and candidate counts.",
		InputSchema: schemaObj(map[string]interface{}{}),
	},
	{
		Name:        "klipforge_get_project",
		Description: "Get full project detail, candidates (with arc/virality/verdict/facecam), timeline, and signals.",
		InputSchema: schemaObj(map[string]interface{}{
			"projectId":      propStr("Project ID, e.g. proj-45r3OyaxvFE-1795349482"),
			"includeDropped": propBool("Set true to include candidates marked verdict=drop"),
		}, "projectId"),
	},
	{
		Name:        "klipforge_analyze_video",
		Description: "Analyze a YouTube video/VOD using multi-signal hype detection. Keep audioFirst=true by default to save cloud disk.",
		InputSchema: schemaObj(map[string]interface{}{
			"url":        propStr("YouTube watch/live/shorts URL"),
			"campaignId": propStr("Optional target campaign ID"),
			"audioFirst": propBool("Defaults to true (audio+chat+heatmap+subs without downloading full MP4)"),
		}, "url"),
	},
	{
		Name:        "klipforge_scan_livechat",
		Description: "Fetch YouTube Live Chat Replay across long 3-5h VODs (parallel Innertube seeker -> yt-dlp fallback) and re-score candidates with trailing reaction lag, unique-chatter anti-spam, and Visual Gag Boost.",
		InputSchema: schemaObj(map[string]interface{}{
			"projectId": propStr("Project ID"),
			"sync":      propBool("Defaults to true (waits for chat replay & returns upgraded candidates immediately)"),
		}, "projectId"),
	},
	{
		Name:        "klipforge_transcript_windows",
		Description: "Return AutoClip-style one-pass compact transcript windows (`id|MM:SS|text` with inline [SPEAKER_X], [CHAT:N], [AUD:N], and synthetic [VISUAL_CHAT_SPIKE] lines) for whole-VOD LLM moment picking.",
		InputSchema: schemaObj(map[string]interface{}{
			"projectId":  propStr("Project ID"),
			"window":     propInt("0-based window index (default 0)"),
			"windowMin":  propNum("Window duration in minutes (default 25, max 60)"),
			"overlapSec": propNum("Overlap between adjacent windows in seconds (default 90)"),
			"maxChars":   propInt("Character budget per window (default 14000)"),
		}, "projectId"),
	},
	{
		Name:        "klipforge_sample_storyboard",
		Description: "Generate a labeled multi-frame contact-sheet JPEG (default 3x3) across a range or full VOD (works in Audio-First mode!) so vision models & humans can visually verify crashes, menus, and gags.",
		InputSchema: schemaObj(map[string]interface{}{
			"projectId":     propStr("Project ID"),
			"startSec":      propNum("Optional start second for range sampling"),
			"endSec":        propNum("Optional end second for range sampling"),
			"timestamps":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number"}, "description": "Optional explicit frame timestamps (max 16)"},
			"count":         propInt("Frame count when timestamps omitted (default 9, max 16)"),
			"cols":          propInt("Grid columns (default 3, max 4)"),
			"includeBase64": propBool("Set true to also attach a native MCP image/jpeg block for vision models"),
		}, "projectId"),
	},
	{
		Name:        "klipforge_detect_scenes",
		Description: "Detect shot boundaries (PySceneDetect or FFmpeg scene fallback) and snap project candidates cleanly onto real cuts.",
		InputSchema: schemaObj(map[string]interface{}{
			"projectId":  propStr("Project ID"),
			"maxSnapSec": propNum("Optional max boundary snap distance in seconds (default 3.0)"),
		}, "projectId"),
	},
	{
		Name:        "klipforge_detect_facecam",
		Description: "Auto-detect gaming streamer webcam coordinates (YuNet/OpenCV on video frames or Audio-First thumbnail) and save on Project.",
		InputSchema: schemaObj(map[string]interface{}{
			"projectId": propStr("Project ID"),
		}, "projectId"),
	},
	{
		Name:        "klipforge_diarize_project",
		Description: "Run speaker diarization (sherpa-onnx or offline fallback) to label TranscriptCue.Speaker (streamer vs NPC/guest).",
		InputSchema: schemaObj(map[string]interface{}{
			"projectId": propStr("Project ID"),
		}, "projectId"),
	},
	{
		Name:        "klipforge_verify_frames",
		Description: "Extract real Start and Peak frame JPEGs for candidate inspection before rendering.",
		InputSchema: schemaObj(map[string]interface{}{
			"projectId":    propStr("Project ID"),
			"candidateIds": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Optional list of candidate IDs"},
		}, "projectId"),
	},
	{
		Name:        "klipforge_set_verdict",
		Description: "Record agent candidate evaluation (keep/drop/maybe, funny 0-5, note, momentType, optional hookLine + 4 H1 checklist flags) without auto-rendering.",
		InputSchema: schemaObj(map[string]interface{}{
			"projectId":        propStr("Project ID"),
			"candidateId":      propStr("Candidate ID"),
			"verdict":          propStr("keep | drop | maybe"),
			"funny":            propInt("Humor/entertainment score 0..5"),
			"note":             propStr("Short rationale for human team/review"),
			"momentType":       propStr("fail | meme | crash | victory | hype | twist"),
			"hookLine":         propStr("Optional standalone 1-line hook (must pass campaign forbiddenWords)"),
			"h1TawaFacecam":    propBool("Optional H1 F1 flag: streamer laugh/reaction visible on facecam"),
			"h1VisualKejadian": propBool("Optional H1 F2 flag: gameplay incident/physics clearly visible in frame"),
			"h1ArcUtuh":        propBool("Optional H1 F3 flag: complete 20-55s arc"),
			"h1HookMutePass":   propBool("Optional H1 F4 flag: 1-3s hook passes mute test (AutoClip 1.5s rule)"),
		}, "projectId", "candidateId", "verdict"),
	},
	{
		Name:        "klipforge_list_board",
		Description: "Fetch the 6-column Kanban board (kandidat -> verifikasi_frame -> render -> review -> siap_posting -> diposting) and member WIP meters.",
		InputSchema: schemaObj(map[string]interface{}{
			"member":     propStr("Optional team member ID/name filter"),
			"projectId":  propStr("Optional projectId filter"),
			"momentType": propStr("Optional momentType filter"),
		}),
	},
	{
		Name:        "klipforge_move_board",
		Description: "Move a candidate or job card across allowed Kanban columns (agent cannot move directly to siap_posting).",
		InputSchema: schemaObj(map[string]interface{}{
			"itemType": propStr("candidate or job"),
			"itemId":   propStr("Candidate ID or Job ID"),
			"projectId": propStr("Required when moving a candidate"),
			"toColumn": propStr("kandidat | verifikasi_frame | render | review | siap_posting | diposting"),
			"reason":   propStr("Optional move/note reason"),
		}, "itemId", "toColumn"),
	},
	{
		Name:        "klipforge_render_edit_script",
		Description: "Render a single clip or multi-segment CapCut-Pro timeline (speedRamp, zoom, textOverlay, auto_by_moment, vocalEnhance, sceneSnap, silenceTrim, karaoke).",
		InputSchema: schemaObj(map[string]interface{}{
			"title":        propStr("Clip or compilation title"),
			"projectId":    propStr("Default project ID for segments"),
			"campaignId":   propStr("Optional campaign ID for watermark/outro/forbiddenWords"),
			"layout":       map[string]interface{}{"type": "object", "description": "aspectRatio (9:16), cropMode (face_track_smart/face_track/facecam_auto/facecam_top_neon/gaming_cinema/blur_bg)"},
			"subtitles":    map[string]interface{}{"type": "object", "description": "enabled, preset (karaoke/viral-yellow/gaming-neon), ..."},
			"audioFx":      map[string]interface{}{"type": "object", "description": "sfxAutoPreset (auto_by_moment), autoDuck, vocalEnhance, bgmAssetId"},
			"videoFx":      map[string]interface{}{"type": "object", "description": "colorPreset, motionPreset, speed, sharpen, fadeIn"},
			"audioNorm":    propBool("Enable -14 LUFS loudnorm"),
			"useWatermark": propBool("Attach campaign watermark"),
			"useOutro":     propBool("Attach clean campaign 3s outro"),
			"sceneSnap":    propBool("Snap segment cuts to detected shot boundaries"),
			"silenceTrim":  propBool("Trim leading/trailing silence while protecting peak"),
			"pickCover":    propBool("Pick sharpest face-present cover frame"),
			"segments":     map[string]interface{}{"type": "array", "description": "Array of segment objects with startSec, endSec, cropMode, momentType, speedRamp, zoom, textOverlay, filter, transition"},
		}, "segments"),
	},
	{
		Name:        "klipforge_render_single",
		Description: "Render a single vertical clip directly via POST /api/jobs/render (supports facecam_auto, vocalEnhance, sceneSnap, silenceTrim, karaoke).",
		InputSchema: schemaObj(map[string]interface{}{
			"projectId":    propStr("Project ID"),
			"campaignId":   propStr("Optional campaign ID"),
			"candidateId":  propStr("Optional candidate ID"),
			"title":        propStr("Clip title"),
			"startSec":     propNum("Clip start timestamp in seconds"),
			"endSec":       propNum("Clip end timestamp in seconds"),
			"hookText":     propStr("Optional animated hook banner text"),
			"layout":       map[string]interface{}{"type": "object", "description": "aspectRatio, resolution, cropMode (face_track_smart/face_track/facecam_auto/facecam_top_neon/blur_bg)"},
			"subtitles":    map[string]interface{}{"type": "object", "description": "enabled, preset, position"},
			"audioFx":      map[string]interface{}{"type": "object", "description": "sfxAutoPreset, autoDuck, vocalEnhance, bgmAssetId"},
			"videoFx":      map[string]interface{}{"type": "object", "description": "colorPreset, motionPreset, speed, sharpen"},
			"audioNorm":    propBool("Enable loudnorm"),
			"useWatermark": propBool("Apply campaign watermark"),
			"useOutro":     propBool("Append clean 3s outro"),
			"sceneSnap":    propBool("Snap clip bounds to real scene cuts"),
			"silenceTrim":  propBool("Trim dead air at start/end"),
			"pickCover":    propBool("Pick sharpest cover frame"),
		}, "projectId", "startSec", "endSec"),
	},
	{
		Name:        "klipforge_post_edit",
		Description: "Fast local post-edit on an already-rendered clip (trim, speed, additional SFX/BGM, vocalEnhance, audioNorm) without re-downloading YouTube.",
		InputSchema: schemaObj(map[string]interface{}{
			"jobId":         propStr("Source RenderJob ID (status=done)"),
			"title":         propStr("Optional edited title"),
			"trimStartSec":  propNum("Trim start seconds"),
			"trimEndSec":    propNum("Trim end seconds"),
			"speed":         propNum("Playback speed 0.50..2.00"),
			"preserveOutro": propBool("Preserve clean campaign outro untouched"),
			"bgmAssetId":    propStr("Optional additional BGM asset ID"),
			"bgmVolume":     propNum("BGM volume 0.05..0.80"),
			"bgmAutoDuck":   propBool("Duck BGM under voice"),
			"audioNorm":     propBool("Apply -14 LUFS normalization"),
			"vocalEnhance":  propBool("Apply broadcast vocal mastering chain"),
			"sfxCues":       map[string]interface{}{"type": "array", "description": "Array of {assetId, atSec, volume}"},
		}, "jobId"),
	},
	{
		Name:        "klipforge_list_jobs",
		Description: "List render jobs cleanly using anti-truncate summary=1 pagination.",
		InputSchema: schemaObj(map[string]interface{}{
			"limit":  propInt("Max jobs to return (default 20)"),
			"status": propStr("Optional status filter: queued, rendering, done, error"),
			"member": propStr("Optional creator member filter"),
		}),
	},
	{
		Name:        "klipforge_get_job",
		Description: "Get full single RenderJob details, status, compliance checks, and output/cover/draft URLs.",
		InputSchema: schemaObj(map[string]interface{}{
			"jobId": propStr("RenderJob ID"),
		}, "jobId"),
	},
	{
		Name:        "klipforge_generate_publish_kit",
		Description: "Generate multi-platform publish copy (TikTok, YouTube Shorts <=100 chars, Instagram Reels) + 4-point SOP checklist.",
		InputSchema: schemaObj(map[string]interface{}{
			"jobId":             propStr("RenderJob ID"),
			"hookText":          propStr("Optional hook text override"),
			"tiktokCaption":     propStr("Optional TikTok caption override"),
			"shortsTitle":       propStr("Optional YouTube Shorts title override (<=100 chars)"),
			"shortsDescription": propStr("Optional YouTube Shorts description override"),
			"reelsCaption":      propStr("Optional Instagram Reels caption override"),
		}, "jobId"),
	},
	{
		Name:        "klipforge_export_jianying_draft",
		Description: "Export a version-pinned Jianying / CapCut desktop draft bundle (.zip, schema jianying-draft-5.9.0) for a RenderJob.",
		InputSchema: schemaObj(map[string]interface{}{
			"jobId": propStr("RenderJob ID"),
		}, "jobId"),
	},
	{
		Name:        "klipforge_audio_and_moment_map",
		Description: "Inspect the offline momentType->SFX/BGM edit map plus audio library assets (or trending sound radar by category).",
		InputSchema: schemaObj(map[string]interface{}{
			"trendingCategory": propStr("Optional trending category: id_viral | meme_sfx | phonk_gaming | windah_viral"),
		}),
	},
	{
		Name:        "klipforge_search_transcript",
		Description: "Search a project's timestamped transcript/subtitles by keyword or phrase (returns matching cue windows).",
		InputSchema: schemaObj(map[string]interface{}{
			"projectId": propStr("Project ID"),
			"query":     propStr("Search keyword or phrase"),
		}, "projectId", "query"),
	},
	{
		Name:        "klipforge_activity",
		Description: "List recent persisted studio activity feed entries (renders, reviews, verdicts, publish events).",
		InputSchema: schemaObj(map[string]interface{}{
			"limit":  propInt("Max activity rows (default 30)"),
			"member": propStr("Optional team member ID/name filter"),
		}),
	},
	{
		Name:        "klipforge_team_metrics",
		Description: "Read non-secret team presence, active WIP counts, turnaround, approval rates, and 7-day sparklines (zero keys exposed).",
		InputSchema: schemaObj(map[string]interface{}{}),
	},
	{
		Name:        "klipforge_inventory",
		Description: "Read-only IN-SAMPLE audit of all KlipForge jobs vs MP4 files on disk (publishable, posted, pending_review, rejected_recoverable, broken_missing_file, failed, orphan MP4s, and rejection reason histogram).",
		InputSchema: schemaObj(map[string]interface{}{}),
	},
	{
		Name:        "klipforge_publishable_queue",
		Description: "List FIFO queue of approved clips with existing MP4 files ready for manual posting (postedPlatforms, remainingPlatforms, downloadUrl, caption, recorded views). Posting is strictly manual (zero auto-post).",
		InputSchema: schemaObj(map[string]interface{}{}),
	},
	{
		Name:        "klipforge_views_ledger",
		Description: "Read manually logged post performance entries and summary (totalViews, medianViews, IN-SAMPLE vs HELD-OUT breakdowns). Strictly read-only for agents.",
		InputSchema: schemaObj(map[string]interface{}{
			"jobId":    propStr("Optional RenderJob ID filter"),
			"platform": propStr("Optional platform filter: tiktok | youtube | instagram"),
			"split":    propStr("Optional sample split filter: IN-SAMPLE | HELD-OUT"),
		}),
	},
}

// handleMCP exposes KlipForge's native Streamable HTTP MCP JSON-RPC 2.0 server at /api/mcp (POST/GET/DELETE).
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodGet:
		if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") {
			flusher, ok := w.(http.Flusher)
			if !ok {
				writeErr(w, http.StatusInternalServerError, "SSE streaming tidak didukung")
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			rc := http.NewResponseController(w)
			_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			_, _ = fmt.Fprint(w, ": mcp-stream-open\n\n")
			flusher.Flush()
			hb := time.NewTicker(20 * time.Second)
			defer hb.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-hb.C:
					_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
					if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
						return
					}
					flusher.Flush()
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"name":            "klipforge-mcp",
			"version":         "1.1.0",
			"protocolVersion": "2025-03-26",
			"transport":       "streamable-http",
			"toolsCount":      len(mcpTools),
		})
		return
	}

	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, mcpResponse{
			JSONRPC: "2.0",
			Error:   &mcpError{Code: -32700, Message: "Parse error: gagal membaca body"},
		})
		return
	}
	trimmed := bytes.TrimSpace(rawBody)
	if len(trimmed) == 0 {
		writeJSON(w, http.StatusBadRequest, mcpResponse{
			JSONRPC: "2.0",
			Error:   &mcpError{Code: -32700, Message: "Parse error: body kosong"},
		})
		return
	}

	// JSON-RPC 2.0 batch array support (`[...]`)
	if trimmed[0] == '[' {
		var batch []mcpRequest
		if err := json.Unmarshal(trimmed, &batch); err != nil || len(batch) == 0 {
			writeJSON(w, http.StatusBadRequest, mcpResponse{
				JSONRPC: "2.0",
				Error:   &mcpError{Code: -32700, Message: "Parse error: batch JSON-RPC tidak valid"},
			})
			return
		}
		if len(batch) > 25 {
			writeJSON(w, http.StatusBadRequest, mcpResponse{
				JSONRPC: "2.0",
				Error:   &mcpError{Code: -32600, Message: "Invalid Request: maksimal 25 request per batch"},
			})
			return
		}
		responses := make([]mcpResponse, 0, len(batch))
		for _, item := range batch {
			if resp, emit := s.handleSingleMCPRequest(r, item); emit {
				responses = append(responses, resp)
			}
		}
		if len(responses) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, responses)
		return
	}

	var req mcpRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, mcpResponse{
			JSONRPC: "2.0",
			Error:   &mcpError{Code: -32700, Message: "Parse error: JSON-RPC tidak valid"},
		})
		return
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		writeJSON(w, http.StatusBadRequest, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32600, Message: "Invalid Request: jsonrpc wajib '2.0'"},
		})
		return
	}
	resp, emit := s.handleSingleMCPRequest(r, req)
	if !emit {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSingleMCPRequest(r *http.Request, req mcpRequest) (mcpResponse, bool) {
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32600, Message: "Invalid Request: jsonrpc wajib '2.0'"},
		}, true
	}

	isNotif := len(req.ID) == 0 || string(bytes.TrimSpace(req.ID)) == "null"
	if strings.HasPrefix(req.Method, "notifications/") {
		if isNotif {
			return mcpResponse{}, false
		}
		return mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{}}, true
	}

	switch req.Method {
	case "ping":
		return mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{}}, true

	case "initialize":
		return mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"protocolVersion": "2025-03-26",
				"capabilities": map[string]interface{}{
					"tools": map[string]interface{}{"listChanged": false},
				},
				"serverInfo": map[string]interface{}{
					"name":    "klipforge-mcp",
					"version": "1.1.0",
				},
				"instructions": "KlipForge least-privilege MCP orchestrator (29 tools). Audio-first by default; enforces I-05 Score-Ban Guardrail (verdict='keep' + frame/storyboard proof before candidate render), read-only P0 Ops observability (inventory, manual publishable queue, views ledger), Live Chat Replay Pro, AutoClip compact transcript windows, Vision Storyboards, and shot-aware face_track_smart while honoring RBAC, Fair Queue, and campaign forbiddenWords.",
			},
		}, true

	case "tools/list":
		return mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"tools": mcpTools,
			},
		}, true

	case "tools/call":
		var params struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.Name) == "" {
			return mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &mcpError{Code: -32602, Message: "Invalid params: tools/call membutuhkan 'name'"},
			}, true
		}
		if params.Arguments == nil {
			params.Arguments = map[string]interface{}{}
		}
		result, rpcErr := s.dispatchMCPTool(r, params.Name, params.Arguments)
		if rpcErr != nil {
			return mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   rpcErr,
			}, true
		}
		return mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}, true

	default:
		return mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)},
		}, true
	}
}

func (s *Server) dispatchMCPTool(outer *http.Request, name string, args map[string]interface{}) (map[string]interface{}, *mcpError) {
	strArg := func(k string) string {
		if v, ok := args[k]; ok && v != nil {
			return strings.TrimSpace(fmt.Sprint(v))
		}
		return ""
	}
	boolArg := func(k string) (bool, bool) {
		v, ok := args[k]
		if !ok || v == nil {
			return false, false
		}
		b, ok := v.(bool)
		return b, ok
	}

	invoke := func(method, rawTarget string, pathVals map[string]string, body interface{}, fn http.HandlerFunc) (int, []byte) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		subReq, err := http.NewRequestWithContext(outer.Context(), method, rawTarget, &buf)
		if err != nil {
			return http.StatusBadRequest, []byte(`{"error":"parameter request tidak valid"}`)
		}
		if body != nil {
			subReq.Header.Set("Content-Type", "application/json")
		}
		for k, v := range pathVals {
			subReq.SetPathValue(k, v)
		}
		rec := httptest.NewRecorder()
		fn(rec, subReq)
		return rec.Code, rec.Body.Bytes()
	}

	formatMCPResult := func(code int, body []byte) map[string]interface{} {
		txt := strings.TrimSpace(string(body))
		return map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": txt},
			},
			"isError": code >= 400,
			"_meta": map[string]interface{}{
				"httpStatus": code,
			},
		}
	}

	switch name {
	case "klipforge_home_metrics":
		code, body := invoke(http.MethodGet, "/api/metrics/home", nil, nil, s.handleHomeMetrics)
		return formatMCPResult(code, body), nil

	case "klipforge_cookie_status":
		code, body := invoke(http.MethodGet, "/api/settings/cookies/status", nil, nil, s.handleCookieHealthStatus)
		return formatMCPResult(code, body), nil

	case "klipforge_list_projects":
		code, body := invoke(http.MethodGet, "/api/projects", nil, nil, s.handleListProjects)
		return formatMCPResult(code, body), nil

	case "klipforge_get_project":
		pid := strArg("projectId")
		if pid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: projectId"}
		}
		target := "/api/projects/" + url.PathEscape(pid)
		if inc, ok := boolArg("includeDropped"); ok && inc {
			target += "?includeDropped=1"
		}
		code, body := invoke(http.MethodGet, target, map[string]string{"id": pid}, nil, s.handleGetProject)
		return formatMCPResult(code, body), nil

	case "klipforge_analyze_video":
		u := strArg("url")
		if u == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: url"}
		}
		payload := map[string]interface{}{}
		for k, v := range args {
			payload[k] = v
		}
		if _, set := args["audioFirst"]; !set {
			payload["audioFirst"] = true
		}
		code, body := invoke(http.MethodPost, "/api/projects/analyze", nil, payload, s.handleAnalyzeProject)
		return formatMCPResult(code, body), nil

	case "klipforge_scan_livechat":
		pid := strArg("projectId")
		if pid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: projectId"}
		}
		payload := map[string]interface{}{}
		for k, v := range args {
			payload[k] = v
		}
		if _, set := args["sync"]; !set {
			payload["sync"] = true
		}
		code, body := invoke(http.MethodPost, "/api/projects/"+url.PathEscape(pid)+"/scan-livechat", map[string]string{"id": pid}, payload, s.handleScanLiveChat)
		return formatMCPResult(code, body), nil

	case "klipforge_transcript_windows":
		pid := strArg("projectId")
		if pid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: projectId"}
		}
		qv := url.Values{}
		for _, key := range []string{"window", "windowMin", "overlapSec", "maxChars"} {
			if val := strArg(key); val != "" {
				qv.Set(key, val)
			}
		}
		target := "/api/projects/" + url.PathEscape(pid) + "/transcript-windows"
		if enc := qv.Encode(); enc != "" {
			target += "?" + enc
		}
		code, body := invoke(http.MethodGet, target, map[string]string{"id": pid}, nil, s.handleGetTranscriptWindows)
		return formatMCPResult(code, body), nil

	case "klipforge_sample_storyboard":
		pid := strArg("projectId")
		if pid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: projectId"}
		}
		code, body := invoke(http.MethodPost, "/api/projects/"+url.PathEscape(pid)+"/storyboard", map[string]string{"id": pid}, args, s.handleSampleStoryboard)
		if code < 400 {
			if incB64, ok := boolArg("includeBase64"); ok && incB64 {
				var parsed map[string]interface{}
				if json.Unmarshal(body, &parsed) == nil {
					if b64, has := parsed["base64"].(string); has && b64 != "" {
						delete(parsed, "base64")
						metaBytes, _ := json.Marshal(parsed)
						return map[string]interface{}{
							"content": []map[string]interface{}{
								{"type": "text", "text": string(metaBytes)},
								{"type": "image", "data": b64, "mimeType": "image/jpeg"},
							},
							"isError": false,
							"_meta": map[string]interface{}{
								"httpStatus": code,
							},
						}, nil
					}
				}
			}
		}
		return formatMCPResult(code, body), nil

	case "klipforge_detect_scenes":
		pid := strArg("projectId")
		if pid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: projectId"}
		}
		code, body := invoke(http.MethodPost, "/api/projects/"+url.PathEscape(pid)+"/detect-scenes", map[string]string{"id": pid}, args, s.handleDetectProjectScenes)
		return formatMCPResult(code, body), nil

	case "klipforge_detect_facecam":
		pid := strArg("projectId")
		if pid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: projectId"}
		}
		code, body := invoke(http.MethodPost, "/api/projects/"+url.PathEscape(pid)+"/detect-facecam", map[string]string{"id": pid}, args, s.handleDetectProjectFacecam)
		return formatMCPResult(code, body), nil

	case "klipforge_diarize_project":
		pid := strArg("projectId")
		if pid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: projectId"}
		}
		code, body := invoke(http.MethodPost, "/api/projects/"+url.PathEscape(pid)+"/diarize", map[string]string{"id": pid}, args, s.handleDiarizeProject)
		return formatMCPResult(code, body), nil

	case "klipforge_verify_frames":
		pid := strArg("projectId")
		if pid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: projectId"}
		}
		code, body := invoke(http.MethodPost, "/api/projects/"+url.PathEscape(pid)+"/verify-frames", map[string]string{"id": pid}, args, s.handleVerifyCandidateFrames)
		return formatMCPResult(code, body), nil

	case "klipforge_set_verdict":
		pid := strArg("projectId")
		cid := strArg("candidateId")
		if pid == "" || cid == "" || strArg("verdict") == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required arguments: projectId, candidateId, verdict"}
		}
		code, body := invoke(http.MethodPost, "/api/projects/"+url.PathEscape(pid)+"/candidates/"+url.PathEscape(cid)+"/verdict",
			map[string]string{"id": pid, "candId": cid}, args, s.handleCandidateVerdict)
		return formatMCPResult(code, body), nil

	case "klipforge_list_board":
		q := url.Values{}
		if m := strArg("member"); m != "" {
			q.Set("member", m)
		}
		if pid := strArg("projectId"); pid != "" {
			q.Set("projectId", pid)
		}
		if mt := strArg("momentType"); mt != "" {
			q.Set("momentType", mt)
		}
		target := "/api/board"
		if enc := q.Encode(); enc != "" {
			target += "?" + enc
		}
		code, body := invoke(http.MethodGet, target, nil, nil, s.handleGetBoard)
		return formatMCPResult(code, body), nil

	case "klipforge_move_board":
		toCol := strArg("toColumn")
		itemID := strArg("itemId")
		itemType := strings.ToLower(strArg("itemType"))
		jobID := strArg("jobId")
		candID := strArg("candidateId")
		if itemID != "" {
			if itemType == "candidate" && candID == "" {
				candID = itemID
			} else if jobID == "" && candID == "" {
				if strings.HasPrefix(itemID, "clip-") {
					candID = itemID
				} else {
					jobID = itemID
				}
			}
		}
		if toCol == "" || (jobID == "" && candID == "") {
			return nil, &mcpError{Code: -32602, Message: "Missing required arguments: itemId (or jobId/candidateId) and toColumn"}
		}
		payload := map[string]interface{}{
			"jobId":       jobID,
			"candidateId": candID,
			"projectId":   strArg("projectId"),
			"toColumn":    toCol,
			"reason":      strArg("reason"),
		}
		code, body := invoke(http.MethodPost, "/api/board/move", nil, payload, s.handleBoardMove)
		return formatMCPResult(code, body), nil

	case "klipforge_render_edit_script":
		code, body := invoke(http.MethodPost, "/api/jobs/edit-script", nil, args, s.handleEditScript)
		return formatMCPResult(code, body), nil

	case "klipforge_render_single":
		code, body := invoke(http.MethodPost, "/api/jobs/render", nil, args, s.handleRenderSingle)
		return formatMCPResult(code, body), nil

	case "klipforge_post_edit":
		jid := strArg("jobId")
		if jid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: jobId"}
		}
		code, body := invoke(http.MethodPost, "/api/jobs/"+url.PathEscape(jid)+"/post-edit", map[string]string{"id": jid}, args, s.handlePostEditJob)
		return formatMCPResult(code, body), nil

	case "klipforge_list_jobs":
		q := url.Values{}
		q.Set("summary", "1")
		if lim := strArg("limit"); lim != "" {
			q.Set("limit", lim)
		} else {
			q.Set("limit", "20")
		}
		if st := strArg("status"); st != "" {
			q.Set("status", st)
		}
		if m := strArg("member"); m != "" {
			q.Set("member", m)
		}
		code, body := invoke(http.MethodGet, "/api/jobs?"+q.Encode(), nil, nil, s.handleListJobs)
		return formatMCPResult(code, body), nil

	case "klipforge_get_job":
		jid := strArg("jobId")
		if jid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: jobId"}
		}
		code, body := invoke(http.MethodGet, "/api/jobs/"+url.PathEscape(jid), map[string]string{"id": jid}, nil, s.handleGetJob)
		return formatMCPResult(code, body), nil

	case "klipforge_generate_publish_kit":
		jid := strArg("jobId")
		if jid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: jobId"}
		}
		code, body := invoke(http.MethodPost, "/api/jobs/"+url.PathEscape(jid)+"/publish-kit", map[string]string{"id": jid}, args, s.handleGeneratePublishKit)
		return formatMCPResult(code, body), nil

	case "klipforge_export_jianying_draft":
		jid := strArg("jobId")
		if jid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: jobId"}
		}
		code, body := invoke(http.MethodPost, "/api/jobs/"+url.PathEscape(jid)+"/draft", map[string]string{"id": jid}, args, s.handleExportJianyingDraft)
		return formatMCPResult(code, body), nil

	case "klipforge_audio_and_moment_map":
		c1, b1 := invoke(http.MethodGet, "/api/audio/moment-map", nil, nil, s.handleGetMomentEditMap)
		c2, b2 := invoke(http.MethodGet, "/api/audio", nil, nil, s.handleListAudio)
		combined := map[string]interface{}{
			"momentMap": json.RawMessage(b1),
			"audio":     json.RawMessage(b2),
		}
		worstCode := c1
		if c2 > worstCode {
			worstCode = c2
		}
		if cat := strArg("trendingCategory"); cat != "" {
			c3, b3 := invoke(http.MethodGet, "/api/audio/trending?category="+url.QueryEscape(cat), nil, nil, s.handleListTrendingAudio)
			combined["trending"] = json.RawMessage(b3)
			if c3 > worstCode {
				worstCode = c3
			}
		}
		out, _ := json.Marshal(combined)
		return formatMCPResult(worstCode, out), nil

	case "klipforge_search_transcript":
		pid := strArg("projectId")
		q := strArg("query")
		if pid == "" || q == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required arguments: projectId, query"}
		}
		code, body := invoke(http.MethodGet, "/api/projects/"+url.PathEscape(pid)+"/search?q="+url.QueryEscape(q), map[string]string{"id": pid}, nil, s.handleSearchTranscript)
		return formatMCPResult(code, body), nil

	case "klipforge_activity":
		qv := url.Values{}
		if lim := strArg("limit"); lim != "" {
			qv.Set("limit", lim)
		} else {
			qv.Set("limit", "30")
		}
		if m := strArg("member"); m != "" {
			qv.Set("member", m)
		}
		code, body := invoke(http.MethodGet, "/api/activity?"+qv.Encode(), nil, nil, s.handleListActivity)
		return formatMCPResult(code, body), nil

	case "klipforge_team_metrics":
		code, body := invoke(http.MethodGet, "/api/team/metrics", nil, nil, s.handleTeamMetrics)
		return formatMCPResult(code, body), nil

	case "klipforge_inventory":
		code, body := invoke(http.MethodGet, "/api/inventory", nil, nil, s.handleGetInventory)
		return formatMCPResult(code, body), nil

	case "klipforge_publishable_queue":
		code, body := invoke(http.MethodGet, "/api/publishable", nil, nil, s.handleListPublishable)
		return formatMCPResult(code, body), nil

	case "klipforge_views_ledger":
		qv := url.Values{}
		if jid := strArg("jobId"); jid != "" {
			qv.Set("jobId", jid)
		}
		if plat := strArg("platform"); plat != "" {
			qv.Set("platform", plat)
		}
		if sp := strArg("split"); sp != "" {
			qv.Set("split", sp)
		}
		target := "/api/ledger/views"
		if enc := qv.Encode(); enc != "" {
			target += "?" + enc
		}
		code, body := invoke(http.MethodGet, target, nil, nil, s.handleListViewsLedger)
		return formatMCPResult(code, body), nil

	default:
		return nil, &mcpError{Code: -32601, Message: fmt.Sprintf("Unknown tool: %s", name)}
	}
}
