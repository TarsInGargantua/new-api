package model

import (
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type APIRequestLogBody string

func (APIRequestLogBody) GormDataType() string {
	return "text"
}

func (APIRequestLogBody) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "mysql" {
		return "LONGTEXT"
	}
	return "TEXT"
}

type APIRequestLog struct {
	Id                    int                 `json:"id" gorm:"index:idx_api_request_logs_created_at_id,priority:1"`
	UsageLogId            int                 `json:"usage_log_id" gorm:"index;default:0"`
	UserId                int                 `json:"user_id" gorm:"index"`
	Username              string              `json:"username" gorm:"index;default:''"`
	TokenId               int                 `json:"token_id" gorm:"index;default:0"`
	TokenName             string              `json:"token_name" gorm:"index;default:''"`
	ModelName             string              `json:"model_name" gorm:"index;default:''"`
	CreatedAt             int64               `json:"created_at" gorm:"bigint;index:idx_api_request_logs_created_at_id,priority:2"`
	RequestId             string              `json:"request_id,omitempty" gorm:"type:varchar(64);index;default:''"`
	UpstreamRequestId     string              `json:"upstream_request_id,omitempty" gorm:"type:varchar(128);index;default:''"`
	Method                string              `json:"method" gorm:"type:varchar(16);default:''"`
	RequestPath           string              `json:"request_path" gorm:"index;default:''"`
	StatusCode            int                 `json:"status_code" gorm:"index;default:0"`
	IsStream              bool                `json:"is_stream"`
	ChannelId             int                 `json:"channel_id" gorm:"index;default:0"`
	Group                 string              `json:"group" gorm:"index;default:''"`
	RequestContentType    string              `json:"request_content_type" gorm:"default:''"`
	ResponseContentType   string              `json:"response_content_type" gorm:"default:''"`
	RequestSize           int64               `json:"request_size" gorm:"default:0"`
	ResponseSize          int64               `json:"response_size" gorm:"default:0"`
	RequestOmittedReason  string              `json:"request_omitted_reason,omitempty" gorm:"default:''"`
	ResponseOmittedReason string              `json:"response_omitted_reason,omitempty" gorm:"default:''"`
	Redacted              bool                `json:"redacted"`
	RequestBody           APIRequestLogBody   `json:"request_body,omitempty"`
	ResponseBody          APIRequestLogBody   `json:"response_body,omitempty"`
	Metadata              APIRequestLogBody   `json:"metadata,omitempty"`
	Usage                 *APIRequestLogUsage `json:"usage,omitempty" gorm:"-"`
}

type APIRequestLogListItem struct {
	Id                    int                 `json:"id"`
	UsageLogId            int                 `json:"usage_log_id"`
	UserId                int                 `json:"user_id"`
	Username              string              `json:"username"`
	TokenId               int                 `json:"token_id"`
	TokenName             string              `json:"token_name"`
	ModelName             string              `json:"model_name"`
	CreatedAt             int64               `json:"created_at"`
	RequestId             string              `json:"request_id,omitempty"`
	UpstreamRequestId     string              `json:"upstream_request_id,omitempty"`
	Method                string              `json:"method"`
	RequestPath           string              `json:"request_path"`
	StatusCode            int                 `json:"status_code"`
	IsStream              bool                `json:"is_stream"`
	ChannelId             int                 `json:"channel_id"`
	Group                 string              `json:"group"`
	RequestContentType    string              `json:"request_content_type"`
	ResponseContentType   string              `json:"response_content_type"`
	RequestSize           int64               `json:"request_size"`
	ResponseSize          int64               `json:"response_size"`
	RequestOmittedReason  string              `json:"request_omitted_reason,omitempty"`
	ResponseOmittedReason string              `json:"response_omitted_reason,omitempty"`
	Redacted              bool                `json:"redacted"`
	Usage                 *APIRequestLogUsage `json:"usage,omitempty" gorm:"-"`
}

