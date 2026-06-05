package service

import (
	"bytes"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const apiRequestLogWriterKey = "api_request_log_writer"
const apiRequestLogRecordedKey = "api_request_log_recorded"
const apiRequestLogRecordedUsageIdsKey = "api_request_log_recorded_usage_ids"
const APIRequestLogOriginalPathKey = "api_request_log_original_path"

type apiRequestLogWriter struct {
	gin.ResponseWriter
	mu   sync.Mutex
	buf  bytes.Buffer
	seen int64
}

func (w *apiRequestLogWriter) Write(data []byte) (int, error) {
	w.capture(data)
	return w.ResponseWriter.Write(data)
}

func (w *apiRequestLogWriter) WriteString(data string) (int, error) {
	w.capture(common.StringToByteSlice(data))
	return w.ResponseWriter.WriteString(data)
}

func (w *apiRequestLogWriter) capture(data []byte) {
	if len(data) == 0 || !common.APIRequestLogCaptureResponse {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen += int64(len(data))
	_, _ = w.buf.Write(data)
}

func (w *apiRequestLogWriter) snapshot() ([]byte, int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	data := make([]byte, w.buf.Len())
	copy(data, w.buf.Bytes())
	return data, w.seen
}

type apiRequestLogBody struct {
	body          string
	contentType   string
	size          int64
	omittedReason string
	redacted      bool
}

func StartAPIRequestLogCapture(c *gin.Context) {
	if c == nil || c.Writer == nil || !common.APIRequestLogEnabled {
		return
	}
	if _, exists := c.Get(apiRequestLogWriterKey); exists {
		return
	}
	writer := &apiRequestLogWriter{ResponseWriter: c.Writer}
	c.Writer = writer
	c.Set(apiRequestLogWriterKey, writer)
}

func RecordAPIRequestLog(c *gin.Context, relayInfo *relaycommon.RelayInfo, relayErr *types.NewAPIError) {
	recordAPIRequestLog(c, relayInfo, relayErr, nil)
}

func recordAPIRequestLog(c *gin.Context, relayInfo *relaycommon.RelayInfo, relayErr *types.NewAPIError, usageLog *model.Log) {
	if c == nil || c.Request == nil || !common.APIRequestLogEnabled {
		return
	}
	if usageLog == nil {
		if recorded, exists := c.Get(apiRequestLogRecordedKey); exists && recorded == true {
			return
		}
	} else if isAPIRequestLogUsageRecorded(c, usageLog.Id) {
		return
	}

	requestLog := buildRequestLogBody(c)
	responseLog := buildResponseLogBody(c)
	metadata := buildAPIRequestLogMetadata(c, relayInfo, relayErr, requestLog, responseLog)
	if usageLog != nil {
		metadata["usage_log_id"] = usageLog.Id
		metadata["usage_log_created_at"] = usageLog.CreatedAt
	}
	metadataJSON, _ := common.Marshal(metadata)

	log := &model.APIRequestLog{
		UserId:                c.GetInt("id"),
		Username:              c.GetString("username"),
		TokenId:               c.GetInt("token_id"),
		TokenName:             c.GetString("token_name"),
		ModelName:             firstNonEmpty(c.GetString("original_model"), relayModelName(relayInfo)),
		CreatedAt:             common.GetTimestamp(),
		RequestId:             c.GetString(common.RequestIdKey),
		UpstreamRequestId:     c.GetString(common.UpstreamRequestIdKey),
		Method:                c.Request.Method,
		RequestPath:           requestPath(c, relayInfo),
		StatusCode:            c.Writer.Status(),
		IsStream:              common.GetContextKeyBool(c, constant.ContextKeyIsStream) || (relayInfo != nil && relayInfo.IsStream),
		ChannelId:             c.GetInt("channel_id"),
		Group:                 firstNonEmpty(c.GetString("group"), relayUsingGroup(relayInfo)),
		RequestContentType:    requestLog.contentType,
		ResponseContentType:   responseLog.contentType,
		RequestSize:           requestLog.size,
		ResponseSize:          responseLog.size,
		RequestOmittedReason:  requestLog.omittedReason,
		ResponseOmittedReason: responseLog.omittedReason,
		Redacted:              requestLog.redacted || responseLog.redacted,
		RequestBody:           model.APIRequestLogBody(requestLog.body),
		ResponseBody:          model.APIRequestLogBody(responseLog.body),
		Metadata:              model.APIRequestLogBody(metadataJSON),
	}
	applyUsageLogToAPIRequestLog(log, usageLog)
	if log.StatusCode == 0 {
		log.StatusCode = 200
	}
	if err := model.CreateAPIRequestLog(log); err != nil {
		logger.LogError(c, "failed to record api request log: "+err.Error())
		return
	}
	if usageLog != nil {
		markAPIRequestLogUsageRecorded(c, usageLog.Id)
	}
	c.Set(apiRequestLogRecordedKey, true)
}

func RecordAPIRequestLogForConsume(c *gin.Context, relayInfo *relaycommon.RelayInfo, usageLog *model.Log) {
	if c == nil || usageLog == nil || usageLog.Id <= 0 || !common.APIRequestLogEnabled {
		return
	}
	recordAPIRequestLog(c, relayInfo, nil, usageLog)
}

func isAPIRequestLogUsageRecorded(c *gin.Context, usageLogId int) bool {
	if c == nil || usageLogId <= 0 {
		return false
	}
	raw, exists := c.Get(apiRequestLogRecordedUsageIdsKey)
	if !exists {
		return false
	}
	recorded, ok := raw.(map[int]bool)
	if !ok {
		return false
	}
	return recorded[usageLogId]
}

func markAPIRequestLogUsageRecorded(c *gin.Context, usageLogId int) {
	if c == nil || usageLogId <= 0 {
		return
	}
	raw, _ := c.Get(apiRequestLogRecordedUsageIdsKey)
	recorded, _ := raw.(map[int]bool)
	if recorded == nil {
		recorded = make(map[int]bool)
		c.Set(apiRequestLogRecordedUsageIdsKey, recorded)
	}
	recorded[usageLogId] = true
}

func buildRequestLogBody(c *gin.Context) apiRequestLogBody {
	contentType := c.Request.Header.Get("Content-Type")
	if !isAuditableContentType(contentType) {
		return apiRequestLogBody{
			contentType:   contentType,
			size:          c.Request.ContentLength,
			omittedReason: "non_text_content_type",
		}
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return apiRequestLogBody{
			contentType:   contentType,
			omittedReason: "read_failed",
		}
	}
	body, err := storage.Bytes()
	if err != nil {
		return apiRequestLogBody{
			contentType:   contentType,
			size:          storage.Size(),
			omittedReason: "read_failed",
		}
	}
	text, redacted := auditBodyToStringWithRedact(body, contentType, common.APIRequestLogRedactSecrets)
	return apiRequestLogBody{
		body:        text,
		contentType: contentType,
		size:        int64(len(body)),
		redacted:    redacted,
	}
}

func buildResponseLogBody(c *gin.Context) apiRequestLogBody {
	contentType := c.Writer.Header().Get("Content-Type")
	if !common.APIRequestLogCaptureResponse {
		return apiRequestLogBody{
			contentType:   contentType,
			omittedReason: "capture_disabled",
		}
	}
	rawWriter, exists := c.Get(apiRequestLogWriterKey)
	if !exists {
		return apiRequestLogBody{
			contentType:   contentType,
			omittedReason: "capture_unavailable",
		}
	}
	writer, ok := rawWriter.(*apiRequestLogWriter)
	if !ok || writer == nil {
		return apiRequestLogBody{
			contentType:   contentType,
			omittedReason: "capture_unavailable",
		}
	}
	body, seen := writer.snapshot()
	if !isAuditableContentType(contentType) {
		return apiRequestLogBody{
			contentType:   contentType,
			size:          seen,
			omittedReason: "non_text_content_type",
		}
	}
	text, redacted := auditBodyToStringWithRedact(body, contentType, common.APIRequestLogRedactSecrets)
	return apiRequestLogBody{
		body:        text,
		contentType: contentType,
		size:        seen,
		redacted:    redacted,
	}
}

func buildAPIRequestLogMetadata(c *gin.Context, relayInfo *relaycommon.RelayInfo, relayErr *types.NewAPIError, requestLog apiRequestLogBody, responseLog apiRequestLogBody) map[string]interface{} {
	metadata := map[string]interface{}{
		"request_size":              requestLog.size,
		"response_size":             responseLog.size,
		"redact_secrets":            common.APIRequestLogRedactSecrets,
		"capture_response":          common.APIRequestLogCaptureResponse,
		"request_omitted_reason":    requestLog.omittedReason,
		"response_omitted_reason":   responseLog.omittedReason,
		"channel_id":                c.GetInt("channel_id"),
		"channel_name":              c.GetString("channel_name"),
		"channel_type":              c.GetInt("channel_type"),
		"used_channels":             c.GetStringSlice("use_channel"),
		"upstream_request_id":       c.GetString(common.UpstreamRequestIdKey),
		"is_api_request_log_record": true,
	}
	if relayInfo != nil {
		metadata["relay_format"] = string(relayInfo.RelayFormat)
		metadata["final_request_format"] = string(relayInfo.GetFinalRequestRelayFormat())
		metadata["origin_model"] = relayInfo.OriginModelName
		metadata["upstream_model"] = relayInfo.UpstreamModelName
		metadata["retry_index"] = relayInfo.RetryIndex
	}
	if relayErr != nil {
		metadata["error"] = relayErr.MaskSensitiveErrorWithStatusCode()
		metadata["error_code"] = relayErr.GetErrorCode()
		metadata["error_type"] = relayErr.GetErrorType()
		metadata["error_status_code"] = relayErr.StatusCode
	}
	return metadata
}

func applyUsageLogToAPIRequestLog(log *model.APIRequestLog, usageLog *model.Log) {
	if log == nil || usageLog == nil {
		return
	}
	log.UsageLogId = usageLog.Id
	log.UserId = usageLog.UserId
	log.Username = usageLog.Username
	log.TokenId = usageLog.TokenId
	log.TokenName = usageLog.TokenName
	log.ModelName = usageLog.ModelName
	log.CreatedAt = usageLog.CreatedAt
	log.RequestId = usageLog.RequestId
	log.UpstreamRequestId = usageLog.UpstreamRequestId
	log.IsStream = usageLog.IsStream
	log.ChannelId = usageLog.ChannelId
	log.Group = usageLog.Group
}

func relayModelName(relayInfo *relaycommon.RelayInfo) string {
	if relayInfo == nil {
		return ""
	}
	return relayInfo.OriginModelName
}

func relayUsingGroup(relayInfo *relaycommon.RelayInfo) string {
	if relayInfo == nil {
		return ""
	}
	return relayInfo.UsingGroup
}

func requestPath(c *gin.Context, relayInfo *relaycommon.RelayInfo) string {
	if c != nil {
		if originalPath := c.GetString(APIRequestLogOriginalPathKey); originalPath != "" {
			return originalPath
		}
	}
	if c != nil && c.Request != nil && c.Request.URL != nil && c.Request.URL.Path != "" {
		return c.Request.URL.Path
	}
	if relayInfo != nil {
		return relayInfo.RequestURLPath
	}
	return ""
}
