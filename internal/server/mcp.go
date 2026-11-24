package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

// mcpTools defines all 18 strictly least-privilege MCP tools exposed at POST /api/mcp.
// Zero destructive tools: NO DELETE, NO QC approve/reject, NO /api/team, NO /api/publish/accounts, NO cookie refresh.
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
		Description: "Record agent candidate evaluation (keep/drop/maybe, funny 0-5, note, momentType) without auto-rendering.",
		InputSchema: schemaObj(map[string]interface{}{
			"projectId":   propStr("Project ID"),
			"candidateId": propStr("Candidate ID"),
			"verdict":     propStr("keep | drop | maybe"),
			"funny":       propInt("Humor/entertainment score 0..5"),
			"note":        propStr("Short rationale for human team/review"),
			"momentType":  propStr("fail | meme | crash | victory | hype | twist"),
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
			"title":      propStr("Clip or compilation title"),
			"projectId":  propStr("Default project ID for segments"),
			"campaignId": propStr("Optional campaign ID for watermark/outro/forbiddenWords"),
			"layout":     map[string]interface{}{"type": "object", "description": "aspectRatio (9:16), cropMode (face_track/facecam_top_neon/...)"},
			"subtitles":  map[string]interface{}{"type": "object", "description": "enabled, preset (karaoke/viral-yellow/gaming-neon), ..."},
			"audioFx":    map[string]interface{}{"type": "object", "description": "sfxAutoPreset (auto_by_moment), autoDuck, vocalEnhance, bgmAssetId"},
			"segments":   map[string]interface{}{"type": "array", "description": "Array of segment objects with startSec, endSec, momentType, speedRamp, zoom, textOverlay, filter, transition"},
		}, "segments"),
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
			"jobId": propStr("RenderJob ID"),
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
}

// handleMCP exposes KlipForge's native Streamable HTTP MCP JSON-RPC 2.0 server at POST /api/mcp.
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	var req mcpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	switch req.Method {
	case "notifications/initialized":
		// Notification without ID expects 202 empty body; with ID ack cleanly
		if len(req.ID) == 0 || string(bytes.TrimSpace(req.ID)) == "null" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{}})

	case "ping":
		writeJSON(w, http.StatusOK, mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{}})

	case "initialize":
		writeJSON(w, http.StatusOK, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"protocolVersion": "2025-03-26",
				"capabilities": map[string]interface{}{
					"tools": map[string]interface{}{"listChanged": false},
				},
				"serverInfo": map[string]interface{}{
					"name":    "klipforge-mcp",
					"version": "1.0.0",
				},
				"instructions": "KlipForge least-privilege MCP orchestrator. Audio-first by default; all renders honor RBAC, Fair Queue, and campaign forbiddenWords.",
			},
		})

	case "tools/list":
		writeJSON(w, http.StatusOK, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"tools": mcpTools,
			},
		})

	case "tools/call":
		var params struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.Name) == "" {
			writeJSON(w, http.StatusOK, mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &mcpError{Code: -32602, Message: "Invalid params: tools/call membutuhkan 'name'"},
			})
			return
		}
		if params.Arguments == nil {
			params.Arguments = map[string]interface{}{}
		}
		result, rpcErr := s.dispatchMCPTool(r, params.Name, params.Arguments)
		if rpcErr != nil {
			writeJSON(w, http.StatusOK, mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   rpcErr,
			})
			return
		}
		writeJSON(w, http.StatusOK, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		})

	default:
		writeJSON(w, http.StatusOK, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)},
		})
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
		subReq := httptest.NewRequest(method, rawTarget, &buf).WithContext(outer.Context())
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

	case "klipforge_detect_scenes":
		pid := strArg("projectId")
		if pid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: projectId"}
		}
		code, body := invoke(http.MethodPost, "/api/projects/"+pid+"/detect-scenes", map[string]string{"id": pid}, args, s.handleDetectProjectScenes)
		return formatMCPResult(code, body), nil

	case "klipforge_detect_facecam":
		pid := strArg("projectId")
		if pid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: projectId"}
		}
		code, body := invoke(http.MethodPost, "/api/projects/"+pid+"/detect-facecam", map[string]string{"id": pid}, args, s.handleDetectProjectFacecam)
		return formatMCPResult(code, body), nil

	case "klipforge_diarize_project":
		pid := strArg("projectId")
		if pid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: projectId"}
		}
		code, body := invoke(http.MethodPost, "/api/projects/"+pid+"/diarize", map[string]string{"id": pid}, args, s.handleDiarizeProject)
		return formatMCPResult(code, body), nil

	case "klipforge_verify_frames":
		pid := strArg("projectId")
		if pid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: projectId"}
		}
		code, body := invoke(http.MethodPost, "/api/projects/"+pid+"/verify-frames", map[string]string{"id": pid}, args, s.handleVerifyCandidateFrames)
		return formatMCPResult(code, body), nil

	case "klipforge_set_verdict":
		pid := strArg("projectId")
		cid := strArg("candidateId")
		if pid == "" || cid == "" || strArg("verdict") == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required arguments: projectId, candidateId, verdict"}
		}
		code, body := invoke(http.MethodPost, "/api/projects/"+pid+"/candidates/"+cid+"/verdict",
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
		code, body := invoke(http.MethodGet, "/api/jobs/"+jid, map[string]string{"id": jid}, nil, s.handleGetJob)
		return formatMCPResult(code, body), nil

	case "klipforge_generate_publish_kit":
		jid := strArg("jobId")
		if jid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: jobId"}
		}
		code, body := invoke(http.MethodPost, "/api/jobs/"+jid+"/publish-kit", map[string]string{"id": jid}, args, s.handleGeneratePublishKit)
		return formatMCPResult(code, body), nil

	case "klipforge_export_jianying_draft":
		jid := strArg("jobId")
		if jid == "" {
			return nil, &mcpError{Code: -32602, Message: "Missing required argument: jobId"}
		}
		code, body := invoke(http.MethodPost, "/api/jobs/"+jid+"/draft", map[string]string{"id": jid}, args, s.handleExportJianyingDraft)
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

	default:
		return nil, &mcpError{Code: -32601, Message: fmt.Sprintf("Unknown tool: %s", name)}
	}
}