type APIRequestLogQueryParams struct {
	StartTimestamp int64
	EndTimestamp   int64
	ModelName      string
	Username       string
	TokenName      string
	StartIdx       int
	Num            int
}

type APIRequestLogUsage struct {
	LogId            int    `json:"log_id"`
	CreatedAt        int64  `json:"created_at"`
	ModelName        string `json:"model_name"`
	TokenName        string `json:"token_name"`
	Quota            int    `json:"quota"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TokenUsed        int    `json:"token_used"`
	UseTime          int    `json:"use_time"`
	Content          string `json:"content,omitempty"`
	Other            string `json:"other,omitempty"`
}

type APIRequestLogStorageStatus struct {
	HasTable              bool   `json:"has_table"`
	Count                 int64  `json:"count"`
	LastCreatedAt         int64  `json:"last_created_at,omitempty"`
	LastRequestId         string `json:"last_request_id,omitempty"`
	LastWriteError        string `json:"last_write_error,omitempty"`
	LastWriteErrorAt      int64  `json:"last_write_error_at,omitempty"`
	LogDBDialect          string `json:"log_db_dialect,omitempty"`
	EnsureMigrationFailed bool   `json:"ensure_migration_failed"`
}

var apiRequestLogEnsureMu sync.Mutex
var apiRequestLogEnsuredDB *gorm.DB
var apiRequestLogEnsured bool
var apiRequestLogLastWriteError string
var apiRequestLogLastWriteErrorAt int64

const apiRequestLogListSyncMax = 200
const apiRequestLogCapturePending = "capture_pending"

type apiRequestLogHydratedBody struct {
	body        APIRequestLogBody
	contentType string
	size        int64
	redacted    bool
	source      string
}

func setAPIRequestLogLastWriteError(err error) {
	apiRequestLogEnsureMu.Lock()
	defer apiRequestLogEnsureMu.Unlock()
	if err == nil {
		apiRequestLogLastWriteError = ""
		apiRequestLogLastWriteErrorAt = 0
		return
	}
	apiRequestLogLastWriteError = err.Error()
	apiRequestLogLastWriteErrorAt = common.GetTimestamp()
}

func EnsureAPIRequestLogTable() error {
	if LOG_DB == nil {
		err := errors.New("log database is not initialized")
		setAPIRequestLogLastWriteError(err)
		return err
	}

	apiRequestLogEnsureMu.Lock()
	if apiRequestLogEnsured && apiRequestLogEnsuredDB == LOG_DB {
		apiRequestLogEnsureMu.Unlock()
		return nil
	}
	apiRequestLogEnsureMu.Unlock()

	if err := LOG_DB.AutoMigrate(&APIRequestLog{}); err != nil {
		setAPIRequestLogLastWriteError(err)
		return err
	}
	setAPIRequestLogLastWriteError(nil)

	apiRequestLogEnsureMu.Lock()
	apiRequestLogEnsuredDB = LOG_DB
	apiRequestLogEnsured = true
	apiRequestLogEnsureMu.Unlock()
	return nil
}

func CreateAPIRequestLog(log *APIRequestLog) error {
	if err := EnsureAPIRequestLogTable(); err != nil {
		return err
	}
	if log != nil && log.UsageLogId > 0 {
		err := createOrUpdateAPIRequestLogByUsageLogId(log)
		setAPIRequestLogLastWriteError(err)
		return err
	}
	err := LOG_DB.Create(log).Error
	setAPIRequestLogLastWriteError(err)
	return err
}

func createOrUpdateAPIRequestLogByUsageLogId(log *APIRequestLog) error {
	var existing APIRequestLog
	err := LOG_DB.Where("usage_log_id = ?", log.UsageLogId).Order("id asc").First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return LOG_DB.Create(log).Error
	}
	if err != nil {
		return err
	}
	log.Id = existing.Id
	return LOG_DB.Save(log).Error
}

func CreateAPIRequestLogFromConsumeLog(c *gin.Context, usageLog *Log) error {
	if !common.APIRequestLogEnabled || usageLog == nil || usageLog.Id <= 0 || usageLog.Type != LogTypeConsume {
		return nil
	}
	return CreateAPIRequestLog(apiRequestLogFromConsumeLog(c, usageLog))
}

func apiRequestLogFromConsumeLog(c *gin.Context, usageLog *Log) *APIRequestLog {
	log := &APIRequestLog{
		UsageLogId:            usageLog.Id,
		UserId:                usageLog.UserId,
		Username:              usageLog.Username,
		TokenId:               usageLog.TokenId,
		TokenName:             usageLog.TokenName,
		ModelName:             usageLog.ModelName,
		CreatedAt:             usageLog.CreatedAt,
		RequestId:             usageLog.RequestId,
		UpstreamRequestId:     usageLog.UpstreamRequestId,
		StatusCode:            200,
		IsStream:              usageLog.IsStream,
		ChannelId:             usageLog.ChannelId,
		Group:                 usageLog.Group,
		RequestOmittedReason:  apiRequestLogCapturePending,
		ResponseOmittedReason: apiRequestLogCapturePending,
	}
	if c != nil {
		if c.Request != nil {
			log.Method = c.Request.Method
			log.RequestContentType = c.Request.Header.Get("Content-Type")
			log.RequestSize = c.Request.ContentLength
			if c.Request.URL != nil {
				log.RequestPath = c.Request.URL.Path
			}
		}
		if c.Writer != nil {
			log.StatusCode = c.Writer.Status()
			log.ResponseContentType = c.Writer.Header().Get("Content-Type")
		}
		applyRequestBodyFromContext(c, log)
	}
	if log.StatusCode == 0 {
		log.StatusCode = 200
	}
	metadata := map[string]interface{}{
		"usage_log_id":                 usageLog.Id,
		"synced_from_usage_log":        true,
		"is_api_request_log_record":    true,
		"request_omitted_reason":       log.RequestOmittedReason,
		"response_omitted_reason":      log.ResponseOmittedReason,
		"request_log_sync_layer":       "model",
		"request_log_sync_created_at":  common.GetTimestamp(),
		"request_log_sync_usage_quota": usageLog.Quota,
	}
	if log.RequestOmittedReason == "" {
		metadata["request_body_source"] = "context_body_storage"
	}
	if c != nil {
		metadata["upstream_request_id"] = c.GetString(common.UpstreamRequestIdKey)
	}
	metadataJSON, _ := common.Marshal(metadata)
	log.Metadata = APIRequestLogBody(metadataJSON)
	return log
}

func applyRequestBodyFromContext(c *gin.Context, log *APIRequestLog) {
	if c == nil || c.Request == nil || log == nil {
		return
	}
	contentType := c.Request.Header.Get("Content-Type")
	log.RequestContentType = contentType
	if !common.IsAuditableContentType(contentType) {
		log.RequestSize = c.Request.ContentLength
		log.RequestOmittedReason = "non_text_content_type"
		return
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return
	}
	body, err := storage.Bytes()
	if err != nil {
		return
	}
	if len(body) == 0 && c.Request.ContentLength != 0 {
		return
	}
	text, redacted := common.AuditBodyToStringWithRedact(body, contentType, common.APIRequestLogRedactSecrets)
	log.RequestBody = APIRequestLogBody(text)
	log.RequestSize = int64(len(body))
	log.RequestOmittedReason = ""
	log.Redacted = redacted
}

func GetAPIRequestLogs(params APIRequestLogQueryParams) (logs []*APIRequestLogListItem, total int64, err error) {
	if err := EnsureAPIRequestLogTable(); err != nil {
		return nil, 0, err
	}
	if err := SyncMissingAPIRequestLogsFromUsageLogs(params, requestLogSyncLimit(params)); err != nil {
		return nil, 0, err
	}
	tx := LOG_DB.Model(&APIRequestLog{})
	tx = applyLogContainsFilter(tx, "api_request_logs.model_name", params.ModelName)
	tx = applyLogContainsFilter(tx, "api_request_logs.username", params.Username)
	tx = applyLogContainsFilter(tx, "api_request_logs.token_name", params.TokenName)
	if params.StartTimestamp != 0 {
		tx = tx.Where("api_request_logs.created_at >= ?", params.StartTimestamp)
	}
	if params.EndTimestamp != 0 {
		tx = tx.Where("api_request_logs.created_at <= ?", params.EndTimestamp)
	}

	if isUnfilteredAPIRequestLogQuery(params) {
		total, err = getAPIRequestLogFastTotal()
		if err != nil {
			return nil, 0, err
		}
	} else {
		if err = tx.Count(&total).Error; err != nil {
			return nil, 0, err
		}
	}

	err = tx.Select(
		"id, usage_log_id, user_id, username, token_id, token_name, model_name, created_at, request_id, upstream_request_id, method, request_path, status_code, is_stream, channel_id, " +
			logGroupCol + " as " + logGroupCol + ", request_content_type, response_content_type, request_size, response_size, request_omitted_reason, response_omitted_reason, redacted",
	).Order("id desc").Limit(params.Num).Offset(params.StartIdx).Find(&logs).Error
	if err != nil {
		return nil, 0, err
	}
	err = attachAPIRequestLogListUsage(logs)
	return logs, total, err
}

func isUnfilteredAPIRequestLogQuery(params APIRequestLogQueryParams) bool {
	return params.StartTimestamp == 0 &&
		params.EndTimestamp == 0 &&
		params.ModelName == "" &&
		params.Username == "" &&
		params.TokenName == ""
}

func getAPIRequestLogFastTotal() (int64, error) {
	var last APIRequestLog
	result := LOG_DB.Select("id").Order("id desc").Limit(1).Find(&last)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, nil
	}
	return int64(last.Id), nil
}

func requestLogSyncLimit(params APIRequestLogQueryParams) int {
	limit := params.StartIdx + params.Num
	if limit < params.Num {
		limit = params.Num
	}
	if limit < 20 {
		limit = 20
	}
	if limit > apiRequestLogListSyncMax {
		return apiRequestLogListSyncMax
	}
	return limit
}

func SyncMissingAPIRequestLogsFromUsageLogs(params APIRequestLogQueryParams, limit int) error {
	if !common.APIRequestLogEnabled {
		return nil
	}
	if err := EnsureAPIRequestLogTable(); err != nil {
		return err
	}
	if limit <= 0 {
		limit = 1000
	}
	tx := LOG_DB.Model(&Log{}).Where("type = ?", LogTypeConsume)
	tx = applyLogContainsFilter(tx, "logs.model_name", params.ModelName)
	tx = applyLogContainsFilter(tx, "logs.username", params.Username)
	tx = applyLogContainsFilter(tx, "logs.token_name", params.TokenName)
	if params.StartTimestamp != 0 {
		tx = tx.Where("logs.created_at >= ?", params.StartTimestamp)
	}
	if params.EndTimestamp != 0 {
		tx = tx.Where("logs.created_at <= ?", params.EndTimestamp)
	}

	var usageLogs []*Log
	if err := tx.Order("id desc").Limit(limit).Find(&usageLogs).Error; err != nil {
		return err
	}
	if len(usageLogs) == 0 {
		return nil
	}

	usageLogIds := make([]int, 0, len(usageLogs))
	for _, usageLog := range usageLogs {
		if usageLog != nil && usageLog.Id > 0 {
			usageLogIds = append(usageLogIds, usageLog.Id)
		}
	}
	if len(usageLogIds) == 0 {
		return nil
	}

	var existingLogs []*APIRequestLog
	if err := LOG_DB.Select("usage_log_id").Where("usage_log_id IN ?", usageLogIds).Find(&existingLogs).Error; err != nil {
		return err
	}
	existing := make(map[int]bool, len(existingLogs))
	for _, log := range existingLogs {
		if log != nil && log.UsageLogId > 0 {
			existing[log.UsageLogId] = true
		}
	}
	requestLogs := make([]*APIRequestLog, 0, len(usageLogs))
	for _, usageLog := range usageLogs {
		if usageLog == nil || usageLog.Id <= 0 || existing[usageLog.Id] {
			continue
		}
		requestLogs = append(requestLogs, apiRequestLogFromConsumeLog(nil, usageLog))
		existing[usageLog.Id] = true
	}
	if len(requestLogs) == 0 {
		return nil
	}
	err := LOG_DB.CreateInBatches(requestLogs, 100).Error
	setAPIRequestLogLastWriteError(err)
	return err
}

func GetAPIRequestLogById(id int) (*APIRequestLog, error) {
	var log APIRequestLog
	if err := LOG_DB.First(&log, id).Error; err != nil {
		return nil, err
	}
	usage, err := getAPIRequestLogUsage(log.UsageLogId, log.RequestId, log.UpstreamRequestId, true)
	if err != nil {
		return nil, err
	}
	log.Usage = usage
	if err := hydrateAPIRequestLogBodiesFromUsage(&log, usage); err != nil {
		return nil, err
	}
	return &log, nil
}

func hydrateAPIRequestLogBodiesFromUsage(log *APIRequestLog, usage *APIRequestLogUsage) error {
	if log == nil || usage == nil {
		return nil
	}

	other := apiRequestLogUsageOtherMap(usage)
	if len(other) == 0 {
		return nil
	}

	updates := make(map[string]interface{})
	metadata := apiRequestLogMetadataMap(log.Metadata)
	now := common.GetTimestamp()

	if shouldHydrateAPIRequestLogBody(log.RequestBody, log.RequestOmittedReason) {
		if body := apiRequestBodyFromUsageMetadata(other); body != nil {
			log.RequestBody = body.body
			log.RequestContentType = firstNonEmptyString(log.RequestContentType, body.contentType)
			log.RequestSize = body.size
			log.RequestOmittedReason = ""
			log.Redacted = log.Redacted || body.redacted
			updates["request_body"] = log.RequestBody
			updates["request_content_type"] = log.RequestContentType
			updates["request_size"] = log.RequestSize
			updates["request_omitted_reason"] = log.RequestOmittedReason
			updates["redacted"] = log.Redacted
			metadata["request_body_source"] = body.source
			metadata["request_body_hydrated_at"] = now
		}
	}

	if shouldHydrateAPIRequestLogBody(log.ResponseBody, log.ResponseOmittedReason) {
		if body := apiResponseBodyFromUsageMetadata(other); body != nil {
			log.ResponseBody = body.body
			log.ResponseContentType = firstNonEmptyString(log.ResponseContentType, body.contentType)
			log.ResponseSize = body.size
			log.ResponseOmittedReason = ""
			log.Redacted = log.Redacted || body.redacted
			updates["response_body"] = log.ResponseBody
			updates["response_content_type"] = log.ResponseContentType
			updates["response_size"] = log.ResponseSize
			updates["response_omitted_reason"] = log.ResponseOmittedReason
			updates["redacted"] = log.Redacted
			metadata["response_body_source"] = body.source
			metadata["response_body_hydrated_at"] = now
		}
	}

	if len(updates) == 0 {
		return nil
	}
	metadataJSON, err := common.Marshal(metadata)
	if err == nil {
		log.Metadata = APIRequestLogBody(metadataJSON)
		updates["metadata"] = log.Metadata
	}
	return LOG_DB.Model(&APIRequestLog{}).Where("id = ?", log.Id).Updates(updates).Error
}

func shouldHydrateAPIRequestLogBody(body APIRequestLogBody, omittedReason string) bool {
	return strings.TrimSpace(string(body)) == "" && omittedReason == apiRequestLogCapturePending
}

func apiRequestLogUsageOtherMap(usage *APIRequestLogUsage) map[string]interface{} {
	if usage == nil || strings.TrimSpace(usage.Other) == "" {
		return nil
	}
	var other map[string]interface{}
	if err := common.UnmarshalJsonStr(usage.Other, &other); err != nil {
		return nil
	}
	return other
}

func apiRequestLogMetadataMap(metadata APIRequestLogBody) map[string]interface{} {
	out := make(map[string]interface{})
	if strings.TrimSpace(string(metadata)) == "" {
		return out
	}
	if err := common.UnmarshalJsonStr(string(metadata), &out); err != nil {
		return make(map[string]interface{})
	}
	if out == nil {
		return make(map[string]interface{})
	}
	return out
}

func apiRequestBodyFromUsageMetadata(other map[string]interface{}) *apiRequestLogHydratedBody {
	messageCapture := mapFromInterface(other["message_capture"])
	if messageCapture != nil {
		if raw := hydratedBodyFromCapturedBodyMap(mapFromInterface(messageCapture["raw_request"]), "usage_log.message_capture.raw_request"); raw != nil {
			return raw
		}
	}
	if audit := mapFromInterface(other["audit_content"]); audit != nil {
		if raw := hydratedBodyFromCapturedBodyMap(mapFromInterface(audit["request"]), "usage_log.audit_content.request"); raw != nil {
			return raw
		}
	}
	if messageCapture != nil {
		return hydratedRequestBodyFromMessageCapture(messageCapture)
	}
	return nil
}

func apiResponseBodyFromUsageMetadata(other map[string]interface{}) *apiRequestLogHydratedBody {
	messageCapture := mapFromInterface(other["message_capture"])
	if messageCapture != nil {
		if raw := hydratedBodyFromCapturedBodyMap(mapFromInterface(messageCapture["raw_response"]), "usage_log.message_capture.raw_response"); raw != nil {
			return raw
		}
	}
	if audit := mapFromInterface(other["audit_content"]); audit != nil {
		if raw := hydratedBodyFromCapturedBodyMap(mapFromInterface(audit["response"]), "usage_log.audit_content.response"); raw != nil {
			return raw
		}
	}
	if messageCapture != nil {
		return hydratedResponseBodyFromMessageCapture(messageCapture)
	}
	return nil
}

func hydratedBodyFromCapturedBodyMap(bodyMap map[string]interface{}, source string) *apiRequestLogHydratedBody {
	if bodyMap == nil {
		return nil
	}
	body := common.Interface2String(bodyMap["body"])
	if strings.TrimSpace(body) == "" {
		return nil
	}
	size := int64(interfaceToInt(bodyMap["size"]))
	if size <= 0 {
		size = int64(len(body))
	}
	return &apiRequestLogHydratedBody{
		body:        APIRequestLogBody(body),
		contentType: common.Interface2String(bodyMap["content_type"]),
		size:        size,
		redacted:    interfaceToBool(bodyMap["redacted"]),
		source:      source,
	}
}

func hydratedRequestBodyFromMessageCapture(capture map[string]interface{}) *apiRequestLogHydratedBody {
	payload := make(map[string]interface{})
	copyIfPresent(payload, capture, "conversation_id")
	copyIfPresent(payload, capture, "question")
	copyIfPresent(payload, capture, "messages")
	copyIfPresent(payload, capture, "meta")
	if len(payload) == 0 {
		return nil
	}
	encoded, err := common.Marshal(payload)
	if err != nil {
		return nil
	}
	return &apiRequestLogHydratedBody{
		body:        APIRequestLogBody(encoded),
		contentType: "application/json",
		size:        int64(len(encoded)),
		source:      "usage_log.message_capture",
	}
}

func hydratedResponseBodyFromMessageCapture(capture map[string]interface{}) *apiRequestLogHydratedBody {
	payload := make(map[string]interface{})
	copyIfPresent(payload, capture, "conversation_id")
	copyIfPresent(payload, capture, "answer")
	copyIfPresent(payload, capture, "model_reasoning")
	copyIfPresent(payload, capture, "meta")
	if len(payload) == 0 {
		return nil
	}
	encoded, err := common.Marshal(payload)
	if err != nil {
		return nil
	}
	return &apiRequestLogHydratedBody{
		body:        APIRequestLogBody(encoded),
		contentType: "application/json",
		size:        int64(len(encoded)),
		source:      "usage_log.message_capture",
	}
}

func copyIfPresent(dst map[string]interface{}, src map[string]interface{}, key string) {
	if dst == nil || src == nil {
		return
	}
	value, exists := src[key]
	if !exists || value == nil {
		return
	}
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			dst[key] = v
		}
	case []interface{}:
		if len(v) > 0 {
			dst[key] = v
		}
	case map[string]interface{}:
		if len(v) > 0 {
			dst[key] = v
		}
	default:
		dst[key] = value
	}
}

func mapFromInterface(value interface{}) map[string]interface{} {
	if value == nil {
		return nil
	}
	if m, ok := value.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func interfaceToInt(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case int32:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	case string:
		i, err := strconv.Atoi(v)
		if err == nil {
			return i
		}
		return 0
	default:
		return 0
	}
}

func interfaceToBool(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func GetAPIRequestLogUsage(requestId string, upstreamRequestId string, includeBody bool) (*APIRequestLogUsage, error) {
	return getAPIRequestLogUsage(0, requestId, upstreamRequestId, includeBody)
}

func getAPIRequestLogUsage(usageLogId int, requestId string, upstreamRequestId string, includeBody bool) (*APIRequestLogUsage, error) {
	if usageLogId > 0 {
		var log Log
		err := LOG_DB.Where("type = ?", LogTypeConsume).First(&log, usageLogId).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return apiRequestUsageFromLog(&log, includeBody), nil
	}
	if requestId == "" && upstreamRequestId == "" {
		return nil, nil
	}

	var logs []*Log
	tx := LOG_DB.Model(&Log{}).Where("type = ?", LogTypeConsume)
	if requestId != "" && upstreamRequestId != "" {
		tx = tx.Where("(request_id = ? OR upstream_request_id = ?)", requestId, upstreamRequestId)
	} else if requestId != "" {
		tx = tx.Where("request_id = ?", requestId)
	} else {
		tx = tx.Where("upstream_request_id = ?", upstreamRequestId)
	}
	if err := tx.Order("id desc").Limit(1).Find(&logs).Error; err != nil {
		return nil, err
	}
	if len(logs) == 0 {
		return nil, nil
	}
	return apiRequestUsageFromLog(logs[0], includeBody), nil
}

func attachAPIRequestLogListUsage(logs []*APIRequestLogListItem) error {
	if len(logs) == 0 {
		return nil
	}
	usageLogIds := make([]int, 0, len(logs))
	requestIds := make([]string, 0, len(logs))
	upstreamRequestIds := make([]string, 0, len(logs))
	for _, item := range logs {
		if item == nil {
			continue
		}
		if item.UsageLogId > 0 {
			usageLogIds = append(usageLogIds, item.UsageLogId)
			continue
		}
		if item.RequestId != "" {
			requestIds = append(requestIds, item.RequestId)
		}
		if item.UpstreamRequestId != "" {
			upstreamRequestIds = append(upstreamRequestIds, item.UpstreamRequestId)
		}
	}
	if len(usageLogIds) == 0 && len(requestIds) == 0 && len(upstreamRequestIds) == 0 {
		return nil
	}

	var usageLogs []*Log
	if len(usageLogIds) > 0 {
		var logsById []*Log
		if err := LOG_DB.Model(&Log{}).Where("type = ? AND id IN ?", LogTypeConsume, usageLogIds).Find(&logsById).Error; err != nil {
			return err
		}
		usageLogs = append(usageLogs, logsById...)
	}
	if len(requestIds) > 0 || len(upstreamRequestIds) > 0 {
		var logsByRequest []*Log
		tx := LOG_DB.Model(&Log{}).Where("type = ?", LogTypeConsume)
		if len(requestIds) > 0 && len(upstreamRequestIds) > 0 {
			tx = tx.Where("(request_id IN ? OR upstream_request_id IN ?)", requestIds, upstreamRequestIds)
		} else if len(requestIds) > 0 {
			tx = tx.Where("request_id IN ?", requestIds)
		} else {
			tx = tx.Where("upstream_request_id IN ?", upstreamRequestIds)
		}
		if err := tx.Order("id desc").Find(&logsByRequest).Error; err != nil {
			return err
		}
		usageLogs = append(usageLogs, logsByRequest...)
	}

	byLogId := make(map[int]*APIRequestLogUsage)
	byRequestId := make(map[string]*APIRequestLogUsage)
	byUpstreamRequestId := make(map[string]*APIRequestLogUsage)
	for _, usageLog := range usageLogs {
		usage := apiRequestUsageFromLog(usageLog, false)
		if usageLog.Id > 0 {
			byLogId[usageLog.Id] = usage
		}
		if usageLog.RequestId != "" {
			if _, exists := byRequestId[usageLog.RequestId]; !exists {
				byRequestId[usageLog.RequestId] = usage
			}
		}
		if usageLog.UpstreamRequestId != "" {
			if _, exists := byUpstreamRequestId[usageLog.UpstreamRequestId]; !exists {
				byUpstreamRequestId[usageLog.UpstreamRequestId] = usage
			}
		}
	}

	for _, item := range logs {
		if item == nil {
			continue
		}
		if item.UsageLogId > 0 {
			item.Usage = byLogId[item.UsageLogId]
			continue
		}
		if item.RequestId != "" {
			if usage, exists := byRequestId[item.RequestId]; exists {
				item.Usage = usage
				continue
			}
		}
		if item.UpstreamRequestId != "" {
			item.Usage = byUpstreamRequestId[item.UpstreamRequestId]
		}
	}

	return nil
}

func apiRequestUsageFromLog(log *Log, includeBody bool) *APIRequestLogUsage {
	if log == nil {
		return nil
	}
	usage := &APIRequestLogUsage{
		LogId:            log.Id,
		CreatedAt:        log.CreatedAt,
		ModelName:        log.ModelName,
		TokenName:        log.TokenName,
		Quota:            log.Quota,
		PromptTokens:     log.PromptTokens,
		CompletionTokens: log.CompletionTokens,
		TokenUsed:        log.PromptTokens + log.CompletionTokens,
		UseTime:          log.UseTime,
	}
	if includeBody {
		usage.Content = log.Content
		usage.Other = log.Other
	}
	return usage
}

func GetAPIRequestLogStorageStatus() (*APIRequestLogStorageStatus, error) {
	status := &APIRequestLogStorageStatus{}
	apiRequestLogEnsureMu.Lock()
	status.LastWriteError = apiRequestLogLastWriteError
	status.LastWriteErrorAt = apiRequestLogLastWriteErrorAt
	apiRequestLogEnsureMu.Unlock()

	if LOG_DB == nil {
		status.LastWriteError = "log database is not initialized"
		status.EnsureMigrationFailed = true
		return status, nil
	}
	if LOG_DB.Dialector != nil {
		status.LogDBDialect = LOG_DB.Dialector.Name()
	}

	status.HasTable = LOG_DB.Migrator().HasTable(&APIRequestLog{})
	if !status.HasTable {
		if err := EnsureAPIRequestLogTable(); err != nil {
			status.EnsureMigrationFailed = true
			status.LastWriteError = err.Error()
			status.LastWriteErrorAt = common.GetTimestamp()
			return status, nil
		}
		status.HasTable = LOG_DB.Migrator().HasTable(&APIRequestLog{})
	}
	if !status.HasTable {
		return status, nil
	}

	count, err := getAPIRequestLogFastTotal()
	if err != nil {
		return status, err
	}
	status.Count = count

	var last APIRequestLog
	result := LOG_DB.Select("created_at, request_id").Order("id desc").Limit(1).Find(&last)
	if result.Error != nil {
		return status, result.Error
	}
	if result.RowsAffected == 0 {
		return status, nil
	}
	status.LastCreatedAt = last.CreatedAt
	status.LastRequestId = last.RequestId
	return status, nil
}
