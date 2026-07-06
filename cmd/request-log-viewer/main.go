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
		Username:       q.Get("username"),
		TokenName:      q.Get("token_name"),
		StartIdx:       (page - 1) * pageSize,
		Num:            pageSize,
	}
	return params, page, pageSize
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
    :root { color-scheme: dark; --bg:#111412; --panel:#191d1a; --line:#30362f; --text:#eef2e8; --muted:#9ba395; --accent:#d9ff72; --warn:#ffc857; --bad:#ff6b6b; }
    * { box-sizing: border-box; }
    body { margin:0; background:var(--bg); color:var(--text); font:14px/1.45 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; letter-spacing:0; }
    header { position:sticky; top:0; z-index:10; display:flex; align-items:center; gap:18px; padding:14px 18px; border-bottom:1px solid var(--line); background:rgba(17,20,18,.95); }
    h1 { margin:0; font-size:16px; font-weight:700; }
    .status { color:var(--muted); font-size:12px; }
    main { display:grid; grid-template-columns:minmax(360px, 42%) 1fr; min-height:calc(100vh - 57px); }
    aside { border-right:1px solid var(--line); overflow:auto; }
    section { overflow:auto; }
    .filters { display:grid; grid-template-columns:repeat(3, 1fr); gap:8px; padding:12px; border-bottom:1px solid var(--line); }
    input, button { border:1px solid var(--line); background:var(--panel); color:var(--text); border-radius:6px; padding:8px 10px; font:inherit; min-width:0; }
    button { cursor:pointer; color:var(--accent); }
    button:hover { border-color:var(--accent); }
    table { width:100%; border-collapse:collapse; }
    th, td { padding:9px 10px; border-bottom:1px solid var(--line); text-align:left; vertical-align:top; }
    th { color:var(--muted); font-size:12px; font-weight:500; }
    tr { cursor:pointer; }
    tr:hover, tr.selected { background:#20251f; }
    .muted { color:var(--muted); }
    .pill { display:inline-flex; align-items:center; border:1px solid var(--line); border-radius:999px; padding:2px 7px; font-size:12px; color:var(--muted); }
    .detail { padding:16px; }
    .summary { display:grid; grid-template-columns:repeat(4, minmax(0,1fr)); gap:8px; margin-bottom:14px; }
    .metric { border:1px solid var(--line); border-radius:8px; padding:10px; background:var(--panel); }
    .metric span { display:block; color:var(--muted); font-size:12px; }
    .metric strong { display:block; margin-top:5px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
    .item { border:1px solid var(--line); border-radius:8px; margin:10px 0; background:var(--panel); }
    .item-head { display:flex; flex-wrap:wrap; gap:6px; align-items:center; padding:8px 10px; border-bottom:1px solid var(--line); }
    pre { margin:0; padding:10px; overflow:auto; max-height:360px; white-space:pre-wrap; word-break:break-word; font:12px/1.5 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
    .empty { padding:28px; color:var(--muted); }
    @media (max-width: 900px) { main { grid-template-columns:1fr; } aside { border-right:0; border-bottom:1px solid var(--line); } .summary { grid-template-columns:1fr 1fr; } }
  </style>
</head>
<body>
  <header>
    <h1>Request Log Viewer</h1>
    <div id="status" class="status">Loading...</div>
    <button id="refresh">Refresh</button>
    <button id="export">Export JSONL</button>
  </header>
  <main>
    <aside>
      <div class="filters">
        <input id="model" placeholder="model">
        <input id="username" placeholder="username">
        <input id="token" placeholder="token">
      </div>
      <table>
        <thead><tr><th>Time</th><th>Model</th><th>User</th><th>Status</th></tr></thead>
        <tbody id="rows"></tbody>
      </table>
    </aside>
    <section id="detail" class="detail"><div class="empty">Select a request log.</div></section>
  </main>
  <script>
    const rowsEl = document.getElementById('rows')
    const detailEl = document.getElementById('detail')
    const statusEl = document.getElementById('status')
    const filters = ['model','username','token'].map(id => document.getElementById(id))
    let selectedId = 0

    const esc = value => String(value ?? '').replace(/[&<>"']/g, s => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[s]))
    const pretty = value => { try { return JSON.stringify(JSON.parse(value), null, 2) } catch { return value || '' } }
    const qs = () => {
      const p = new URLSearchParams({ p:'1', page_size:'100' })
      if (filters[0].value) p.set('model_name', filters[0].value)
      if (filters[1].value) p.set('username', filters[1].value)
      if (filters[2].value) p.set('token_name', filters[2].value)
      return p
    }
    async function api(path) {
      const res = await fetch(path)
      const json = await res.json()
      if (!json.success) throw new Error(json.message || 'request failed')
      return json.data
    }
    async function loadStatus() {
      const data = await api('/api/status')
      statusEl.textContent = data.has_table ? String(data.count) + ' rows - ' + (data.request_log_db_dialect || 'db') : 'table missing'
    }
    async function loadRows() {
      const data = await api('/api/logs?' + qs())
      rowsEl.innerHTML = data.items.map(row => [
        '<tr data-id="' + esc(row.id) + '" class="' + (row.id === selectedId ? 'selected' : '') + '">',
        '<td>' + new Date((row.created_at || 0) * 1000).toLocaleString() + '</td>',
        '<td>' + esc(row.model_name || '-') + '<br><span class="muted">' + esc(row.token_name || '') + '</span></td>',
        '<td>' + esc(row.username || '-') + '</td>',
        '<td><span class="pill">' + esc(row.parse_status || 'ok') + '</span></td>',
        '</tr>'
      ].join('')).join('')
      rowsEl.querySelectorAll('tr').forEach(row => row.onclick = () => loadDetail(Number(row.dataset.id)))
    }
    async function loadDetail(id) {
      selectedId = id
      await loadRows()
      const log = await api('/api/logs/' + id)
      const itemHtml = (log.items || []).map(item => [
        '<article class="item">',
        '<div class="item-head">',
        '<span class="pill">' + esc(item.seq) + '</span>',
        '<span class="pill">' + esc(item.phase) + '</span>',
        '<span class="pill">' + esc(item.item_type) + '</span>',
        item.role ? '<span class="pill">' + esc(item.role) + '</span>' : '',
        item.name ? '<span class="muted">' + esc(item.name) + '</span>' : '',
        item.source ? '<span class="muted">' + esc(item.source) + '</span>' : '',
        '</div>',
        '<pre>' + esc(pretty(item.content)) + '</pre>',
        '</article>'
      ].join('')).join('') || '<div class="empty">No parsed training items.</div>'
      detailEl.innerHTML = [
        '<div class="summary">',
        '<div class="metric"><span>Model</span><strong>' + esc(log.model_name) + '</strong></div>',
        '<div class="metric"><span>Request ID</span><strong>' + esc(log.request_id || '-') + '</strong></div>',
        '<div class="metric"><span>Tokens</span><strong>' + esc(log.token_used || 0) + '</strong></div>',
        '<div class="metric"><span>Cost</span><strong>' + esc(log.quota || 0) + '</strong></div>',
        '</div>',
        itemHtml
      ].join('')
    }
    document.getElementById('refresh').onclick = () => { loadStatus(); loadRows() }
    document.getElementById('export').onclick = () => { location.href = '/api/export.jsonl?' + qs() }
    filters.forEach(input => input.addEventListener('keydown', e => { if (e.key === 'Enter') loadRows() }))
    loadStatus().catch(err => statusEl.textContent = err.message)
    loadRows().catch(err => rowsEl.innerHTML = '<tr><td colspan="4">' + esc(err.message) + '</td></tr>')
  </script>
</body>
</html>`
