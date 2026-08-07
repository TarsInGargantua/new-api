package main

import (
	"crypto/subtle"
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
	username := flag.String("username", firstNonEmpty(os.Getenv("REQUEST_LOG_VIEWER_USERNAME"), "admin"), "basic auth username")
	password := flag.String("password", os.Getenv("REQUEST_LOG_VIEWER_PASSWORD"), "basic auth password")
	dsn := flag.String("dsn", firstNonEmpty(os.Getenv("REQUEST_LOG_VIEWER_SQL_DSN"), os.Getenv("REQUEST_LOG_SQL_DSN")), "request log database DSN")
	exportDir := flag.String("export-dir", firstNonEmpty(os.Getenv("REQUEST_LOG_VIEWER_EXPORT_DIR"), "exports"), "persistent JSONL export directory")
	flag.Parse()

	if strings.TrimSpace(*password) == "" {
		fmt.Fprintln(os.Stderr, "REQUEST_LOG_VIEWER_PASSWORD is required")
		os.Exit(1)
	}
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
	exportWorker, err := newRequestLogExportWorker(model.REQUEST_LOG_DB, *exportDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init export worker: %v\n", err)
		os.Exit(1)
	}
	if err := exportWorker.Recover(); err != nil {
		fmt.Fprintf(os.Stderr, "recover export batches: %v\n", err)
		os.Exit(1)
	}
	server := &requestLogViewerServer{db: model.REQUEST_LOG_DB, exports: exportWorker}

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveIndex)
	mux.HandleFunc("/api/status", serveStatus)
	mux.HandleFunc("/api/filter-options", serveFilterOptions)
	mux.HandleFunc("/api/logs", serveLegacyRequestLogs)
	mux.HandleFunc("/api/logs/", serveLegacyRequestLogs)
	mux.HandleFunc("/api/turns", server.serveTurns)
	mux.HandleFunc("/api/turns/", server.serveTurnDetail)
	mux.HandleFunc("/api/export-preview", server.serveExportPreview)
	mux.HandleFunc("/api/export-batches", server.serveExportBatches)
	mux.HandleFunc("/api/export-batches/", server.serveExportBatchAction)
	mux.HandleFunc("/api/export.jsonl", serveJSONL)

	fmt.Printf("request-log-viewer listening on %s\n", *addr)
	handler := basicAuth(mux, *username, *password)
	if err := http.ListenAndServe(*addr, handler); err != nil {
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
		HasTurnTable        bool   `json:"has_turn_table"`
		TurnCount           int64  `json:"turn_count"`
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
	result.HasTurnTable = model.REQUEST_LOG_DB.Migrator().HasTable(&model.APIRequestLogTurn{})
	var err error
	if result.HasTurnTable {
		err = model.REQUEST_LOG_DB.Model(&model.APIRequestLogTurn{}).Count(&result.TurnCount).Error
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
	writeAPIError(w, http.StatusGone, "request-level views are disabled; use /api/turns")
}

func serveJSONL(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, http.StatusGone, "request-level export is disabled; create a turn export batch")
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

func basicAuth(next http.Handler, username string, password string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(gotUser), []byte(username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(gotPass), []byte(password)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="request-log-viewer"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
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
  <title>Request Log Viewer</title>
  <style>
    :root {
      color-scheme: dark;
      --bg:#10110f;
      --panel:#171913;
      --panel-2:#1d1f18;
      --panel-3:#23251c;
      --line:#34362b;
      --line-soft:#27291f;
      --text:#ece7d9;
      --muted:#9c988b;
      --faint:#706d63;
      --accent:#c3a35b;
      --accent-dim:#806b3f;
      --good:#8ea36f;
      --warn:#c88945;
      --bad:#c46a5a;
      --role-user:#7fc8f1;
      --role-user-bg:rgba(77,157,203,.12);
      --role-system:#c8a7e8;
      --role-system-bg:rgba(161,111,204,.13);
      --role-assistant:#e8bd68;
      --role-assistant-bg:rgba(195,163,91,.13);
      --role-developer:#e69a9a;
      --role-developer-bg:rgba(196,106,90,.13);
      --role-tool:#7fc6a4;
      --role-tool-bg:rgba(92,164,126,.13);
      --code:#0c0d0b;
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
      grid-template-columns:1fr auto auto;
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
    main {
      position:relative;
      display:grid;
      grid-template-columns:minmax(560px, 52%) 1fr;
      min-height:0;
      overflow:hidden;
    }
    aside, section { min-height:0; overflow:hidden; }
    aside {
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
      background:#13140f;
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
      background:#141510;
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
      background:#12130f;
    }
    tr { cursor:pointer; }
    tbody tr { transition:background .12s ease; }
    tbody tr:hover { background:#1b1d17; }
    tbody tr.selected { background:#24261d; box-shadow:inset 3px 0 0 var(--accent); }
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
      background:#141510;
      white-space:nowrap;
    }
    .pill.ok { color:var(--good); border-color:rgba(142,163,111,.42); }
    .pill.partial { color:var(--warn); border-color:rgba(200,137,69,.5); }
    .pill.failed { color:var(--bad); border-color:rgba(196,106,90,.5); }
    .pill.completed, .pill.exact { color:var(--good); border-color:rgba(142,163,111,.42); }
    .pill.open, .pill.inferred, .pill.building, .pill.pending { color:var(--warn); border-color:rgba(200,137,69,.5); }
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
    .item-head {
      display:flex;
      flex-wrap:wrap;
      gap:10px;
      align-items:center;
      padding:10px 12px;
      border-bottom:1px solid var(--line-soft);
      background:#141610;
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
    .role-assistant { color:var(--role-assistant); border-color:rgba(232,189,104,.4); background:var(--role-assistant-bg); }
    .role-developer { color:var(--role-developer); border-color:rgba(230,154,154,.4); background:var(--role-developer-bg); }
    .role-tool { color:var(--role-tool); border-color:rgba(127,198,164,.38); background:var(--role-tool-bg); }
    .call-id {
      display:inline-flex;
      align-items:baseline;
      gap:7px;
      min-width:0;
      max-width:100%;
      border:1px solid rgba(195,163,91,.35);
      border-radius:5px;
      padding:3px 7px;
      background:rgba(195,163,91,.075);
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
      color:#dcc784;
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
      color:#d8d1bf;
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
      aside { border-right:0; border-bottom:1px solid var(--line); max-height:46vh; }
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
  </style>
</head>
<body>
  <header>
    <div class="brand">
      <div class="mark"></div>
      <h1>Turn Log Viewer</h1>
    </div>
    <div id="status" class="status">Loading...</div>
    <div class="actions">
      <button id="refresh">Refresh</button>
      <button id="export">Exports</button>
    </div>
  </header>
  <main>
    <aside>
      <div class="filters">
        <div class="filter-field" data-filter="model_name">
          <button class="select-button" id="modelFilter" type="button"><strong>All models</strong><span>Model</span></button>
          <div class="select-menu" id="modelMenu"></div>
        </div>
        <div class="filter-field" data-filter="username">
          <button class="select-button" id="userFilter" type="button"><strong>All users</strong><span>User</span></button>
          <div class="select-menu" id="userMenu"></div>
        </div>
        <input id="sessionFilter" type="search" placeholder="Session ID">
        <input id="turnFilter" type="search" placeholder="Turn ID">
        <select id="attributionFilter" aria-label="Attribution">
          <option value="">All confidence</option>
          <option value="exact">Exact</option>
          <option value="inferred">Inferred</option>
          <option value="unknown">Unknown</option>
        </select>
        <select id="turnStatusFilter" aria-label="Turn status">
          <option value="">All states</option>
          <option value="completed">Completed</option>
          <option value="open">Open</option>
          <option value="unknown">Unknown</option>
        </select>
        <select id="exportedFilter" aria-label="Export status">
          <option value="">All export states</option>
          <option value="false">Not exported</option>
          <option value="true">Exported</option>
        </select>
        <div class="time-range">
          <label class="time-field" for="startTime"><span>Start</span><input id="startTime" type="datetime-local"></label>
          <label class="time-field" for="endTime"><span>End</span><input id="endTime" type="datetime-local"></label>
          <button id="clearTime" class="clear-time" type="button">Clear time</button>
        </div>
      </div>
      <div class="list-scroll">
        <table>
          <thead><tr><th>Ended</th><th>Session / turn</th><th>Model</th><th>User</th><th>State</th></tr></thead>
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
    <section id="detail" class="detail"><div class="empty">Select a turn.</div></section>
  </main>
  <dialog id="exportDialog">
    <div class="export-head"><h2>Export batches</h2><button id="closeExport" class="icon-button" type="button" title="Close" aria-label="Close">&times;</button></div>
    <div class="export-body">
      <div class="export-controls">
        <label class="toggle"><input id="includeInferred" type="checkbox">Include inferred completed turns</label>
        <button id="createExport" type="button">Create batch</button>
      </div>
      <div id="exportPreview" class="export-preview">Loading preview...</div>
      <div id="batchList" class="batch-list"></div>
    </div>
  </dialog>
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
    const sessionFilterEl = document.getElementById('sessionFilter')
    const turnFilterEl = document.getElementById('turnFilter')
    const attributionFilterEl = document.getElementById('attributionFilter')
    const turnStatusFilterEl = document.getElementById('turnStatusFilter')
    const exportedFilterEl = document.getElementById('exportedFilter')
    const exportDialogEl = document.getElementById('exportDialog')
    const includeInferredEl = document.getElementById('includeInferred')
    const exportPreviewEl = document.getElementById('exportPreview')
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
    const detailLoadingHTML = () => [
      '<div class="loading-card" aria-live="polite">',
      '<div class="loading-copy"><span class="loader"></span><span>Loading turn</span></div>',
      '<div class="loading-lines"><i></i><i></i><i></i></div>',
      '</div>'
    ].join('')
    const detailErrorHTML = message => '<div class="empty">Failed to load turn: ' + esc(message || 'unknown error') + '</div>'
    const emptyItemsHTML = () => '<div class="empty">No current-turn items.</div>'
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
      const turnId = turnFilterEl.value.trim()
      const attribution = attributionFilterEl.value
      const turnStatus = turnStatusFilterEl.value
      const exported = exportedFilterEl.value
      if (sessionId) p.set('session_id', sessionId)
      if (turnId) p.set('turn_id', turnId)
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
      if (!json.success) throw new Error(json.message || 'request failed')
      return json.data
    }
    function closeMenus(except) {
      selectFields.forEach(field => {
        if (field !== except) field.classList.remove('open')
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
      const count = data.turn_count ?? data.count ?? 0
      const dropped = data.queue_dropped_jobs ? ' · ' + String(data.queue_dropped_jobs) + ' queue drops' : ''
      statusEl.textContent = data.has_turn_table === false ? 'turn table missing' : String(count) + ' turns - ' + (data.request_log_db_dialect || 'db') + dropped
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
      const data = await api('/api/turns?' + qs())
      state.total = data.total || 0
      state.page = data.page || state.page
      state.pageSize = data.page_size || state.pageSize
      updatePager()
      if (!data.items || data.items.length === 0) {
        rowsEl.innerHTML = '<tr><td colspan="5"><div class="empty">No turns.</div></td></tr>'
        return
      }
      rowsEl.innerHTML = data.items.map(row => [
        '<tr data-id="' + esc(row.id) + '" class="' + (row.id === selectedId ? 'selected' : '') + '">',
        '<td><div class="time">' + (row.completed_at ? new Date(row.completed_at * 1000).toLocaleString() : 'Open') + '</div></td>',
        '<td><div class="model">' + esc(row.session_id || 'unknown') + '</div><div class="subline">#' + esc(row.turn_index || '-') + ' · ' + esc(row.turn_id || '-') + '</div></td>',
        '<td><div class="model">' + esc(row.model_name || '-') + '</div><div class="subline">' + esc(row.token_name || '') + '</div></td>',
        '<td><div>' + esc(row.username || '-') + '</div><div class="subline">' + esc(row.request_count || 0) + ' requests</div></td>',
        '<td><span class="pill ' + esc(row.completion_status || 'unknown') + '">' + esc(row.completion_status || 'unknown') + '</span><div class="subline">' + esc(row.attribution || 'unknown') + (row.exported ? ' · exported' : '') + '</div></td>',
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
        const log = await api('/api/turns/' + id)
        if (requestToken !== detailRequestToken) return
        const itemHtml = (log.items || []).map(item => {
          const callId = callIdFor(item)
          return [
            '<article class="item" data-phase="' + esc(item.phase || '') + '">',
            '<div class="item-head">',
            '<div class="item-primary">',
            item.role ? '<span class="role-badge ' + roleClass(item.role) + '">' + esc(item.role) + '</span>' : '',
            '<div class="item-title">' + esc(item.item_type || 'item') + '</div>',
            callId ? '<span class="call-id" title="' + esc(callId) + '"><span class="call-id-label">call_id</span><code>' + esc(callId) + '</code></span>' : '',
            '</div>',
            '<div class="item-meta">',
            '<span class="pill">' + esc(item.seq) + '</span>',
            '<span class="pill">' + esc(item.phase) + '</span>',
            item.message_phase ? '<span class="pill ' + esc(item.message_phase) + '">' + esc(item.message_phase) + '</span>' : '',
            item.item_status ? '<span class="pill ' + esc(item.item_status) + '">' + esc(item.item_status) + '</span>' : '',
            item.name ? '<span class="item-context">' + esc(item.name) + '</span>' : '',
            item.source ? '<span class="item-context">' + esc(item.source) + '</span>' : '',
            '</div>',
            '</div>',
            '<pre>' + esc(displayContentFor(item)) + '</pre>',
            '</article>'
          ].join('')
        }).join('') || emptyItemsHTML()
        detailEl.innerHTML = [
          '<div class="detail-head">',
          '<div class="detail-title"><h2>' + esc(log.model_name || '-') + '</h2><div class="request-id">' + esc(log.session_id || 'unknown') + ' / ' + esc(log.turn_id || '-') + '</div></div>',
          '<span class="pill ' + esc(log.completion_status || 'unknown') + '">' + esc(log.completion_status || 'unknown') + ' · ' + esc(log.attribution || 'unknown') + '</span>',
          '</div>',
          '<div class="summary">',
          '<div class="metric"><span>Items</span><strong>' + esc((log.items || []).length) + '</strong></div>',
          '<div class="metric"><span>Requests</span><strong>' + esc(log.request_count || 0) + '</strong></div>',
          '<div class="metric"><span>Tokens</span><strong>' + esc(log.token_used || 0) + '</strong></div>',
          '<div class="metric"><span>Cost</span><strong>' + esc(log.quota || 0) + '</strong></div>',
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
      p.set('include_inferred', String(includeInferredEl.checked))
      const data = await api('/api/export-preview?' + p)
      const fragments = [String(data.available_count || 0) + ' verified, unexported turns match the current filters.']
      if (data.broken_count) {
        const reasons = []
        if (data.broken_time_count) reasons.push(String(data.broken_time_count) + ' invalid time/status')
        if (data.broken_request_count) reasons.push(String(data.broken_request_count) + ' request mismatch')
        if (data.broken_item_count) reasons.push(String(data.broken_item_count) + ' item mismatch')
        fragments.push(String(data.broken_count) + ' potentially broken completed turns are excluded' + (reasons.length ? ' (' + reasons.join(', ') + ')' : '') + '.')
      }
      exportPreviewEl.textContent = fragments.join(' ')
    }
    const beijingTime = value => value ? new Date(value * 1000).toLocaleString('zh-CN', { timeZone:'Asia/Shanghai', hour12:false }) + ' 北京时间' : ''
    function batchIntegrityLabel(batch) {
      if (batch.integrity_status === 'verified') return 'verified'
      if (batch.integrity_status === 'broken') return 'broken'
      return 'unverified'
    }
    function batchProgress(batch) {
      if (!['pending', 'building'].includes(batch.status)) return ''
      const total = Math.max(0, Number(batch.row_count || 0))
      const processed = Math.max(0, Math.min(total || Number.MAX_SAFE_INTEGER, Number(batch.processed_rows || 0)))
      const percent = total > 0 ? Math.min(100, Math.round(processed * 100 / total)) : 0
      const label = batch.status === 'pending'
        ? 'Queued · ' + String(total) + ' turns'
        : 'Exporting ' + String(processed) + ' / ' + String(total) + ' turns · ' + String(percent) + '%'
      return '<div class="batch-progress"><span>' + esc(label) + '</span><div class="batch-progress-track" aria-label="' + esc(label) + '"><i class="batch-progress-fill" style="width:' + String(percent) + '%"></i></div></div>'
    }
    function batchActions(batch) {
      const actions = []
      if (batch.status === 'completed') {
        actions.push('<a href="/api/export-batches/' + encodeURIComponent(batch.tag) + '/download"><button type="button">Download</button></a>')
        if (batch.integrity_status === 'verified') {
          if (batch.cleaned_at) actions.push('<button type="button" data-delete="' + esc(batch.tag) + '">Delete</button>')
          else actions.push('<button type="button" data-clean="' + esc(batch.tag) + '">Mark cleaned</button>')
        } else {
          actions.push('<button type="button" data-audit="' + esc(batch.tag) + '">Audit</button>')
        }
      }
      if (batch.status === 'failed') {
        actions.push('<button type="button" data-retry="' + esc(batch.tag) + '">Retry</button>')
      }
      return actions.join('')
    }
    async function loadBatches() {
      const data = await api('/api/export-batches?p=1&page_size=50')
      const batches = data.items || []
      batchListEl.innerHTML = batches.map(batch => [
        '<div class="batch-row">',
        '<div><div class="batch-tag">' + esc(batch.tag) + '</div><div class="batch-meta">' + esc(batch.row_count || 0) + ' turns · integrity: ' + esc(batchIntegrityLabel(batch)) + (batch.cleaned_at ? ' · cleaned ' + esc(beijingTime(batch.cleaned_at)) : '') + (batch.integrity_error ? ' · ' + esc(batch.integrity_error) : '') + (batch.error ? ' · ' + esc(batch.error) : '') + '</div>' + batchProgress(batch) + '</div>',
        '<span class="pill ' + esc(batch.status || 'pending') + '">' + esc(batch.status || 'pending') + '</span>',
        '<div>' + batchActions(batch) + '</div>',
        '</div>'
      ].join('')).join('') || '<div class="empty">No export batches.</div>'
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
            exportPreviewEl.textContent = batch.integrity_status === 'verified' ? 'Audit passed.' : 'Audit found broken data: ' + (batch.integrity_error || 'see batch details')
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
          if (!window.confirm('Confirm that downstream processing or cold backup is complete. This enables deletion of this export batch.')) return
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
      batchListEl.querySelectorAll('[data-delete]').forEach(button => {
        button.onclick = async () => {
          if (!window.confirm('Delete this cleaned export JSONL and its batch branch? Original turns remain marked exported and will not be exported again.')) return
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
      if (batches.some(batch => ['pending', 'building'].includes(batch.status))) {
        setTimeout(() => { if (exportDialogEl.open) loadBatches().catch(() => {}) }, 1500)
      }
    }
    async function openExports() {
      exportDialogEl.showModal()
      exportPreviewEl.textContent = 'Loading preview...'
      await Promise.all([loadExportPreview(), loadBatches()])
    }
    document.addEventListener('click', () => closeMenus())
    document.getElementById('refresh').onclick = () => { loadStatus(); loadFilterOptions(); loadRows() }
    document.getElementById('export').onclick = () => openExports().catch(err => exportPreviewEl.textContent = err.message)
    document.getElementById('closeExport').onclick = () => exportDialogEl.close()
    includeInferredEl.onchange = () => loadExportPreview().catch(err => exportPreviewEl.textContent = err.message)
    document.getElementById('createExport').onclick = async event => {
      const button = event.currentTarget
      button.disabled = true
      const p = qs(false)
      p.set('include_inferred', String(includeInferredEl.checked))
      try {
        const batch = await api('/api/export-batches?' + p, { method:'POST' })
        exportPreviewEl.textContent = 'Created ' + batch.tag + ' with ' + String(batch.row_count || 0) + ' turns.'
        await Promise.all([loadBatches(), loadRows()])
      } catch (err) {
        exportPreviewEl.textContent = err.message
      } finally {
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
      loadRows()
    }
    startTimeEl.onchange = applyTimeFilter
    endTimeEl.onchange = applyTimeFilter
    clearTimeEl.onclick = () => {
      startTimeEl.value = ''
      endTimeEl.value = ''
      applyTimeFilter()
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
    turnFilterEl.oninput = applyTextFilters
    attributionFilterEl.onchange = () => {
      state.page = 1
      loadRows()
    }
    turnStatusFilterEl.onchange = () => {
      state.page = 1
      loadRows()
    }
    exportedFilterEl.onchange = () => {
      state.page = 1
      loadRows()
    }
    loadStatus().catch(err => statusEl.textContent = err.message)
    loadFilterOptions().catch(err => statusEl.textContent = err.message)
    loadRows().catch(err => rowsEl.innerHTML = '<tr><td colspan="5">' + esc(err.message) + '</td></tr>')
  </script>
</body>
</html>`
