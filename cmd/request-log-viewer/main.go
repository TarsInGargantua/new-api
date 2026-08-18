package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

type apiResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

type pageData struct {
	Items    interface{} `json:"items"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

func main() {
	addr := flag.String("addr", firstNonEmpty(os.Getenv("REQUEST_LOG_VIEWER_ADDR"), ":3001"), "listen address")
	dsn := flag.String("dsn", firstNonEmpty(os.Getenv("REQUEST_LOG_VIEWER_SQL_DSN"), os.Getenv("REQUEST_LOG_SQL_DSN")), "request log database DSN")
	exportDir := flag.String("export-dir", firstNonEmpty(os.Getenv("REQUEST_LOG_VIEWER_EXPORT_DIR"), "exports"), "persistent JSONL export directory")
	exportWorkerEnabled := flag.Bool("export-worker-enabled", !strings.EqualFold(strings.TrimSpace(os.Getenv("REQUEST_LOG_VIEWER_EXPORT_WORKER_ENABLED")), "false"), "process queued export batches")
	flag.Parse()

	if strings.TrimSpace(*dsn) == "" {
		fmt.Fprintln(os.Stderr, "REQUEST_LOG_SQL_DSN or REQUEST_LOG_VIEWER_SQL_DSN is required")
		os.Exit(1)
	}
	if os.Getenv("REQUEST_LOG_SQL_DSN") == "" {
		_ = os.Setenv("REQUEST_LOG_SQL_DSN", *dsn)
	}
	_ = os.Setenv("REQUEST_LOG_DB_READ_ONLY", "true")
	if err := model.InitRequestLogDB(); err != nil {
		fmt.Fprintf(os.Stderr, "init request log db: %v\n", err)
		os.Exit(1)
	}
	exportWorker, err := newRequestLogExportWorkerWithAutoRun(model.REQUEST_LOG_DB, *exportDir, *exportWorkerEnabled)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init export worker: %v\n", err)
		os.Exit(1)
	}
	if *exportWorkerEnabled {
		if err := exportWorker.Recover(); err != nil {
			fmt.Fprintf(os.Stderr, "recover export batches: %v\n", err)
			os.Exit(1)
		}
	}
	server := &requestLogViewerServer{db: model.REQUEST_LOG_DB, exports: exportWorker}

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveIndex)
	mux.HandleFunc("/api/status", serveStatus)
	mux.HandleFunc("/api/filter-options", serveFilterOptions)
	mux.HandleFunc("/api/logs", serveLegacyRequestLogs)
	mux.HandleFunc("/api/logs/", serveLegacyRequestLogs)
	mux.HandleFunc("/api/sessions", server.serveSessions)
	mux.HandleFunc("/api/sessions/", server.serveSessionDetail)
	mux.HandleFunc("/api/export-preview", server.serveExportPreview)
	mux.HandleFunc("/api/export-batches", server.serveExportBatches)
	mux.HandleFunc("/api/export-batches/", server.serveExportBatchAction)
	mux.HandleFunc("/api/export.jsonl", serveJSONL)

	fmt.Printf("request-log-viewer listening on %s\n", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

func serveStatus(w http.ResponseWriter, r *http.Request) {
	type viewerStatus struct {
		HasSessionData      bool   `json:"has_session_data"`
		SessionCount        int64  `json:"session_count"`
		RequestLogDBDialect string `json:"request_log_db_dialect,omitempty"`
	}
	result := viewerStatus{}
	if model.REQUEST_LOG_DB == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "request log database is not initialized")
		return
	}
	if model.REQUEST_LOG_DB.Dialector != nil {
		result.RequestLogDBDialect = model.REQUEST_LOG_DB.Dialector.Name()
	}
	result.HasSessionData = model.REQUEST_LOG_DB.Migrator().HasTable(&model.APIRequestLogTurn{})
	var err error
	if result.HasSessionData {
		_, result.SessionCount, err = model.GetAPIRequestLogSessions(model.REQUEST_LOG_DB, model.APIRequestLogTurnQueryParams{Num: 1})
	}
	writeAPI(w, result, err)
}

func serveFilterOptions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/filter-options" {
		http.NotFound(w, r)
		return
	}
	options, err := model.GetAPIRequestLogTurnFilterOptions(model.REQUEST_LOG_DB, queryInt(r.URL.Query().Get("limit"), 500))
	writeAPI(w, options, err)
}

func serveLegacyRequestLogs(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, http.StatusGone, "request-level views are disabled; use /api/sessions")
}

func serveJSONL(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, http.StatusGone, "request-level export is disabled; create a session export batch")
}

func queryList(values map[string][]string, key string) []string {
	rawValues := values[key]
	if len(rawValues) == 0 {
		return nil
	}
	out := make([]string, 0, len(rawValues))
	seen := map[string]bool{}
	for _, raw := range rawValues {
		for _, part := range strings.Split(raw, ",") {
			value := strings.TrimSpace(part)
			if value == "" || seen[value] {
				continue
			}
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func writeAPI(w http.ResponseWriter, data interface{}, err error) {
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: data})
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, apiResponse{Success: false, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	body, err := common.Marshal(value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func queryInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func queryInt64(value string, fallback int64) int64 {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

const indexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Session Log Viewer</title>
  <style>
    :root {
      color-scheme: dark;
      --bg:#0f1115;
      --panel:#161a20;
      --panel-2:#1b2028;
      --panel-3:#222833;
      --line:#343c4a;
      --line-soft:#272e38;
      --text:#e8edf5;
      --muted:#98a2b2;
      --faint:#697586;
      --accent:#4f8cff;
      --accent-dim:#316ac7;
      --good:#8ea36f;
      --warn:#b08cff;
      --bad:#c46a5a;
      --role-user:#7fc8f1;
      --role-user-bg:rgba(77,157,203,.12);
      --role-system:#c8a7e8;
      --role-system-bg:rgba(161,111,204,.13);
      --role-assistant:#86a8ff;
      --role-assistant-bg:rgba(79,140,255,.13);
      --role-developer:#e69a9a;
      --role-developer-bg:rgba(196,106,90,.13);
      --role-tool:#7fc6a4;
      --role-tool-bg:rgba(92,164,126,.13);
      --code:#0b0e13;
      --shadow:0 18px 45px rgba(0,0,0,.28);
    }
    * { box-sizing:border-box; }
    html, body { height:100%; overflow:hidden; }
    body {
      margin:0;
      background:var(--bg);
      color:var(--text);
      display:grid;
      grid-template-rows:auto minmax(0, 1fr);
      font:14px/1.45 "IBM Plex Sans", "Avenir Next", "Helvetica Neue", sans-serif;
      letter-spacing:0;
    }
    body:before {
      content:"";
      position:fixed;
      inset:0;
      pointer-events:none;
      opacity:.12;
      background-image:radial-gradient(var(--line) 1px, transparent 1px);
      background-size:18px 18px;
    }
    header {
      position:relative;
      z-index:10;
      display:grid;
      grid-template-columns:auto minmax(260px, 1fr) auto auto;
      align-items:center;
      gap:14px;
      padding:14px 18px;
      border-bottom:1px solid var(--line);
      background:rgba(16,17,15,.96);
      backdrop-filter:blur(12px);
    }
    h1 { margin:0; font-size:15px; font-weight:700; letter-spacing:.02em; }
    .brand { display:flex; align-items:center; gap:10px; min-width:0; }
    .mark { width:10px; height:22px; border-left:2px solid var(--accent); border-right:1px solid var(--line); }
    .status { color:var(--muted); font-size:12px; white-space:nowrap; }
    .actions { display:flex; gap:8px; }
    [hidden] { display:none !important; }
    main {
      position:relative;
      display:grid;
      grid-template-columns:minmax(560px, 52%) 1fr;
      min-height:0;
      overflow:hidden;
    }
    .turn-list, section { min-height:0; overflow:hidden; }
    .turn-list {
      display:grid;
      grid-template-rows:auto minmax(0, 1fr) auto;
      border-right:1px solid var(--line);
      background:rgba(18,19,16,.72);
    }
    .filters {
      z-index:2;
      display:grid;
      grid-template-columns:repeat(2, minmax(0,1fr));
      gap:8px;
      padding:12px;
      border-bottom:1px solid var(--line);
      background:rgba(18,19,16,.96);
      backdrop-filter:blur(10px);
    }
    .filter-field { position:relative; min-width:0; }
    .time-range {
      grid-column:1 / -1;
      display:grid;
      grid-template-columns:minmax(0,1fr) minmax(0,1fr) auto;
      gap:8px;
      align-items:end;
    }
    .time-field {
      display:flex;
      flex-direction:column;
      gap:5px;
      min-width:0;
    }
    .time-field span {
      color:var(--faint);
      font-size:11px;
      font-weight:700;
      line-height:1;
      text-transform:uppercase;
      letter-spacing:.07em;
    }
    .time-field input {
      width:100%;
      min-height:39px;
      color-scheme:dark;
    }
    .clear-time {
      min-height:39px;
      white-space:nowrap;
    }
    .select-button {
      width:100%;
      display:flex;
      align-items:center;
      justify-content:space-between;
      gap:8px;
      min-height:39px;
      color:var(--muted);
      text-align:left;
    }
    .select-button strong {
      min-width:0;
      overflow:hidden;
      text-overflow:ellipsis;
      white-space:nowrap;
      color:var(--text);
      font-weight:600;
    }
    .select-button span { color:var(--faint); font-size:12px; }
    .select-menu {
      position:absolute;
      left:0;
      right:0;
      top:calc(100% + 6px);
      z-index:20;
      display:none;
      max-height:300px;
      overflow:auto;
      border:1px solid var(--line);
      border-radius:8px;
      background:#12161c;
      box-shadow:var(--shadow);
      padding:6px;
    }
    .filter-field.open .select-menu { display:block; }
    .select-actions {
      display:flex;
      justify-content:space-between;
      gap:8px;
      padding:4px 4px 8px;
      border-bottom:1px solid var(--line-soft);
      margin-bottom:4px;
    }
    .select-actions button {
      min-height:0;
      padding:4px 7px;
      font-size:12px;
      color:var(--muted);
    }
    .option {
      display:flex;
      align-items:center;
      gap:8px;
      padding:7px 6px;
      border-radius:6px;
      color:var(--text);
      cursor:pointer;
    }
    .option:hover { background:var(--panel-2); }
    .option input { width:auto; accent-color:var(--accent); }
    .option span {
      min-width:0;
      overflow:hidden;
      text-overflow:ellipsis;
      white-space:nowrap;
    }
    .list-scroll {
      min-height:0;
      overflow:auto;
      scrollbar-gutter:stable;
    }
    .pager {
      display:flex;
      align-items:center;
      justify-content:space-between;
      gap:10px;
      padding:10px 12px;
      border-top:1px solid var(--line);
      background:rgba(18,19,16,.96);
    }
    .pager-info { color:var(--muted); font-size:12px; white-space:nowrap; }
    .pager-controls { display:flex; align-items:center; gap:8px; }
    .pager button { padding:6px 9px; }
    .page-jump { width:76px; padding:6px 8px; }
    .pager button:disabled {
      cursor:not-allowed;
      color:var(--faint);
      border-color:var(--line-soft);
      background:#13171d;
    }
    input, button, select {
      min-width:0;
      border:1px solid var(--line);
      background:var(--panel);
      color:var(--text);
      border-radius:6px;
      padding:8px 10px;
      font:inherit;
      outline:none;
    }
    input, select { color:var(--text); }
    input::placeholder { color:var(--faint); }
    input:focus, select:focus { border-color:var(--accent-dim); background:var(--panel-2); }
    button {
      cursor:pointer;
      color:var(--text);
      background:var(--panel-2);
      transition:border-color .15s ease, background .15s ease, color .15s ease;
    }
    button:hover { border-color:var(--accent); color:var(--accent); background:var(--panel-3); }
    table { width:100%; border-collapse:separate; border-spacing:0; }
    th, td { padding:10px 12px; border-bottom:1px solid var(--line-soft); text-align:left; vertical-align:top; }
    th {
      color:var(--faint);
      font-size:11px;
      font-weight:700;
      text-transform:uppercase;
      letter-spacing:.08em;
      background:#11151b;
    }
    tr { cursor:pointer; }
    tbody tr { transition:background .12s ease; }
    tbody tr:hover { background:#1a2028; }
    tbody tr.selected { background:#202b3d; box-shadow:inset 3px 0 0 var(--accent); }
    .time { color:var(--muted); white-space:nowrap; font-size:12px; }
    .model { font-weight:700; color:var(--text); }
    .subline { margin-top:3px; color:var(--faint); font-size:12px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; max-width:240px; }
    .muted { color:var(--muted); }
    .pill {
      display:inline-flex;
      align-items:center;
      min-height:22px;
      border:1px solid var(--line);
      border-radius:999px;
      padding:2px 8px;
      font-size:12px;
      color:var(--muted);
      background:#13171d;
      white-space:nowrap;
    }
    .pill.ok { color:var(--good); border-color:rgba(142,163,111,.42); }
    .pill.partial { color:var(--warn); border-color:rgba(176,140,255,.5); }
    .pill.failed { color:var(--bad); border-color:rgba(196,106,90,.5); }
    .pill.completed, .pill.exact { color:var(--good); border-color:rgba(142,163,111,.42); }
    .pill.open, .pill.inferred, .pill.building, .pill.pending { color:var(--warn); border-color:rgba(176,140,255,.5); }
    .pill.unknown { color:var(--faint); }
    .detail {
      min-height:0;
      overflow:auto;
      padding:18px;
      scrollbar-gutter:stable;
    }
    .detail-head {
      display:grid;
      grid-template-columns:1fr auto;
      gap:14px;
      align-items:start;
      margin-bottom:14px;
      padding-bottom:14px;
      border-bottom:1px solid var(--line);
    }
    .detail-title { min-width:0; }
    .detail-title h2 { margin:0; font-size:18px; line-height:1.2; letter-spacing:.01em; }
    .request-id {
      margin-top:7px;
      color:var(--muted);
      font:12px/1.4 "IBM Plex Mono", "SFMono-Regular", Menlo, Consolas, monospace;
      overflow:hidden;
      text-overflow:ellipsis;
      white-space:nowrap;
      max-width:720px;
    }
    .summary {
      display:grid;
      grid-template-columns:repeat(4, minmax(0,1fr));
      gap:8px;
      margin-bottom:16px;
    }
    .metric {
      border:1px solid var(--line);
      border-radius:8px;
      padding:10px 11px;
      background:var(--panel);
      box-shadow:0 1px 0 rgba(255,255,255,.02) inset;
    }
    .metric span {
      display:block;
      color:var(--faint);
      font-size:11px;
      font-weight:700;
      text-transform:uppercase;
      letter-spacing:.07em;
    }
    .metric strong {
      display:block;
      margin-top:5px;
      overflow:hidden;
      text-overflow:ellipsis;
      white-space:nowrap;
      font-size:14px;
      color:var(--text);
    }
    .items { display:flex; flex-direction:column; gap:10px; }
    .item {
      border:1px solid var(--line);
      border-radius:8px;
      background:var(--panel);
      box-shadow:var(--shadow);
      overflow:hidden;
    }
    details.item > summary {
      cursor:pointer;
      list-style:none;
    }
    details.item > summary::-webkit-details-marker { display:none; }
    details.item > summary:focus-visible { outline:none; box-shadow:inset 0 0 0 2px oklch(70% .16 245 / .42); }
    .collapse-state {
      display:inline-flex;
      align-items:center;
      gap:7px;
      margin-left:auto;
      color:var(--faint);
      font-size:10px;
      white-space:nowrap;
    }
    .item-chevron {
      width:7px;
      height:7px;
      border-right:1.5px solid currentColor;
      border-bottom:1.5px solid currentColor;
      transform:rotate(45deg) translateY(-1px);
      transition:transform .18s cubic-bezier(.22,1,.36,1);
    }
    details.item[open] .state-collapsed { display:none; }
    details.item:not([open]) .state-expanded { display:none; }
    details.item[open] .item-chevron { transform:rotate(225deg) translate(-1px, -1px); }
    .item-head {
      display:flex;
      flex-wrap:wrap;
      gap:10px;
      align-items:center;
      padding:10px 12px;
      border-bottom:1px solid var(--line-soft);
      background:#151a21;
    }
    .item-primary,
    .item-meta {
      display:flex;
      flex-wrap:wrap;
      align-items:center;
      gap:7px;
      min-width:0;
    }
    .item-primary { flex:1 1 520px; }
    .item-meta { flex:0 1 auto; justify-content:flex-end; }
    .item-title {
      color:var(--text);
      font-weight:750;
      font-size:12px;
      white-space:nowrap;
    }
    .role-badge {
      display:inline-flex;
      align-items:center;
      gap:6px;
      min-height:24px;
      border:1px solid var(--line);
      border-radius:999px;
      padding:2px 9px;
      color:var(--muted);
      background:var(--panel-2);
      font-size:11px;
      font-weight:750;
      line-height:1;
      white-space:nowrap;
    }
    .role-badge:before {
      content:"";
      width:6px;
      height:6px;
      flex:0 0 6px;
      border-radius:50%;
      background:currentColor;
      box-shadow:0 0 0 3px rgba(255,255,255,.035);
    }
    .role-user { color:var(--role-user); border-color:rgba(127,200,241,.38); background:var(--role-user-bg); }
    .role-system { color:var(--role-system); border-color:rgba(200,167,232,.38); background:var(--role-system-bg); }
    .role-assistant { color:var(--role-assistant); border-color:rgba(134,168,255,.4); background:var(--role-assistant-bg); }
    .role-developer { color:var(--role-developer); border-color:rgba(230,154,154,.4); background:var(--role-developer-bg); }
    .role-tool { color:var(--role-tool); border-color:rgba(127,198,164,.38); background:var(--role-tool-bg); }
    .call-id {
      display:inline-flex;
      align-items:baseline;
      gap:7px;
      min-width:0;
      max-width:100%;
      border:1px solid rgba(79,140,255,.35);
      border-radius:5px;
      padding:3px 7px;
      background:rgba(79,140,255,.075);
    }
    .call-id-label {
      flex:0 0 auto;
      color:var(--faint);
      font-size:9px;
      font-weight:800;
      line-height:1;
      text-transform:uppercase;
      letter-spacing:.07em;
    }
    .call-id code {
      min-width:0;
      color:#9bb9ff;
      font:11px/1.35 "IBM Plex Mono", "SFMono-Regular", Menlo, Consolas, monospace;
      overflow-wrap:anywhere;
    }
    .item-context {
      color:var(--muted);
      font:11px/1.35 "IBM Plex Mono", "SFMono-Regular", Menlo, Consolas, monospace;
      overflow-wrap:anywhere;
    }
    dialog {
      width:min(760px, calc(100vw - 32px));
      max-height:min(760px, calc(100vh - 32px));
      border:1px solid var(--line);
      border-radius:8px;
      padding:0;
      color:var(--text);
      background:var(--panel);
      box-shadow:var(--shadow);
    }
    dialog::backdrop { background:rgba(5,6,5,.78); backdrop-filter:blur(3px); }
    .export-head {
      display:flex;
      align-items:center;
      justify-content:space-between;
      gap:12px;
      padding:14px 16px;
      border-bottom:1px solid var(--line);
    }
    .export-head h2 { margin:0; font-size:15px; }
    .icon-button { width:34px; height:34px; padding:0; font-size:20px; line-height:1; }
    .export-body { display:grid; gap:14px; padding:16px; overflow:auto; }
    .export-controls { display:flex; flex-wrap:wrap; align-items:center; justify-content:space-between; gap:12px; }
    .toggle { display:flex; align-items:center; gap:8px; color:var(--muted); }
    .toggle input { width:auto; accent-color:var(--accent); }
    .export-preview { color:var(--muted); font-size:13px; }
    .batch-list { display:grid; gap:8px; }
    .history-exports {
      border-top:1px solid var(--line);
      margin-top:4px;
      padding-top:8px;
    }
    .history-exports summary {
      cursor:pointer;
      color:var(--muted);
      font-size:12px;
      font-weight:700;
      user-select:none;
    }
    .history-exports summary:hover { color:var(--accent); }
    .history-export-list { display:grid; gap:8px; margin-top:8px; }
    .batch-row {
      display:grid;
      grid-template-columns:minmax(0,1fr) auto auto;
      align-items:center;
      gap:10px;
      padding:10px;
      border-top:1px solid var(--line-soft);
    }
    .batch-tag { min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font:12px/1.4 "IBM Plex Mono", monospace; }
    .batch-meta { color:var(--faint); font-size:11px; }
    .batch-progress { display:grid; grid-template-columns:auto minmax(90px, 1fr); align-items:center; gap:8px; margin-top:7px; color:var(--muted); font-size:11px; }
    .batch-progress-track { height:6px; overflow:hidden; border:1px solid var(--line-soft); border-radius:999px; background:var(--code); }
    .batch-progress-fill { display:block; height:100%; border-radius:inherit; background:linear-gradient(90deg, var(--accent-dim), var(--accent)); transition:width .25s ease; }
    pre {
      margin:0;
      padding:12px;
      overflow:auto;
      max-height:420px;
      white-space:pre-wrap;
      word-break:break-word;
      background:var(--code);
      color:#d9e1ed;
      font:12px/1.55 "IBM Plex Mono", "SFMono-Regular", Menlo, Consolas, monospace;
    }
    .empty {
      display:flex;
      align-items:center;
      justify-content:center;
      min-height:220px;
      border:1px dashed var(--line);
      border-radius:8px;
      color:var(--muted);
      background:rgba(23,25,19,.55);
    }
    .loading-card {
      display:grid;
      place-items:center;
      gap:18px;
      min-height:220px;
      border:1px solid var(--line);
      border-radius:8px;
      color:var(--muted);
      background:rgba(23,25,19,.55);
    }
    .loading-copy {
      display:flex;
      align-items:center;
      gap:10px;
      color:var(--text);
      font-weight:700;
    }
    .loader {
      width:16px;
      height:16px;
      border:2px solid var(--line);
      border-top-color:var(--accent);
      border-radius:50%;
      animation:spin .8s linear infinite;
    }
    .loading-lines {
      display:grid;
      gap:8px;
      width:min(420px, 72%);
    }
    .loading-lines i {
      display:block;
      height:8px;
      border-radius:999px;
      background:linear-gradient(90deg, var(--panel-2), var(--line), var(--panel-2));
      background-size:200% 100%;
      animation:scan 1.2s ease-in-out infinite;
    }
    .loading-lines i:nth-child(2) { width:78%; animation-delay:.12s; }
    .loading-lines i:nth-child(3) { width:56%; animation-delay:.24s; }
    @keyframes spin {
      to { transform:rotate(360deg); }
    }
    @keyframes scan {
      0% { background-position:100% 0; opacity:.42; }
      50% { opacity:.85; }
      100% { background-position:-100% 0; opacity:.42; }
    }
    @media (max-width: 980px) {
      header { grid-template-columns:1fr; align-items:start; }
      body { overflow:auto; }
      main { grid-template-columns:1fr; overflow:visible; }
      .turn-list { border-right:0; border-bottom:1px solid var(--line); max-height:46vh; }
      .filters { grid-template-columns:1fr; }
      .time-range { grid-template-columns:1fr 1fr; }
      .clear-time { grid-column:1 / -1; }
      .list-scroll { max-height:none; }
      .detail { overflow:visible; }
      .summary { grid-template-columns:1fr 1fr; }
      .detail-head { grid-template-columns:1fr; }
      .item-meta { width:100%; justify-content:flex-start; }
      .batch-row { grid-template-columns:1fr auto; }
      .batch-tag { grid-column:1 / -1; }
    }

    /* 2026 workspace refresh: a quieter, denser operator surface. */
    :root {
      --bg:oklch(14.5% .009 255);
      --panel:oklch(18% .01 255);
      --panel-2:oklch(21% .012 255);
      --panel-3:oklch(24% .014 255);
      --line:oklch(34% .018 255);
      --line-soft:oklch(27% .014 255);
      --text:oklch(93% .012 255);
      --muted:oklch(70% .015 255);
      --faint:oklch(55% .014 255);
      --accent:oklch(70% .16 245);
      --accent-dim:oklch(58% .14 245);
      --accent-surface:oklch(23% .045 245);
      --good:oklch(75% .11 142);
      --warn:oklch(72% .14 300);
      --bad:oklch(70% .15 28);
      --code:oklch(12.5% .009 255);
      --shadow:0 18px 42px oklch(7% .01 255 / .34);
      --focus:0 0 0 3px oklch(70% .16 245 / .22);
    }
    body {
      background:var(--bg);
      grid-template-columns:minmax(0, 1fr);
      grid-template-rows:64px minmax(0, 1fr);
      font-family:"Avenir Next", Avenir, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    body:before {
      opacity:.17;
      background-image:
        linear-gradient(oklch(44% .025 245 / .06) 1px, transparent 1px),
        linear-gradient(90deg, oklch(44% .025 245 / .06) 1px, transparent 1px);
      background-size:32px 32px;
      mask-image:linear-gradient(to bottom, black, transparent 72%);
    }
    header {
      grid-column:1;
      grid-row:1;
      grid-template-columns:auto minmax(0, 1fr) auto;
      gap:18px;
      min-height:64px;
      padding:10px 18px;
      border-color:var(--line-soft);
      background:oklch(14.5% .009 255 / .98);
      backdrop-filter:none;
    }
    .topbar-brand {
      display:flex;
      align-items:center;
      gap:10px;
      min-width:0;
    }
    .brand-copy {
      min-width:0;
      white-space:nowrap;
    }
    .brand-copy strong { display:block; margin-top:2px; font-size:13px; line-height:1.2; }
    .eyebrow,
    .dialog-eyebrow {
      color:var(--accent);
      font-size:10px;
      font-weight:800;
      line-height:1.2;
      letter-spacing:.14em;
      text-transform:uppercase;
    }
    .mark {
      position:relative;
      width:36px;
      height:36px;
      flex:0 0 36px;
      border:1px solid oklch(70% .16 245 / .5);
      border-radius:10px;
      background:var(--accent-surface);
      box-shadow:0 8px 22px oklch(8% .01 255 / .28), inset 0 1px 0 oklch(90% .04 245 / .09);
    }
    .mark:before,
    .mark:after {
      content:"";
      position:absolute;
    }
    .mark:before {
      inset:9px;
      border:1px solid var(--accent);
      border-radius:3px;
      transform:rotate(45deg);
    }
    .mark:after {
      top:50%;
      left:50%;
      width:4px;
      height:4px;
      border-radius:50%;
      background:var(--accent);
      box-shadow:0 0 12px oklch(70% .16 245 / .72);
      transform:translate(-50%, -50%);
    }
    .status {
      display:inline-flex;
      align-items:center;
      gap:8px;
      min-height:32px;
      padding:5px 11px;
      border:1px solid var(--line-soft);
      border-radius:999px;
      background:var(--panel);
      color:var(--muted);
      font-variant-numeric:tabular-nums;
    }
    .status:before {
      content:"";
      width:7px;
      height:7px;
      flex:0 0 7px;
      border-radius:50%;
      background:var(--good);
      box-shadow:0 0 0 4px oklch(75% .11 142 / .09);
    }
    .actions { gap:9px; }
    .actions button { min-height:38px; padding:8px 13px; font-weight:700; }
    .topbar-meta {
      display:flex;
      align-items:center;
      gap:10px;
    }
    .refresh-action svg,
    .nav-icon svg { width:18px; height:18px; fill:none; stroke:currentColor; stroke-width:2; stroke-linecap:round; stroke-linejoin:round; }
    .workspace-nav {
      display:flex;
      align-items:center;
      justify-self:start;
      gap:4px;
      min-width:0;
      padding:4px;
      border:1px solid var(--line-soft);
      border-radius:10px;
      background:oklch(17% .01 255);
    }
    .nav-link {
      display:flex;
      align-items:center;
      justify-content:center;
      gap:8px;
      min-height:34px;
      padding:0 13px;
      border:1px solid transparent;
      border-radius:7px;
      color:var(--muted);
      font-size:12px;
      font-weight:700;
      text-decoration:none;
      transition:color .18s cubic-bezier(.22,1,.36,1), background .18s cubic-bezier(.22,1,.36,1);
    }
    .nav-link:hover { color:var(--text); background:var(--panel); }
    .nav-link.active { border-color:oklch(70% .16 245 / .25); color:var(--accent); background:var(--accent-surface); }
    .nav-link:focus-visible { outline:none; box-shadow:var(--focus); }
    .nav-icon {
      display:grid;
      place-items:center;
      width:18px;
      min-width:18px;
      color:currentColor;
      font-size:17px;
      line-height:1;
    }
    .nav-label { white-space:nowrap; }
    .actions .refresh-action {
      display:grid;
      place-items:center;
      width:40px;
      min-width:40px;
      height:40px;
      min-height:40px;
      padding:0;
      color:var(--muted);
    }
    main {
      grid-column:1;
      grid-row:2;
      grid-template-columns:minmax(600px, 49%) minmax(0, 1fr);
    }
    .turn-list {
      border-color:var(--line-soft);
      background:oklch(16.5% .01 255 / .78);
    }
    .filters {
      position:relative;
      z-index:30;
      grid-template-columns:1fr;
      gap:9px;
      padding:10px 14px 12px;
      overflow:visible;
      border-color:var(--line-soft);
      background:oklch(16.5% .01 255 / .98);
      backdrop-filter:none;
    }
    .filter-heading {
      grid-column:1 / -1;
      display:flex;
      align-items:center;
      justify-content:space-between;
      gap:12px;
      padding-bottom:0;
    }
    .filter-heading strong { display:block; font-size:12px; }
    .filter-heading-tools { display:flex; align-items:center; gap:9px; }
    .clear-filters,
    .advanced-toggle {
      display:inline-flex;
      align-items:center;
      justify-content:center;
      gap:7px;
      min-height:30px;
      padding:4px 9px;
      color:var(--muted);
      font-size:11px;
      line-height:1;
    }
    .advanced-toggle[aria-expanded="true"] { border-color:var(--accent-dim); color:var(--accent); background:var(--accent-surface); }
    .toggle-caret,
    .select-caret {
      display:inline-block;
      width:7px;
      height:7px;
      flex:0 0 7px;
      border-right:1.5px solid currentColor;
      border-bottom:1.5px solid currentColor;
      transform:rotate(45deg) translateY(-1px);
      transition:transform .18s cubic-bezier(.22,1,.36,1);
    }
    .advanced-toggle[aria-expanded="true"] .toggle-caret { transform:rotate(225deg) translate(-1px, -1px); }
    .filter-count {
      display:inline-grid;
      place-items:center;
      min-width:17px;
      height:17px;
      margin-left:4px;
      border-radius:999px;
      background:var(--accent);
      color:oklch(16% .02 245);
      font-size:10px;
      font-weight:800;
    }
    .quick-filters {
      display:grid;
      grid-template-columns:repeat(4, minmax(0, 1fr));
      gap:7px;
    }
    .advanced-filters {
      display:grid;
      grid-template-columns:repeat(3, minmax(0, 1fr));
      gap:8px;
      padding-top:9px;
      border-top:1px solid var(--line-soft);
    }
    .advanced-filters[hidden] { display:none; }
    .sr-only {
      position:absolute !important;
      width:1px !important;
      height:1px !important;
      padding:0 !important;
      margin:-1px !important;
      overflow:hidden !important;
      clip:rect(0, 0, 0, 0) !important;
      white-space:nowrap !important;
      border:0 !important;
    }
    .filter-field,
    .filter-control {
      display:flex;
      flex-direction:column;
      gap:5px;
      min-width:0;
    }
    .field-kicker,
    .filter-control > span {
      color:var(--faint);
      font-size:10px;
      font-weight:800;
      line-height:1;
      letter-spacing:.08em;
      text-transform:uppercase;
    }
    .select-button,
    .filter-control input,
    .filter-control select,
    .time-field input { min-height:36px; padding-top:7px; padding-bottom:7px; }
    .select-button span.select-caret { color:var(--accent); }
    .select-menu {
      right:auto;
      top:calc(100% + 7px);
      z-index:60;
      width:max(100%, 220px);
      max-height:min(360px, calc(100vh - 220px));
      overflow-y:auto;
      overscroll-behavior:contain;
      scrollbar-gutter:stable;
      border-color:var(--line);
      border-radius:10px;
      background:oklch(17% .01 255);
      box-shadow:0 22px 60px oklch(5% .01 255 / .55);
    }
    .quick-filters .filter-field:nth-child(2) .select-menu { right:0; left:auto; }
    .select-actions {
      position:sticky;
      top:-6px;
      z-index:2;
      background:oklch(17% .01 255);
    }
    input, button, select {
      border-color:var(--line);
      border-radius:8px;
      background:var(--panel);
    }
    input:hover, select:hover { border-color:oklch(44% .025 255); }
    input:focus, select:focus {
      border-color:var(--accent-dim);
      background:var(--panel-2);
      box-shadow:var(--focus);
    }
    button:focus-visible, input:focus-visible, select:focus-visible, summary:focus-visible {
      outline:none;
      box-shadow:var(--focus);
    }
    button { transition:border-color .18s cubic-bezier(.22,1,.36,1), background .18s cubic-bezier(.22,1,.36,1), color .18s cubic-bezier(.22,1,.36,1), transform .18s cubic-bezier(.22,1,.36,1); }
    button:active { transform:translateY(1px); }
    .list-scroll { background:oklch(16% .01 255 / .52); }
    table { table-layout:fixed; }
    th:nth-child(1) { width:21%; }
    th:nth-child(2) { width:35%; }
    th:nth-child(3) { width:16%; }
    th:nth-child(4) { width:13%; }
    th:nth-child(5) { width:15%; }
    th {
      position:sticky;
      top:0;
      z-index:3;
      padding-top:11px;
      padding-bottom:11px;
      border-color:var(--line-soft);
      background:oklch(17.5% .01 255 / .98);
      backdrop-filter:blur(10px);
    }
    td { padding-top:12px; padding-bottom:12px; }
    td { overflow:hidden; }
    td .model,
    td .pill {
      max-width:100%;
      overflow:hidden;
      text-overflow:ellipsis;
      white-space:nowrap;
    }
    tbody tr { position:relative; }
    tbody tr:hover { background:oklch(22% .017 255 / .72); }
    tbody tr.selected {
      background:var(--accent-surface);
      box-shadow:inset 0 0 0 1px oklch(70% .16 245 / .28);
    }
    tbody tr.selected .model { color:oklch(88% .07 245); }
    .pager {
      min-height:58px;
      padding:10px 16px;
      border-color:var(--line-soft);
      background:oklch(16.5% .01 255 / .98);
    }
    .pager-info { font-variant-numeric:tabular-nums; }
    .detail {
      padding:22px 24px 32px;
      background:oklch(15.5% .01 255 / .7);
    }
    .detail-head {
      margin-bottom:18px;
      padding:16px;
      border:1px solid var(--line);
      border-radius:12px;
      background:var(--panel);
      box-shadow:0 14px 36px oklch(8% .01 255 / .2);
    }
    .detail-title h2 { font-size:20px; letter-spacing:-.02em; }
    .request-id { margin-top:8px; color:var(--faint); }
    .summary {
      display:grid;
      gap:0;
      overflow:hidden;
      border:1px solid var(--line);
      border-radius:11px;
      background:var(--panel);
    }
    .metric {
      min-width:0;
      border:0;
      border-right:1px solid var(--line-soft);
      border-radius:0;
      padding:12px 14px;
      background:transparent;
      box-shadow:none;
    }
    .metric:last-child { border-right:0; }
    .metric strong { margin-top:4px; font-size:15px; font-variant-numeric:tabular-nums; }
    .items { gap:12px; }
    .item {
      border-color:var(--line);
      border-radius:11px;
      background:oklch(18.5% .01 255);
      box-shadow:0 12px 30px oklch(7% .01 255 / .2);
      transition:border-color .18s cubic-bezier(.22,1,.36,1), transform .18s cubic-bezier(.22,1,.36,1);
    }
    .item:hover { border-color:oklch(41% .025 255); transform:translateY(-1px); }
    .item-head { padding:11px 13px; background:oklch(20% .012 255); }
    details.item > summary:hover { background:oklch(22% .017 255); }
    .call-id { border-radius:7px; }
    pre { padding:15px; max-height:520px; line-height:1.65; }
    .empty-detail {
      min-height:calc(100vh - 160px);
      flex-direction:column;
      gap:8px;
      border:0;
      background:transparent;
      text-align:center;
    }
    .empty-detail strong { color:var(--text); font-size:15px; }
    .empty-detail > span { max-width:34ch; color:var(--faint); font-size:12px; }
    .empty-orbit {
      position:relative;
      width:62px;
      height:62px;
      margin-bottom:8px;
      border:1px solid var(--line);
      border-radius:50%;
    }
    .empty-orbit:before,
    .empty-orbit:after {
      content:"";
      position:absolute;
      border-radius:50%;
    }
    .empty-orbit:before { inset:12px; border:1px solid oklch(70% .16 245 / .45); }
    .empty-orbit:after { width:8px; height:8px; top:7px; right:8px; background:var(--accent); box-shadow:0 0 18px oklch(70% .16 245 / .45); }
    .exports-view {
      grid-column:1;
      grid-row:2;
      min-height:0;
      overflow:auto;
      background:oklch(15.5% .01 255 / .86);
    }
    .export-body {
      display:grid;
      gap:24px;
      width:min(1120px, 100%);
      margin:0 auto;
      padding:24px;
    }
    .export-overview {
      display:grid;
      grid-template-columns:minmax(0, 1fr) auto;
      align-items:center;
      gap:24px;
      padding:18px 0 22px;
      border-bottom:1px solid var(--line-soft);
    }
    .section-kicker {
      color:var(--faint);
      font-size:10px;
      font-weight:800;
      letter-spacing:.1em;
      text-transform:uppercase;
    }
    .export-preview { margin-top:8px; color:var(--muted); }
    .preview-count {
      display:flex;
      flex-wrap:wrap;
      align-items:baseline;
      gap:8px;
      color:var(--text);
    }
    .preview-count strong { color:var(--accent); font-size:24px; font-variant-numeric:tabular-nums; }
    .preview-detail { margin-top:5px; color:var(--faint); font-size:12px; }
    .preview-empty { display:grid; gap:5px; max-width:54ch; }
    .preview-empty strong { color:var(--text); font-size:14px; }
    .preview-empty span { color:var(--faint); font-size:12px; }
    .export-controls {
      display:flex;
      flex-direction:column;
      align-items:flex-end;
      gap:12px;
    }
    .toggle { cursor:pointer; }
    .create-export {
      border-color:oklch(70% .16 245 / .65);
      background:var(--accent);
      color:oklch(16% .02 245);
      font-weight:800;
    }
    .create-export:hover { border-color:oklch(78% .14 240); background:oklch(76% .15 242); color:oklch(14% .02 245); }
    .create-export:disabled,
    .create-export:disabled:hover {
      border-color:var(--line-soft);
      background:var(--panel-2);
      color:var(--faint);
      cursor:not-allowed;
      opacity:.72;
      transform:none;
    }
    .batch-list { display:grid; gap:24px; }
    .export-section { overflow:visible; }
    .export-section-head,
    .history-exports > summary {
      display:flex;
      align-items:center;
      justify-content:space-between;
      gap:12px;
      margin-bottom:10px;
    }
    .export-section-head h3,
    .history-exports > summary strong { margin:2px 0 0; color:var(--text); font-size:14px; }
    .section-count {
      display:inline-grid;
      place-items:center;
      min-width:24px;
      height:24px;
      border:1px solid var(--line);
      border-radius:999px;
      color:var(--muted);
      background:var(--panel);
      font-size:11px;
      font-variant-numeric:tabular-nums;
    }
    .active-export-list,
    .history-export-list { display:grid; gap:9px; }
    .batch-row {
      grid-template-columns:minmax(0, 1fr) auto minmax(170px, auto);
      gap:14px;
      padding:14px;
      border:1px solid var(--line-soft);
      border-radius:9px;
      background:var(--panel);
    }
    .batch-actions { display:flex; flex-wrap:wrap; justify-content:flex-end; gap:6px; }
    .batch-actions button { min-height:30px; padding:5px 8px; font-size:11px; }
    .history-exports {
      margin-top:0;
      padding-top:20px;
      border-top:1px solid var(--line-soft);
    }
    .history-exports > summary { margin-bottom:0; padding:0; list-style:none; }
    .history-exports > summary > span:first-child { display:grid; gap:2px; }
    .history-exports > summary::-webkit-details-marker { display:none; }
    .history-exports[open] > summary { margin-bottom:10px; }
    .export-empty {
      display:flex;
      align-items:center;
      min-height:70px;
      padding:14px;
      border:1px dashed var(--line);
      border-radius:8px;
      color:var(--faint);
      background:oklch(17% .01 255 / .55);
      font-size:12px;
    }
    .batch-progress-fill { background:var(--accent); }
    @media (max-width: 1180px) {
      main { grid-template-columns:minmax(560px, 54%) minmax(0, 1fr); }
      .detail { padding:18px; }
      .export-body { padding:20px; }
    }
    @media (max-width: 980px) {
      html, body { height:100%; min-height:100%; overflow:hidden; }
      body {
        display:grid;
        grid-template-columns:minmax(0, 1fr);
        grid-template-rows:58px minmax(0, 1fr);
      }
      header {
        grid-column:1;
        grid-row:1;
        grid-template-columns:auto minmax(0, 1fr) auto;
        gap:10px;
        min-height:58px;
        padding:8px 12px;
      }
      .status { white-space:normal; }
      main {
        grid-column:1;
        grid-row:2;
        display:block;
        grid-template-columns:1fr;
        overflow:auto;
      }
      .turn-list {
        display:grid;
        grid-template-rows:auto minmax(320px, 54vh) auto;
        max-height:none;
        overflow:visible;
      }
      .filters { grid-template-columns:1fr; }
      .list-scroll { max-height:54vh; }
      .filter-heading { align-items:start; }
      .detail { padding:16px; }
      .empty-detail { min-height:320px; }
      .metric { border-right:0; border-bottom:1px solid var(--line-soft); }
      .metric:nth-child(odd) { border-right:1px solid var(--line-soft); }
      .metric:nth-last-child(-n+2) { border-bottom:0; }
      .exports-view { grid-column:1; grid-row:2; }
      .export-overview { grid-template-columns:1fr; align-items:start; }
      .export-controls { align-items:flex-start; }
    }
    @media (max-width: 760px) {
      .status { display:none; }
      .brand-copy .eyebrow { display:none; }
    }
    @media (max-width: 620px) {
      header { gap:8px; padding-right:9px; padding-left:9px; }
      .brand-copy { display:none; }
      .mark { width:32px; height:32px; flex-basis:32px; }
      .workspace-nav { padding:3px; }
      .nav-link { min-height:32px; padding-right:10px; padding-left:10px; }
      .actions { width:auto; }
      .actions button { flex:0 0 36px; }
      .quick-filters { grid-template-columns:repeat(2, minmax(0, 1fr)); }
      .advanced-filters { grid-template-columns:1fr; }
      .advanced-filters .time-range { grid-column:auto; }
      .filters { padding:12px; }
      th:nth-child(3),
      td:nth-child(3),
      th:nth-child(4),
      td:nth-child(4) { display:none; }
      th:nth-child(1) { width:23%; }
      th:nth-child(2) { width:52%; }
      th:nth-child(5) { width:25%; }
      th, td { padding-right:9px; padding-left:9px; }
      .time-range { grid-template-columns:1fr 1fr; }
      .clear-time { grid-column:1 / -1; }
      .pager { align-items:flex-start; flex-direction:column; }
      .pager-controls { width:100%; }
      .pager-controls button { flex:1; }
      .page-jump { width:68px; }
      .summary { grid-template-columns:1fr 1fr; }
      .export-body { gap:20px; padding:16px; }
      .export-overview { gap:18px; padding-top:8px; }
      .export-controls { align-items:stretch; }
      .create-export { width:100%; }
      .batch-row { grid-template-columns:1fr auto; }
      .batch-actions { grid-column:1 / -1; justify-content:flex-start; }
    }
    @media (max-width: 440px) {
      .nav-icon { display:none; }
    }
    @media (prefers-reduced-motion: reduce) {
      *, *:before, *:after { scroll-behavior:auto !important; animation-duration:.01ms !important; animation-iteration-count:1 !important; transition-duration:.01ms !important; }
    }
  </style>
</head>
<body>
  <header>
    <div class="topbar-brand">
      <div class="mark" aria-hidden="true"></div>
      <div class="brand-copy">
        <div class="eyebrow">Request intelligence</div>
        <strong>Session Log Viewer</strong>
      </div>
    </div>
    <nav class="workspace-nav" aria-label="Workspace">
      <a id="turnNav" class="nav-link active" href="#sessions" aria-current="page" title="Session data"><span class="nav-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><rect width="18" height="18" x="3" y="3" rx="2"/><path d="M9 3v18M3 9h18M3 15h18"/></svg></span><span class="nav-label">Session data</span></a>
      <a id="exportNav" class="nav-link" href="#exports" title="Exports"><span class="nav-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/></svg></span><span class="nav-label">Exports</span></a>
    </nav>
    <div class="topbar-meta">
      <div id="status" class="status">Loading...</div>
      <div class="actions">
        <button id="refresh" class="refresh-action" type="button" title="Refresh data" aria-label="Refresh data"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 12a9 9 0 0 1-15.219 6.219L3 15M3 21v-6h6M3 12A9 9 0 0 1 18.219 5.781L21 9M21 3v6h-6"/></svg></button>
      </div>
    </div>
  </header>
  <main id="turnView" class="workspace-view">
    <aside class="turn-list">
      <div class="filters">
        <div class="filter-heading">
          <strong>Explore sessions</strong>
          <div class="filter-heading-tools"><button id="advancedToggle" class="advanced-toggle" type="button" aria-expanded="false" aria-controls="advancedFilters">More filters <span id="advancedCount" class="filter-count" hidden></span><span class="toggle-caret" aria-hidden="true"></span></button><button id="clearFilters" class="clear-filters" type="button">Reset</button></div>
        </div>
        <div class="quick-filters">
          <div class="filter-field" data-filter="model_name">
            <button class="select-button" id="modelFilter" type="button" aria-label="Filter by model" aria-haspopup="true" aria-controls="modelMenu" aria-expanded="false"><strong>All models</strong><span class="select-caret" aria-hidden="true"></span></button>
            <div class="select-menu" id="modelMenu" role="group" aria-label="Model options"></div>
          </div>
          <div class="filter-field" data-filter="username">
            <button class="select-button" id="userFilter" type="button" aria-label="Filter by user" aria-haspopup="true" aria-controls="userMenu" aria-expanded="false"><strong>All users</strong><span class="select-caret" aria-hidden="true"></span></button>
            <div class="select-menu" id="userMenu" role="group" aria-label="User options"></div>
          </div>
          <label class="filter-control"><span class="sr-only">Session ID</span><input id="sessionFilter" type="search" placeholder="Session ID"></label>
        </div>
        <div id="advancedFilters" class="advanced-filters" hidden>
          <label class="filter-control"><span>Confidence</span><select id="attributionFilter" aria-label="Attribution">
            <option value="">All confidence</option>
            <option value="exact">Exact</option>
            <option value="inferred">Inferred</option>
            <option value="unknown">Unknown</option>
          </select></label>
          <label class="filter-control"><span>Session state</span><select id="turnStatusFilter" aria-label="Session status">
            <option value="">All states</option>
            <option value="completed">Completed</option>
            <option value="open">Open</option>
            <option value="unknown">Unknown</option>
          </select></label>
          <label class="filter-control"><span>Export state</span><select id="exportedFilter" aria-label="Export status">
            <option value="">All export states</option>
            <option value="false">Not exported</option>
            <option value="true">Exported</option>
          </select></label>
          <div class="time-range">
            <label class="time-field" for="startTime"><span>Start</span><input id="startTime" type="datetime-local"></label>
            <label class="time-field" for="endTime"><span>End</span><input id="endTime" type="datetime-local"></label>
            <button id="clearTime" class="clear-time" type="button">Clear time</button>
          </div>
        </div>
      </div>
      <div class="list-scroll">
        <table>
          <thead><tr><th scope="col">Ended</th><th scope="col">Session</th><th scope="col">Model</th><th scope="col">User</th><th scope="col">State</th></tr></thead>
          <tbody id="rows"></tbody>
        </table>
      </div>
      <div class="pager">
        <div id="pageInfo" class="pager-info">Page 1</div>
        <div class="pager-controls">
          <button id="prevPage" type="button">Prev</button>
          <input id="pageJump" class="page-jump" type="number" min="1" inputmode="numeric" aria-label="Jump to page" placeholder="Page">
          <button id="jumpPage" type="button">Go</button>
          <button id="nextPage" type="button">Next</button>
        </div>
      </div>
    </aside>
    <section id="detail" class="detail"><div class="empty empty-detail"><div class="empty-orbit" aria-hidden="true"></div><strong>No session selected</strong></div></section>
  </main>
  <section id="exportsView" class="workspace-view exports-view" hidden>
    <div class="export-body">
      <section class="export-overview">
        <div class="export-readiness">
          <div class="section-kicker">Current selection</div>
          <div id="exportPreview" class="export-preview">Analyzing eligible sessions...</div>
        </div>
        <div class="export-controls">
          <label class="toggle"><input id="includeInferred" type="checkbox"><span>Include inferred completed sessions</span></label>
          <button id="createExport" class="create-export" type="button">Create export batch</button>
        </div>
      </section>
      <div id="batchList" class="batch-list"><div class="export-empty">Loading export batches...</div></div>
    </div>
  </section>
  <script>
    const rowsEl = document.getElementById('rows')
    const detailEl = document.getElementById('detail')
    const statusEl = document.getElementById('status')
    const pageInfoEl = document.getElementById('pageInfo')
    const prevPageEl = document.getElementById('prevPage')
    const nextPageEl = document.getElementById('nextPage')
    const pageJumpEl = document.getElementById('pageJump')
    const jumpPageEl = document.getElementById('jumpPage')
    const startTimeEl = document.getElementById('startTime')
    const endTimeEl = document.getElementById('endTime')
    const clearTimeEl = document.getElementById('clearTime')
    const clearFiltersEl = document.getElementById('clearFilters')
    const sessionFilterEl = document.getElementById('sessionFilter')
    const attributionFilterEl = document.getElementById('attributionFilter')
    const turnStatusFilterEl = document.getElementById('turnStatusFilter')
    const exportedFilterEl = document.getElementById('exportedFilter')
    const turnViewEl = document.getElementById('turnView')
    const exportsViewEl = document.getElementById('exportsView')
    const turnNavEl = document.getElementById('turnNav')
    const exportNavEl = document.getElementById('exportNav')
    const advancedFiltersEl = document.getElementById('advancedFilters')
    const advancedToggleEl = document.getElementById('advancedToggle')
    const advancedCountEl = document.getElementById('advancedCount')
    const includeInferredEl = document.getElementById('includeInferred')
    const exportPreviewEl = document.getElementById('exportPreview')
    const createExportEl = document.getElementById('createExport')
    const batchListEl = document.getElementById('batchList')
    const selectFields = Array.from(document.querySelectorAll('.filter-field'))
    const state = {
      page: 1,
      pageSize: 50,
      total: 0,
      time: {
        start: '',
        end: ''
      },
      selected: {
        model_name: new Set(),
        username: new Set()
      }
    }
    let selectedId = 0
    let detailRequestToken = 0

    const esc = value => String(value ?? '').replace(/[&<>"']/g, s => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[s]))
    const pretty = value => { try { return JSON.stringify(JSON.parse(value), null, 2) } catch { return value || '' } }
    const parsedContent = value => { try { return JSON.parse(value) } catch { return null } }
    const roleClass = role => {
      const normalized = String(role || '').toLowerCase()
      if (['user', 'system', 'assistant', 'developer', 'tool'].includes(normalized)) return 'role-' + normalized
      return 'role-other'
    }
    const callIdFor = item => {
      if (!['tool_call', 'tool_result'].includes(item.item_type)) return ''
      const content = parsedContent(item.content)
      const contentCallId = content && typeof content === 'object' && !Array.isArray(content)
        ? content.call_id || content.tool_call_id || ''
        : ''
      return item.tool_call_id || contentCallId
    }
    const displayContentFor = item => {
      const content = parsedContent(item.content)
      const callId = callIdFor(item)
      if (!['tool_call', 'tool_result'].includes(item.item_type) || !content || typeof content !== 'object' || Array.isArray(content) || !callId) {
        return pretty(item.content)
      }
      const displayContent = { ...content }
      if (displayContent.call_id === callId) delete displayContent.call_id
      if (displayContent.tool_call_id === callId) delete displayContent.tool_call_id
      return JSON.stringify(displayContent, null, 2)
    }
    const completionStatusLabel = value => ({ completed:'Completed', open:'Open', unknown:'Unknown', pending:'Queued', failed:'Failed', final:'Final', initial:'Initial', in_progress:'In progress', incomplete:'Incomplete', analysis:'Analysis', commentary:'Commentary' }[String(value || '').toLowerCase()] || String(value || 'Unknown'))
    const attributionLabel = value => ({ exact:'Exact', inferred:'Inferred', unknown:'Unknown' }[String(value || '').toLowerCase()] || String(value || 'Unknown'))
    const batchStatusLabel = value => ({ pending:'Queued', building:'Exporting', completed:'Completed', failed:'Failed' }[String(value || '').toLowerCase()] || String(value || 'Unknown'))
    const roleLabel = value => ({ user:'User', system:'System', assistant:'Assistant', developer:'Developer', tool:'Tool' }[String(value || '').toLowerCase()] || String(value || ''))
    const itemTypeLabel = value => ({ message:'Message', reasoning:'Reasoning', tool_call:'Tool call', tool_result:'Tool result', function_call:'Function call', function_result:'Function result' }[String(value || '').toLowerCase()] || String(value || 'Item'))
    const phaseLabel = value => ({ input:'Input', output:'Output' }[String(value || '').toLowerCase()] || String(value || ''))
    const localizedError = value => String(value || '')
    const detailLoadingHTML = () => [
      '<div class="loading-card" aria-live="polite">',
      '<div class="loading-copy"><span class="loader"></span><span>Loading session</span></div>',
      '<div class="loading-lines"><i></i><i></i><i></i></div>',
      '</div>'
    ].join('')
    const detailErrorHTML = message => '<div class="empty">Failed to load session: ' + esc(localizedError(message) || 'unknown error') + '</div>'
    const emptyItemsHTML = () => '<div class="empty">No session items.</div>'
    const timestampFromLocalInput = value => {
      if (!value) return 0
      const date = new Date(value)
      if (Number.isNaN(date.getTime())) return 0
      return Math.floor(date.getTime() / 1000)
    }
    const qs = (includePage = true) => {
      const p = new URLSearchParams()
      if (includePage) {
        p.set('p', String(state.page))
        p.set('page_size', String(state.pageSize))
      }
      state.selected.model_name.forEach(value => p.append('model_name', value))
      state.selected.username.forEach(value => p.append('username', value))
      const sessionId = sessionFilterEl.value.trim()
      const attribution = attributionFilterEl.value
      const turnStatus = turnStatusFilterEl.value
      const exported = exportedFilterEl.value
      if (sessionId) p.set('session_id', sessionId)
      if (attribution) p.set('attribution', attribution)
      if (turnStatus) p.set('status', turnStatus)
      if (exported) p.set('exported', exported)
      const startTimestamp = timestampFromLocalInput(state.time.start)
      const endTimestamp = timestampFromLocalInput(state.time.end)
      if (startTimestamp > 0) p.set('start_timestamp', String(startTimestamp))
      if (endTimestamp > 0) p.set('end_timestamp', String(endTimestamp))
      return p
    }
    async function api(path, options = {}) {
      const res = await fetch(path, options)
      const json = await res.json().catch(() => ({ success:false, message:res.statusText }))
      if (!json.success) throw new Error(localizedError(json.message) || 'request failed')
      return json.data
    }
    function closeMenus(except) {
      selectFields.forEach(field => {
        if (field !== except) {
          field.classList.remove('open')
          field.querySelector('.select-button').setAttribute('aria-expanded', 'false')
        }
      })
    }
    function labelFor(key) {
      return key === 'model_name' ? 'models' : 'users'
    }
    function updateSelectButton(key) {
      const field = document.querySelector('.filter-field[data-filter="' + key + '"]')
      const strong = field.querySelector('.select-button strong')
      const selected = Array.from(state.selected[key])
      if (selected.length === 0) {
        strong.textContent = key === 'model_name' ? 'All models' : 'All users'
      } else if (selected.length === 1) {
        strong.textContent = selected[0]
      } else {
        strong.textContent = String(selected.length) + ' ' + labelFor(key)
      }
      strong.title = strong.textContent
    }
    function renderSelect(key, values) {
      const field = document.querySelector('.filter-field[data-filter="' + key + '"]')
      const button = field.querySelector('.select-button')
      const menu = field.querySelector('.select-menu')
      menu.innerHTML = ''

      const actions = document.createElement('div')
      actions.className = 'select-actions'
      const all = document.createElement('button')
      all.type = 'button'
      all.textContent = 'Select all'
      const clear = document.createElement('button')
      clear.type = 'button'
      clear.textContent = 'Clear'
      actions.appendChild(all)
      actions.appendChild(clear)
      menu.appendChild(actions)

      const list = values && values.length ? values : []
      if (list.length === 0) {
        const empty = document.createElement('div')
        empty.className = 'empty'
        empty.textContent = 'No options'
        menu.appendChild(empty)
      }
      list.forEach(value => {
        const label = document.createElement('label')
        label.className = 'option'
        const input = document.createElement('input')
        input.type = 'checkbox'
        input.value = value
        const text = document.createElement('span')
        text.textContent = value
        text.title = value
        label.appendChild(input)
        label.appendChild(text)
        menu.appendChild(label)
        input.onchange = () => {
          if (input.checked) state.selected[key].add(value)
          else state.selected[key].delete(value)
          state.page = 1
          updateSelectButton(key)
          loadRows()
        }
      })

      button.onclick = event => {
        event.stopPropagation()
        const open = field.classList.contains('open')
        closeMenus(field)
        field.classList.toggle('open', !open)
        button.setAttribute('aria-expanded', String(!open))
      }
      menu.onclick = event => event.stopPropagation()
      all.onclick = () => {
        state.selected[key] = new Set(list)
        menu.querySelectorAll('input[type="checkbox"]').forEach(input => input.checked = true)
        state.page = 1
        updateSelectButton(key)
        loadRows()
      }
      clear.onclick = () => {
        state.selected[key].clear()
        menu.querySelectorAll('input[type="checkbox"]').forEach(input => input.checked = false)
        state.page = 1
        updateSelectButton(key)
        loadRows()
      }
      updateSelectButton(key)
    }
    async function loadFilterOptions() {
      const data = await api('/api/filter-options?limit=1000')
      renderSelect('model_name', data.model_names || [])
      renderSelect('username', data.usernames || [])
    }
    async function loadStatus() {
      const data = await api('/api/status')
      const dropped = data.queue_dropped_jobs ? ' · ' + String(data.queue_dropped_jobs) + ' queue drops' : ''
      statusEl.textContent = data.has_session_data === false ? 'Session data unavailable' : String(data.session_count || 0) + ' sessions · ' + (data.request_log_db_dialect || 'db') + dropped
    }
    function updatePager() {
      const totalPages = Math.max(1, Math.ceil((state.total || 0) / state.pageSize))
      if (state.page > totalPages) state.page = totalPages
      const start = state.total === 0 ? 0 : (state.page - 1) * state.pageSize + 1
      const end = Math.min(state.total, state.page * state.pageSize)
      pageInfoEl.textContent = 'Page ' + state.page + ' / ' + totalPages + ' · ' + start + '-' + end + ' of ' + state.total
      pageJumpEl.max = String(totalPages)
      if (document.activeElement !== pageJumpEl) pageJumpEl.value = String(state.page)
      prevPageEl.disabled = state.page <= 1
      nextPageEl.disabled = state.page >= totalPages
    }
    async function loadRows() {
      const data = await api('/api/sessions?' + qs())
      state.total = data.total || 0
      state.page = data.page || state.page
      state.pageSize = data.page_size || state.pageSize
      updatePager()
      if (!data.items || data.items.length === 0) {
        rowsEl.innerHTML = '<tr><td colspan="5"><div class="empty">No sessions.</div></td></tr>'
        return
      }
      rowsEl.innerHTML = data.items.map(row => [
        '<tr data-id="' + esc(row.id) + '" class="' + (row.id === selectedId ? 'selected' : '') + '">',
        '<td><div class="time">' + (row.completed_at ? new Date(row.completed_at * 1000).toLocaleString() : 'Open') + '</div></td>',
        '<td><div class="model">' + esc(row.session_id || 'unknown') + '</div><div class="subline">' + esc(row.request_count || 0) + ' requests · ' + esc(row.item_count || 0) + ' items</div></td>',
        '<td><div class="model">' + esc(row.model_name || '-') + '</div><div class="subline">' + esc(row.token_name || '') + '</div></td>',
        '<td><div>' + esc(row.username || '-') + '</div><div class="subline">' + esc(row.request_count || 0) + ' requests</div></td>',
        '<td><span class="pill ' + esc(row.completion_status || 'unknown') + '">' + esc(completionStatusLabel(row.completion_status)) + '</span><div class="subline">' + esc(attributionLabel(row.attribution)) + (row.exported ? ' · Exported' : '') + '</div></td>',
        '</tr>'
      ].join('')).join('')
      rowsEl.querySelectorAll('tr').forEach(row => row.onclick = () => loadDetail(Number(row.dataset.id)))
    }
    async function loadDetail(id) {
      const requestToken = ++detailRequestToken
      selectedId = id
      detailEl.innerHTML = detailLoadingHTML()
      try {
        await loadRows()
        const log = await api('/api/sessions/' + id)
        if (requestToken !== detailRequestToken) return
        let collapsedDeveloperCount = 0
        const itemHtml = (log.items || []).map(item => {
          const callId = callIdFor(item)
          const collapseDeveloper = String(item.role || '').toLowerCase() === 'developer' && String(item.item_type || '').toLowerCase() === 'message' && collapsedDeveloperCount++ < 2
          return [
            '<details class="item" data-phase="' + esc(item.phase || '') + '"' + (collapseDeveloper ? '' : ' open') + '><summary class="item-head">',
            '<div class="item-primary">',
            item.role ? '<span class="role-badge ' + roleClass(item.role) + '">' + esc(roleLabel(item.role)) + '</span>' : '',
            '<div class="item-title">' + esc(itemTypeLabel(item.item_type)) + '</div>',
            callId ? '<span class="call-id" title="' + esc(callId) + '"><span class="call-id-label">Call ID</span><code>' + esc(callId) + '</code></span>' : '',
            '</div>',
            '<div class="item-meta">',
            item.sequence !== undefined && item.sequence !== null && String(item.sequence).trim() !== '' ? '<span class="pill">Seq ' + esc(item.sequence) + '</span>' : '',
            item.phase ? '<span class="pill">' + esc(phaseLabel(item.phase)) + '</span>' : '',
            item.message_phase ? '<span class="pill ' + esc(item.message_phase) + '">' + esc(completionStatusLabel(item.message_phase)) + '</span>' : '',
            item.item_status ? '<span class="pill ' + esc(item.item_status) + '">' + esc(completionStatusLabel(item.item_status)) + '</span>' : '',
            item.name ? '<span class="item-context">' + esc(item.name) + '</span>' : '',
            item.source ? '<span class="item-context">' + esc(item.source) + '</span>' : '',
            '</div>',
            '<span class="collapse-state"><span class="state-expanded">Collapse</span><span class="state-collapsed">' + (collapseDeveloper ? 'Collapsed · click to expand' : 'Expand') + '</span><i class="item-chevron" aria-hidden="true"></i></span>',
            '</summary>',
            '<pre>' + esc(displayContentFor(item)) + '</pre>',
            '</details>'
          ].join('')
        }).join('') || emptyItemsHTML()
        detailEl.innerHTML = [
          '<div class="detail-head">',
          '<div class="detail-title"><h2>' + esc(log.model_name || '-') + '</h2><div class="request-id">' + esc(log.session_id || 'unknown') + '</div></div>',
          '<span class="pill ' + esc(log.completion_status || 'unknown') + '">' + esc(completionStatusLabel(log.completion_status)) + ' · ' + esc(attributionLabel(log.attribution)) + '</span>',
          '</div>',
          '<div class="summary">',
          '<div class="metric"><span>Items</span><strong>' + esc((log.items || []).length) + '</strong></div>',
          '<div class="metric"><span>Requests</span><strong>' + esc(log.request_count || 0) + '</strong></div>',
          '<div class="metric"><span>Tokens</span><strong>' + esc(log.token_used || 0) + '</strong></div>',
          '<div class="metric"><span>Quota</span><strong>' + esc(log.quota || 0) + '</strong></div>',
          '</div>',
          '<div class="items">' + itemHtml + '</div>'
        ].join('')
      } catch (err) {
        if (requestToken !== detailRequestToken) return
        detailEl.innerHTML = detailErrorHTML(err.message)
      }
    }
    async function loadExportPreview() {
      const p = qs(false)
      if (Array.from(p.keys()).length === 0) {
        createExportEl.disabled = true
        exportPreviewEl.innerHTML = '<div class="preview-empty"><strong>No export selection</strong><span>Choose at least one filter in Session data before creating an export.</span></div>'
        return
      }
      createExportEl.disabled = true
      p.set('include_inferred', String(includeInferredEl.checked))
      const data = await api('/api/export-preview?' + p)
      const available = Number(data.available_count || 0)
      let detail = 'Each selected session is exported as one immutable snapshot. Later content stays in a new unexported branch.'
      if (data.broken_count) {
        const reasons = []
        if (data.broken_time_count) reasons.push(String(data.broken_time_count) + ' invalid time or status')
        if (data.broken_request_count) reasons.push(String(data.broken_request_count) + ' request count mismatches')
        if (data.broken_item_count) reasons.push(String(data.broken_item_count) + ' item count mismatches')
        detail = String(data.broken_count) + ' potentially broken completed sessions were excluded' + (reasons.length ? ': ' + reasons.join(', ') : '') + '.'
      }
      exportPreviewEl.innerHTML = '<div class="preview-count"><strong>' + esc(available.toLocaleString()) + '</strong><span>sessions ready to export</span></div><div class="preview-detail">' + esc(detail) + '</div>'
      createExportEl.disabled = available === 0
    }
    const beijingTime = value => value ? new Date(value * 1000).toLocaleString('en-CA', { timeZone:'Asia/Shanghai', hour12:false }) + ' Beijing time' : ''
    function batchIntegrityLabel(batch) {
      if (batch.integrity_status === 'verified') return 'Verified'
      if (batch.integrity_status === 'broken') return 'Broken'
      return 'Pending audit'
    }
    function batchProgress(batch) {
      if (!['pending', 'building'].includes(batch.status)) return ''
      const total = Math.max(0, Number(batch.row_count || 0))
      const processed = Math.max(0, Math.min(total || Number.MAX_SAFE_INTEGER, Number(batch.processed_rows || 0)))
      const percent = total > 0 ? Math.min(100, Math.round(processed * 100 / total)) : 0
      const label = batch.status === 'pending'
        ? 'Queued · ' + String(total) + ' sessions'
        : 'Exporting ' + String(processed) + ' / ' + String(total) + ' sessions · ' + String(percent) + '%'
      return '<div class="batch-progress"><span>' + esc(label) + '</span><div class="batch-progress-track" aria-label="' + esc(label) + '"><i class="batch-progress-fill" style="width:' + String(percent) + '%"></i></div></div>'
    }
    function batchActions(batch) {
      const actions = []
      if (batch.status === 'completed') {
        if (!batch.artifact_deleted_at) actions.push('<a href="/api/export-batches/' + encodeURIComponent(batch.tag) + '/download"><button type="button">Download</button></a>')
        if (!batch.reset_at) actions.push('<button type="button" data-reset="' + esc(batch.tag) + '">Reset export state</button>')
        if (batch.reset_at && !batch.artifact_deleted_at) actions.push('<button type="button" data-delete-artifact="' + esc(batch.tag) + '">Delete export file</button>')
        if (batch.integrity_status === 'verified') {
          if (!batch.cleaned_at) actions.push('<button type="button" data-clean="' + esc(batch.tag) + '">Mark cleaned</button>')
        } else if (!batch.reset_at) {
          actions.push('<button type="button" data-audit="' + esc(batch.tag) + '" title="Recheck exported sessions, requests, and items for consistency">Audit</button>')
        }
        actions.push('<button type="button" data-delete-history="' + esc(batch.tag) + '">Delete history</button>')
      }
      if (batch.status === 'failed') {
        actions.push('<button type="button" data-retry="' + esc(batch.tag) + '">Retry</button>')
      }
      return actions.join('')
    }
    function renderBatchRow(batch) {
      return [
        '<div class="batch-row">',
        '<div><div class="batch-tag">' + esc(batch.tag) + '</div><div class="batch-meta">' + esc(batch.row_count || 0) + ' sessions · integrity: ' + esc(batchIntegrityLabel(batch)) + (batch.reset_at ? ' · reset ' + esc(beijingTime(batch.reset_at)) + ' (' + esc(batch.reset_rows || 0) + ' source records released)' : '') + (batch.artifact_deleted_at ? ' · export file deleted' : '') + (batch.cleaned_at ? ' · cleaned ' + esc(beijingTime(batch.cleaned_at)) : '') + (batch.integrity_error ? ' · ' + esc(localizedError(batch.integrity_error)) : '') + (batch.error ? ' · ' + esc(localizedError(batch.error)) : '') + '</div>' + batchProgress(batch) + '</div>',
        '<span class="pill ' + esc(batch.status || 'pending') + '">' + esc(batchStatusLabel(batch.status)) + '</span>',
        '<div class="batch-actions">' + batchActions(batch) + '</div>',
        '</div>'
      ].join('')
    }
    async function loadBatches() {
      const data = await api('/api/export-batches?p=1&page_size=1000')
      const batches = data.items || []
      const activeBatches = batches.filter(batch => batch.status !== 'completed')
      const historicalBatches = batches.filter(batch => batch.status === 'completed')
      const activeRowsHTML = activeBatches.length
        ? activeBatches.map(renderBatchRow).join('')
        : '<div class="export-empty">No active or failed export batches.</div>'
      const historyRowsHTML = historicalBatches.length
        ? historicalBatches.map(renderBatchRow).join('')
        : '<div class="export-empty">No export history yet.</div>'
      const activeHTML = '<section class="export-section"><div class="export-section-head"><div><div class="section-kicker">Activity</div><h3>Active exports</h3></div><span class="section-count">' + esc(activeBatches.length) + '</span></div><div class="active-export-list">' + activeRowsHTML + '</div></section>'
      const historyHTML = '<details class="history-exports"><summary><span><span class="section-kicker">Archive</span><strong>Export history</strong></span><span class="section-count">' + esc(historicalBatches.length) + '</span></summary><div class="history-export-list">' + historyRowsHTML + '</div></details>'
      batchListEl.innerHTML = activeHTML + historyHTML
      batchListEl.querySelectorAll('[data-retry]').forEach(button => {
        button.onclick = async () => {
          button.disabled = true
          try {
            await api('/api/export-batches/' + encodeURIComponent(button.dataset.retry) + '/retry', { method:'POST' })
            await loadBatches()
          } catch (err) {
            exportPreviewEl.textContent = err.message
          } finally {
            button.disabled = false
          }
        }
      })
      batchListEl.querySelectorAll('[data-audit]').forEach(button => {
        button.onclick = async () => {
          button.disabled = true
          try {
            const batch = await api('/api/export-batches/' + encodeURIComponent(button.dataset.audit) + '/audit', { method:'POST' })
            exportPreviewEl.textContent = batch.integrity_status === 'verified' ? 'Integrity audit passed.' : 'Integrity audit found an issue: ' + (localizedError(batch.integrity_error) || 'See batch details.')
            await loadBatches()
          } catch (err) {
            exportPreviewEl.textContent = err.message
          } finally {
            button.disabled = false
          }
        }
      })
      batchListEl.querySelectorAll('[data-clean]').forEach(button => {
        button.onclick = async () => {
          if (!window.confirm('Confirm downstream processing or cold backup is complete? The export batch can be deleted afterward.')) return
          button.disabled = true
          try {
            await api('/api/export-batches/' + encodeURIComponent(button.dataset.clean) + '/mark-cleaned', { method:'POST' })
            await loadBatches()
          } catch (err) {
            exportPreviewEl.textContent = err.message
          } finally {
            button.disabled = false
          }
        }
      })
      batchListEl.querySelectorAll('[data-reset]').forEach(button => {
        button.onclick = async () => {
          if (!window.confirm('Reset this export? The generated file will be deleted, history will be retained, and associated sessions will become unexported.')) return
          button.disabled = true
          exportPreviewEl.textContent = 'Resetting export batch...'
          try {
            await api('/api/export-batches/' + encodeURIComponent(button.dataset.reset) + '/reset', { method:'POST' })
            exportPreviewEl.textContent = 'Export batch reset. Refreshing the list...'
            await Promise.all([loadBatches(), loadRows()])
            exportPreviewEl.textContent = 'Export batch reset. Refreshing the available count...'
            loadExportPreview().catch(err => { exportPreviewEl.textContent = err.message })
          } catch (err) {
            exportPreviewEl.textContent = err.message
          } finally {
            button.disabled = false
          }
        }
      })
      batchListEl.querySelectorAll('[data-delete-artifact]').forEach(button => {
        button.onclick = async () => {
          if (!window.confirm('Delete the file for this reset export? History will be retained and the file will no longer be downloadable.')) return
          button.disabled = true
          try {
            await api('/api/export-batches/' + encodeURIComponent(button.dataset.deleteArtifact) + '/delete-artifact', { method:'POST' })
            await loadBatches()
          } catch (err) {
            exportPreviewEl.textContent = err.message
          } finally {
            button.disabled = false
          }
        }
      })
      batchListEl.querySelectorAll('[data-delete]').forEach(button => {
        button.onclick = async () => {
          if (!window.confirm('Delete this cleaned export file and batch record? Original sessions will remain exported.')) return
          button.disabled = true
          try {
            await api('/api/export-batches/' + encodeURIComponent(button.dataset.delete) + '/delete', { method:'POST' })
            await loadBatches()
          } catch (err) {
            exportPreviewEl.textContent = err.message
          } finally {
            button.disabled = false
          }
        }
      })
      batchListEl.querySelectorAll('[data-delete-history]').forEach(button => {
        button.onclick = async () => {
          if (!window.confirm('Delete this history record and any remaining export file? This cannot be undone and will not change the current session export state.')) return
          button.disabled = true
          exportPreviewEl.textContent = 'Deleting export history...'
          try {
            await api('/api/export-batches/' + encodeURIComponent(button.dataset.deleteHistory) + '/delete-history', { method:'POST' })
            await Promise.all([loadBatches(), loadRows()])
            exportPreviewEl.textContent = 'Export history deleted.'
            loadExportPreview().catch(err => { exportPreviewEl.textContent = err.message })
          } catch (err) {
            exportPreviewEl.textContent = err.message
          } finally {
            button.disabled = false
          }
        }
      })
      if (batches.some(batch => ['pending', 'building'].includes(batch.status))) {
        setTimeout(() => { if (!exportsViewEl.hidden) loadBatches().catch(() => {}) }, 1500)
      }
    }
    function setWorkspace(name) {
      const showingExports = name === 'exports'
      closeMenus()
      turnViewEl.hidden = showingExports
      exportsViewEl.hidden = !showingExports
      turnNavEl.classList.toggle('active', !showingExports)
      exportNavEl.classList.toggle('active', showingExports)
      turnNavEl.toggleAttribute('aria-current', !showingExports)
      exportNavEl.toggleAttribute('aria-current', showingExports)
      document.title = showingExports ? 'Exports · Session Log Viewer' : 'Session Log Viewer'
    }
    async function openExports() {
      setWorkspace('exports')
      exportPreviewEl.textContent = 'Loading preview...'
      await Promise.all([loadExportPreview(), loadBatches()])
    }
    document.addEventListener('click', () => closeMenus())
    document.getElementById('refresh').onclick = () => {
      loadStatus()
      if (exportsViewEl.hidden) {
        loadFilterOptions()
        loadRows()
      } else {
        loadExportPreview().catch(err => { exportPreviewEl.textContent = err.message })
        loadBatches().catch(err => { exportPreviewEl.textContent = err.message })
      }
    }
    turnNavEl.onclick = event => {
      event.preventDefault()
      setWorkspace('turns')
    }
    exportNavEl.onclick = event => {
      event.preventDefault()
      openExports().catch(err => exportPreviewEl.textContent = err.message)
    }
    includeInferredEl.onchange = () => loadExportPreview().catch(err => exportPreviewEl.textContent = err.message)
    createExportEl.onclick = async event => {
      const button = event.currentTarget
      const p = qs(false)
      if (Array.from(p.keys()).length === 0) {
        button.disabled = true
        exportPreviewEl.innerHTML = '<div class="preview-empty"><strong>No export selection</strong><span>Choose at least one filter in Session data before creating an export.</span></div>'
        return
      }
      button.disabled = true
      p.set('include_inferred', String(includeInferredEl.checked))
      try {
        const batch = await api('/api/export-batches?' + p, { method:'POST' })
        exportPreviewEl.textContent = 'Created export batch ' + batch.tag + ' with ' + String(batch.row_count || 0) + ' sessions.'
        await Promise.all([loadBatches(), loadRows(), loadExportPreview()])
      } catch (err) {
        exportPreviewEl.textContent = err.message
        button.disabled = false
      }
    }
    prevPageEl.onclick = () => {
      if (state.page <= 1) return
      state.page--
      loadRows()
    }
    nextPageEl.onclick = () => {
      const totalPages = Math.max(1, Math.ceil((state.total || 0) / state.pageSize))
      if (state.page >= totalPages) return
      state.page++
      loadRows()
    }
    function jumpToPage() {
      const totalPages = Math.max(1, Math.ceil((state.total || 0) / state.pageSize))
      const requested = Number.parseInt(pageJumpEl.value, 10)
      if (!Number.isInteger(requested) || requested < 1) {
        pageJumpEl.value = String(state.page)
        return
      }
      state.page = Math.min(requested, totalPages)
      loadRows()
    }
    jumpPageEl.onclick = jumpToPage
    pageJumpEl.onkeydown = event => {
      if (event.key === 'Enter') jumpToPage()
    }
    function applyTimeFilter() {
      state.time.start = startTimeEl.value
      state.time.end = endTimeEl.value
      state.page = 1
      updateAdvancedFilterCount()
      loadRows()
    }
    startTimeEl.onchange = applyTimeFilter
    endTimeEl.onchange = applyTimeFilter
    clearTimeEl.onclick = () => {
      startTimeEl.value = ''
      endTimeEl.value = ''
      applyTimeFilter()
      updateAdvancedFilterCount()
    }
    function updateAdvancedFilterCount() {
      const count = [attributionFilterEl.value, turnStatusFilterEl.value, exportedFilterEl.value, startTimeEl.value, endTimeEl.value].filter(Boolean).length
      advancedCountEl.textContent = String(count)
      advancedCountEl.hidden = count === 0
    }
    advancedToggleEl.onclick = () => {
      const expanded = advancedToggleEl.getAttribute('aria-expanded') === 'true'
      advancedToggleEl.setAttribute('aria-expanded', String(!expanded))
      advancedFiltersEl.hidden = expanded
    }
    clearFiltersEl.onclick = () => {
      state.selected.model_name.clear()
      state.selected.username.clear()
      state.time.start = ''
      state.time.end = ''
      state.page = 1
      startTimeEl.value = ''
      endTimeEl.value = ''
      sessionFilterEl.value = ''
      attributionFilterEl.value = ''
      turnStatusFilterEl.value = ''
      exportedFilterEl.value = ''
      selectFields.forEach(field => field.querySelectorAll('input[type="checkbox"]').forEach(input => { input.checked = false }))
      updateSelectButton('model_name')
      updateSelectButton('username')
      updateAdvancedFilterCount()
      loadRows()
    }
    let textFilterTimer = 0
    function applyTextFilters() {
      clearTimeout(textFilterTimer)
      textFilterTimer = setTimeout(() => {
        state.page = 1
        loadRows()
      }, 250)
    }
    sessionFilterEl.oninput = applyTextFilters
    attributionFilterEl.onchange = () => {
      state.page = 1
      updateAdvancedFilterCount()
      loadRows()
    }
    turnStatusFilterEl.onchange = () => {
      state.page = 1
      updateAdvancedFilterCount()
      loadRows()
    }
    exportedFilterEl.onchange = () => {
      state.page = 1
      updateAdvancedFilterCount()
      loadRows()
    }
    document.addEventListener('keydown', event => {
      const target = event.target
      const isTyping = target && ['INPUT', 'SELECT', 'TEXTAREA'].includes(target.tagName)
      const openField = selectFields.find(field => field.classList.contains('open'))
      if (event.key === 'Escape' && openField) {
        event.preventDefault()
        const button = openField.querySelector('.select-button')
        closeMenus()
        button.focus()
        return
      }
      if (event.key === '/' && !isTyping && !turnViewEl.hidden) {
        event.preventDefault()
        sessionFilterEl.focus()
      }
    })
    loadStatus().catch(err => statusEl.textContent = err.message)
    loadFilterOptions().catch(err => statusEl.textContent = err.message)
    updateAdvancedFilterCount()
    loadRows().catch(err => rowsEl.innerHTML = '<tr><td colspan="5">' + esc(err.message) + '</td></tr>')
  </script>
</body>
</html>`
