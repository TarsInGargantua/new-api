package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

type requestLogViewerServer struct {
	db      *gorm.DB
	exports *requestLogExportWorker
}

func (s *requestLogViewerServer) serveTurns(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/turns" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	params, page, pageSize := turnQuery(r, 50)
	items, total, err := model.GetAPIRequestLogTurns(s.db, params)
	writeAPI(w, pageData{Items: items, Total: total, Page: page, PageSize: pageSize}, err)
}

func (s *requestLogViewerServer) serveTurnDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	idText := strings.TrimPrefix(r.URL.Path, "/api/turns/")
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid turn id")
		return
	}
	detail, err := model.GetAPIRequestLogTurnById(s.db, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeAPIError(w, http.StatusNotFound, "turn not found")
		return
	}
	writeAPI(w, detail, err)
}

func (s *requestLogViewerServer) serveExportPreview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/export-preview" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	params, _, _ := turnQuery(r, 50)
	preview, err := model.PreviewAPIRequestLogExport(s.db, params, queryBool(r.URL.Query().Get("include_inferred")))
	writeAPI(w, preview, err)
}

func (s *requestLogViewerServer) serveExportBatches(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/export-batches" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		page := queryInt(r.URL.Query().Get("p"), 1)
		pageSize := queryInt(r.URL.Query().Get("page_size"), 50)
		if pageSize > 1000 {
			pageSize = 1000
		}
		items, total, err := model.GetAPIRequestLogExportBatches(s.db, model.APIRequestLogExportBatchQueryParams{
			Statuses: queryList(r.URL.Query(), "status"),
			StartIdx: (page - 1) * pageSize,
			Num:      pageSize,
		})
		writeAPI(w, pageData{Items: items, Total: total, Page: page, PageSize: pageSize}, err)
	case http.MethodPost:
		params, _, _ := turnQuery(r, 50)
		batch, err := model.CreateAPIRequestLogExportBatch(s.db, params, queryBool(r.URL.Query().Get("include_inferred")))
		if err != nil {
			writeAPI(w, nil, err)
			return
		}
		s.exports.Enqueue(batch.Tag)
		writeJSON(w, http.StatusAccepted, apiResponse{Success: true, Data: batch})
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *requestLogViewerServer) serveExportBatchAction(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/export-batches/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
		http.NotFound(w, r)
		return
	}
	tag, action := parts[0], parts[1]
	switch action {
	case "download":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		s.serveExportDownload(w, r, tag)
	case "retry":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		batch, err := model.RetryAPIRequestLogExportBatchPending(s.db, tag)
		if err != nil {
			writeAPI(w, nil, err)
			return
		}
		s.exports.Enqueue(batch.Tag)
		writeJSON(w, http.StatusAccepted, apiResponse{Success: true, Data: batch})
	case "audit":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		batch, err := model.AuditAPIRequestLogExportBatch(s.db, tag)
		if err != nil {
			writeExportActionError(w, err)
			return
		}
		writeAPI(w, batch, nil)
	case "mark-cleaned":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		batch, err := model.MarkAPIRequestLogExportBatchCleaned(s.db, tag)
		if err != nil {
			writeExportActionError(w, err)
			return
		}
		writeAPI(w, batch, nil)
	case "reset":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		batch, err := model.ResetAPIRequestLogExportBatch(s.db, tag)
		if err != nil {
			writeExportActionError(w, err)
			return
		}
		writeAPI(w, batch, nil)
	case "delete":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		s.serveExportBatchDelete(w, tag)
	default:
		http.NotFound(w, r)
	}
}

