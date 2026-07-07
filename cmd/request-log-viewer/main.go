package main

import (
	"bufio"
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

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveIndex)
	mux.HandleFunc("/api/status", serveStatus)
	mux.HandleFunc("/api/filter-options", serveFilterOptions)
	mux.HandleFunc("/api/logs", serveLogs)
	mux.HandleFunc("/api/logs/", serveLogDetail)
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
	status, err := model.GetAPIRequestLogStorageStatus()
	writeAPI(w, status, err)
}

func serveFilterOptions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/filter-options" {
		http.NotFound(w, r)
		return
	}
	options, err := model.GetAPIRequestLogFilterOptions(queryInt(r.URL.Query().Get("limit"), 500))
	writeAPI(w, options, err)
}

func serveLogs(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/logs" {
		http.NotFound(w, r)
		return
	}
	params, page, pageSize := requestQuery(r, 100)
	items, total, err := model.GetAPIRequestLogs(params)
	writeAPI(w, pageData{Items: items, Total: total, Page: page, PageSize: pageSize}, err)
}

func serveLogDetail(w http.ResponseWriter, r *http.Request) {
	idText := strings.TrimPrefix(r.URL.Path, "/api/logs/")
	id, _ := strconv.Atoi(idText)
	if id <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid id")
		return
	}
	detail, err := model.GetAPIRequestLogById(id)
	writeAPI(w, detail, err)
}