func (s *requestLogViewerServer) serveExportBatchDelete(w http.ResponseWriter, tag string) {
	batch, err := model.GetAPIRequestLogExportBatchByTag(s.db, tag)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeAPIError(w, http.StatusNotFound, "export batch not found")
		return
	}
	if err != nil {
		writeAPI(w, nil, err)
		return
	}
	if batch.Status != model.APIRequestLogExportBatchStatusCompleted {
		writeAPIError(w, http.StatusConflict, "only completed export batches can be deleted")
		return
	}
	if batch.CleanedAt <= 0 {
		writeAPIError(w, http.StatusConflict, model.ErrAPIRequestLogExportBatchNotCleaned.Error())
		return
	}
	staged, err := s.exports.StageArtifactDeletion(batch)
	if err != nil {
		writeAPI(w, nil, err)
		return
	}
	deleted, err := model.DeleteAPIRequestLogExportBatch(s.db, tag)
	if err != nil {
		_ = staged.Restore()
		writeExportActionError(w, err)
		return
	}
	if err := staged.Finalize(); err != nil {
		// The database branch is already gone and the artifact is no longer
		// reachable from the viewer. Keep this as a successful delete while
		// surfacing the cleanup problem for an operator to remove the staged file.
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Message: "batch deleted; staged artifact cleanup failed: " + err.Error(), Data: deleted})
		return
	}
	writeAPI(w, deleted, nil)
}

func writeExportActionError(w http.ResponseWriter, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeAPIError(w, http.StatusNotFound, "export batch not found")
		return
	}
	if errors.Is(err, model.ErrAPIRequestLogExportBatchNotCleaned) || errors.Is(err, model.ErrAPIRequestLogExportBatchNotClaimable) || errors.Is(err, model.ErrAPIRequestLogExportBatchAlreadyReset) {
		writeAPIError(w, http.StatusConflict, err.Error())
		return
	}
	writeAPI(w, nil, err)
}

func (s *requestLogViewerServer) serveExportDownload(w http.ResponseWriter, r *http.Request, tag string) {
	batch, err := model.GetAPIRequestLogExportBatchByTag(s.db, tag)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeAPIError(w, http.StatusNotFound, "export batch not found")
		return
	}
	if err != nil {
		writeAPI(w, nil, err)
		return
	}
	if batch.Status != model.APIRequestLogExportBatchStatusCompleted {
		writeAPIError(w, http.StatusConflict, "export batch is not completed")
		return
	}
	path, err := s.exports.ArtifactPath(batch)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		writeAPIError(w, http.StatusNotFound, "export artifact is missing")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(batch.Tag+".jsonl")))
	w.Header().Set("ETag", `"`+batch.SHA256+`"`)
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
}

func turnQuery(r *http.Request, defaultPageSize int) (model.APIRequestLogTurnQueryParams, int, int) {
	q := r.URL.Query()
	page := queryInt(q.Get("p"), 1)
	pageSize := queryInt(q.Get("page_size"), defaultPageSize)
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	params := model.APIRequestLogTurnQueryParams{
		StartTimestamp:     queryInt64(q.Get("start_timestamp"), 0),
		EndTimestamp:       queryInt64(q.Get("end_timestamp"), 0),
		SessionId:          q.Get("session_id"),
		TurnId:             q.Get("turn_id"),
		Protocol:           q.Get("protocol"),
		Protocols:          queryList(q, "protocol"),
		ModelName:          q.Get("model_name"),
		ModelNames:         queryList(q, "model_name"),
		Username:           q.Get("username"),
		Usernames:          queryList(q, "username"),
		TokenName:          q.Get("token_name"),
		CompletionStatus:   q.Get("status"),
		CompletionStatuses: queryList(q, "status"),
		Attribution:        q.Get("attribution"),
		Attributions:       queryList(q, "attribution"),
		StartIdx:           (page - 1) * pageSize,
		Num:                pageSize,
	}
	if value := strings.TrimSpace(q.Get("exported")); value != "" {
		exported, err := strconv.ParseBool(value)
		if err == nil {
			params.Exported = &exported
		}
	}
	return params, page, pageSize
}

func queryBool(value string) bool {
	parsed, _ := strconv.ParseBool(strings.TrimSpace(value))
	return parsed
}

func writeMethodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
}