func serveJSONL(w http.ResponseWriter, r *http.Request) {
	params, _, _ := requestQuery(r, 1000)
	if params.Num > 5000 {
		params.Num = 5000
	}
	includeEncrypted := strings.EqualFold(r.URL.Query().Get("include_encrypted"), "true")
	items, _, err := model.GetAPIRequestLogs(params)
	if err != nil {
		writeAPI(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=request-log-training.jsonl")
	writer := bufio.NewWriter(w)
	defer writer.Flush()
	for _, item := range items {
		detail, err := model.GetAPIRequestLogById(item.Id)
		if err != nil {
			continue
		}
		line := trainingJSONLRecord(detail, includeEncrypted)
		encoded, err := common.Marshal(line)
		if err != nil {
			continue
		}
		_, _ = writer.Write(encoded)
		_, _ = writer.WriteString("\n")
	}
}

func requestQuery(r *http.Request, defaultPageSize int) (model.APIRequestLogQueryParams, int, int) {
	q := r.URL.Query()
	page := queryInt(q.Get("p"), 1)
	pageSize := queryInt(q.Get("page_size"), defaultPageSize)
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	params := model.APIRequestLogQueryParams{
		StartTimestamp: queryInt64(q.Get("start_timestamp"), 0),
		EndTimestamp:   queryInt64(q.Get("end_timestamp"), 0),
		ModelName:      q.Get("model_name"),
		ModelNames:     queryList(q, "model_name"),
		Username:       q.Get("username"),
		Usernames:      queryList(q, "username"),
		TokenName:      q.Get("token_name"),
		StartIdx:       (page - 1) * pageSize,
		Num:            pageSize,
	}
	return params, page, pageSize
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

func trainingJSONLRecord(log *model.APIRequestLog, includeEncrypted bool) map[string]interface{} {
	out := map[string]interface{}{
		"id":                log.Id,
		"request_id":        log.RequestId,
		"model":             log.ModelName,
		"created_at":        log.CreatedAt,
		"parse_status":      log.ParseStatus,
		"schema_version":    log.SchemaVersion,
		"training_items":    []map[string]interface{}{},
		"prompt_tokens":     log.PromptTokens,
		"completion_tokens": log.CompletionTokens,
	}
	items := make([]map[string]interface{}, 0, len(log.Items))
	for _, item := range log.Items {
		if item.ContentType == "encrypted" && !includeEncrypted {
			continue
		}
		items = append(items, map[string]interface{}{
			"seq":          item.Seq,
			"phase":        item.Phase,
			"type":         item.ItemType,
			"role":         item.Role,
			"content_type": item.ContentType,
			"content":      string(item.Content),
			"tool_call_id": item.ToolCallId,
			"name":         item.Name,
			"source":       item.Source,
		})
	}
	out["training_items"] = items
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
      grid-template-columns:minmax(430px, 42%) 1fr;
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
    .pager button:disabled {
      cursor:not-allowed;
      color:var(--faint);
      border-color:var(--line-soft);
      background:#141510;
    }
    input, button {
      min-width:0;
      border:1px solid var(--line);
      background:var(--panel);
      color:var(--text);
      border-radius:6px;
      padding:8px 10px;
      font:inherit;
      outline:none;
    }
    input { color:var(--text); }
    input::placeholder { color:var(--faint); }
    input:focus { border-color:var(--accent-dim); background:var(--panel-2); }
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
    .item[data-phase="input"] { border-left:3px solid var(--good); }
    .item[data-phase="output"] { border-left:3px solid var(--accent); }
    .item-head {
      display:flex;
      flex-wrap:wrap;
      gap:7px;
      align-items:center;
      padding:9px 10px;
      border-bottom:1px solid var(--line-soft);
      background:#141610;
    }
    .item-title { margin-right:auto; color:var(--text); font-weight:700; font-size:12px; }
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
    }
  </style>
</head>
<body>
  <header>
    <div class="brand">
      <div class="mark"></div>
      <h1>Request Log Viewer</h1>
    </div>
    <div id="status" class="status">Loading...</div>
    <div class="actions">
      <button id="refresh">Refresh</button>
      <button id="export">JSONL</button>
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
        <div class="time-range">
          <label class="time-field" for="startTime"><span>Start</span><input id="startTime" type="datetime-local"></label>
          <label class="time-field" for="endTime"><span>End</span><input id="endTime" type="datetime-local"></label>
          <button id="clearTime" class="clear-time" type="button">Clear time</button>
        </div>
      </div>
      <div class="list-scroll">
        <table>
          <thead><tr><th>Time</th><th>Model</th><th>User</th><th>Status</th></tr></thead>
          <tbody id="rows"></tbody>
        </table>
      </div>
      <div class="pager">
        <div id="pageInfo" class="pager-info">Page 1</div>
        <div class="pager-controls">
          <button id="prevPage" type="button">Prev</button>
          <button id="nextPage" type="button">Next</button>
        </div>
      </div>
    </aside>
    <section id="detail" class="detail"><div class="empty">Select a request log.</div></section>
  </main>
  <script>
    const rowsEl = document.getElementById('rows')
    const detailEl = document.getElementById('detail')
    const statusEl = document.getElementById('status')
    const pageInfoEl = document.getElementById('pageInfo')
    const prevPageEl = document.getElementById('prevPage')
    const nextPageEl = document.getElementById('nextPage')
    const startTimeEl = document.getElementById('startTime')
    const endTimeEl = document.getElementById('endTime')
    const clearTimeEl = document.getElementById('clearTime')
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
    const detailLoadingHTML = () => [
      '<div class="loading-card" aria-live="polite">',
      '<div class="loading-copy"><span class="loader"></span><span>Loading request</span></div>',
      '<div class="loading-lines"><i></i><i></i><i></i></div>',
      '</div>'
    ].join('')
    const detailErrorHTML = message => '<div class="empty">Failed to load request: ' + esc(message || 'unknown error') + '</div>'
    const effectiveItemsStatus = log => {
      if (log.items_status === 'pending' && (log.items || []).length) return 'ok'
      return log.items_status || ''
    }
    const emptyItemsHTML = log => {
      const status = effectiveItemsStatus(log)
      if (status === 'pending') return '<div class="empty">Items are still being written.</div>'
      if (status === 'failed') return '<div class="empty">Items write failed: ' + esc(log.items_error || 'unknown error') + '</div>'
      return '<div class="empty">No parsed items.</div>'
    }
    const timestampFromLocalInput = value => {
      if (!value) return 0
      const date = new Date(value)
      if (Number.isNaN(date.getTime())) return 0
      return Math.floor(date.getTime() / 1000)
    }
    const qs = () => {
      const p = new URLSearchParams({ p:String(state.page), page_size:String(state.pageSize) })
      state.selected.model_name.forEach(value => p.append('model_name', value))
      state.selected.username.forEach(value => p.append('username', value))
      const startTimestamp = timestampFromLocalInput(state.time.start)
      const endTimestamp = timestampFromLocalInput(state.time.end)
      if (startTimestamp > 0) p.set('start_timestamp', String(startTimestamp))
      if (endTimestamp > 0) p.set('end_timestamp', String(endTimestamp))
      return p
    }
    async function api(path) {
      const res = await fetch(path)
      const json = await res.json()
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
      statusEl.textContent = data.has_table ? String(data.count) + ' rows - ' + (data.request_log_db_dialect || 'db') : 'table missing'
    }
    function updatePager() {
      const totalPages = Math.max(1, Math.ceil((state.total || 0) / state.pageSize))
      if (state.page > totalPages) state.page = totalPages
      const start = state.total === 0 ? 0 : (state.page - 1) * state.pageSize + 1
      const end = Math.min(state.total, state.page * state.pageSize)
      pageInfoEl.textContent = 'Page ' + state.page + ' / ' + totalPages + ' · ' + start + '-' + end + ' of ' + state.total
      prevPageEl.disabled = state.page <= 1
      nextPageEl.disabled = state.page >= totalPages
    }
    async function loadRows() {
      const data = await api('/api/logs?' + qs())
      state.total = data.total || 0
      state.page = data.page || state.page
      state.pageSize = data.page_size || state.pageSize
      updatePager()
      if (!data.items || data.items.length === 0) {
        rowsEl.innerHTML = '<tr><td colspan="4"><div class="empty">No rows.</div></td></tr>'
        return
      }
      rowsEl.innerHTML = data.items.map(row => [
        '<tr data-id="' + esc(row.id) + '" class="' + (row.id === selectedId ? 'selected' : '') + '">',
        '<td><div class="time">' + new Date((row.created_at || 0) * 1000).toLocaleString() + '</div></td>',
        '<td><div class="model">' + esc(row.model_name || '-') + '</div><div class="subline">' + esc(row.token_name || '') + '</div></td>',
        '<td><div>' + esc(row.username || '-') + '</div><div class="subline">' + esc(row.group || '') + '</div></td>',
        '<td><span class="pill ' + esc(row.parse_status || 'ok') + '">' + esc(row.parse_status || 'ok') + '</span></td>',
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
        const log = await api('/api/logs/' + id)
        if (requestToken !== detailRequestToken) return
        const itemHtml = (log.items || []).map(item => [
          '<article class="item" data-phase="' + esc(item.phase || '') + '">',
          '<div class="item-head">',
          '<div class="item-title">' + esc(item.item_type || 'item') + '</div>',
          '<span class="pill">' + esc(item.seq) + '</span>',
          '<span class="pill">' + esc(item.phase) + '</span>',
          item.role ? '<span class="pill">' + esc(item.role) + '</span>' : '',
          item.name ? '<span class="muted">' + esc(item.name) + '</span>' : '',
          item.source ? '<span class="muted">' + esc(item.source) + '</span>' : '',
          '</div>',
          '<pre>' + esc(pretty(item.content)) + '</pre>',
          '</article>'
        ].join('')).join('') || emptyItemsHTML(log)
        detailEl.innerHTML = [
          '<div class="detail-head">',
          '<div class="detail-title"><h2>' + esc(log.model_name || '-') + '</h2><div class="request-id">' + esc(log.request_id || '-') + '</div></div>',
          '<span class="pill ' + esc(log.parse_status || 'ok') + '">' + esc(log.parse_status || 'ok') + '</span>',
          '</div>',
          '<div class="summary">',
          '<div class="metric"><span>Items</span><strong>' + esc((log.items || []).length) + '</strong></div>',
          '<div class="metric"><span>Item status</span><strong>' + esc(effectiveItemsStatus(log) || '-') + '</strong></div>',
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
    document.addEventListener('click', () => closeMenus())
    document.getElementById('refresh').onclick = () => { loadStatus(); loadFilterOptions(); loadRows() }
    document.getElementById('export').onclick = () => { location.href = '/api/export.jsonl?' + qs() }
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
    loadStatus().catch(err => statusEl.textContent = err.message)
    loadFilterOptions().catch(err => statusEl.textContent = err.message)
    loadRows().catch(err => rowsEl.innerHTML = '<tr><td colspan="4">' + esc(err.message) + '</td></tr>')
  </script>
</body>
</html>`
