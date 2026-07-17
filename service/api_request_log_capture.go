package service

import (
	"io"
	"strconv"
	"strings"

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
const apiRequestLogPendingUsageKey = "api_request_log_pending_usage"
const APIRequestLogOriginalPathKey = "api_request_log_original_path"

type apiRequestLogWriter struct {
	gin.ResponseWriter
	buffer *limitedAuditBuffer
	stream *apiRequestLogSSECollector
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
	if w.buffer != nil {
		w.buffer.Write(data)
	}
	if w.stream != nil {
		w.stream.Feed(data)
	}
}

func (w *apiRequestLogWriter) snapshot() ([]byte, int64, bool) {
	if w.buffer == nil {
		return nil, 0, false
	}
	return w.buffer.Snapshot()
}

func (w *apiRequestLogWriter) streamSnapshot() apiRequestLogSSESnapshot {
	if w == nil || w.stream == nil {
		return apiRequestLogSSESnapshot{}
	}
	return w.stream.Snapshot()
}

type apiRequestLogBody struct {
	body          string
	contentType   string
	size          int64
	omittedReason string
	redacted      bool
}

// APIRequestLogTurnMeta contains only the identifiers needed to materialize a
// conversation turn. Raw client metadata and headers are intentionally omitted.
type APIRequestLogTurnMeta struct {
	SessionID           string
	TurnID              string
	WindowID            string
	RequestKind         string
	TurnStartedAtUnixMS int64
	Completed           bool
	CompletionSignal    string
	Items               []APIRequestLogTurnItemMeta
}

type APIRequestLogTurnItemMeta struct {
	Seq            int
	ProviderItemID string
	TurnID         string
	MessagePhase   string
	Status         string
}

type codexTurnMetadataHeader struct {
	SessionID           string      `json:"session_id"`
	ThreadID            string      `json:"thread_id"`
	TurnID              string      `json:"turn_id"`
	WindowID            string      `json:"window_id"`
	RequestKind         string      `json:"request_kind"`
	TurnStartedAtUnixMS interface{} `json:"turn_started_at_unix_ms"`
}

func StartAPIRequestLogCapture(c *gin.Context) {
	if c == nil || c.Writer == nil || !common.APIRequestLogEnabled {
		return
	}
	if common.IsCallLogExcludedUsername(c.GetString("username")) {
		return
	}
	if _, exists := c.Get(apiRequestLogWriterKey); exists {
		return
	}
	writer := &apiRequestLogWriter{
		ResponseWriter: c.Writer,
		buffer:         newLimitedAuditBuffer(common.APIRequestLogMaxBodyBytes),
		stream:         newAPIRequestLogSSECollector(common.APIRequestLogRedactSecrets),
	}
	c.Writer = writer
	c.Set(apiRequestLogWriterKey, writer)
	common.SetContextKey(c, constant.ContextKeyAPIRequestLogDeferConsumeSync, true)
}

func apiRequestLogTurnMetaFromRequest(c *gin.Context, relayInfo *relaycommon.RelayInfo) APIRequestLogTurnMeta {
	meta := APIRequestLogTurnMeta{}
	if c != nil && c.Request != nil {
		raw := strings.TrimSpace(c.Request.Header.Get("X-Codex-Turn-Metadata"))
		if raw != "" {
			var header codexTurnMetadataHeader
			if common.UnmarshalJsonStr(raw, &header) == nil {
				meta.SessionID = firstNonEmpty(strings.TrimSpace(header.SessionID), strings.TrimSpace(header.ThreadID))
				meta.TurnID = strings.TrimSpace(header.TurnID)
				meta.WindowID = strings.TrimSpace(header.WindowID)
				meta.RequestKind = strings.TrimSpace(header.RequestKind)
				meta.TurnStartedAtUnixMS = apiRequestLogInt64(header.TurnStartedAtUnixMS)
			}
		}
	}
	if meta.SessionID == "" {
		meta.SessionID = conversationIDFromRelay(c, relayInfo)
	}
	return meta
}

func apiRequestLogInt64(value interface{}) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return parsed
	default:
		return 0
	}
}

func RecordAPIRequestLog(c *gin.Context, relayInfo *relaycommon.RelayInfo, relayErr *types.NewAPIError) {
	recordAPIRequestLog(c, relayInfo, relayErr, nil)
}

func recordAPIRequestLog(c *gin.Context, relayInfo *relaycommon.RelayInfo, relayErr *types.NewAPIError, usageLog *model.Log) {
	if c == nil || c.Request == nil || !common.APIRequestLogEnabled {
		return
	}
	if common.IsCallLogExcludedUsername(c.GetString("username")) {
		return
	}
	directUsageLog := usageLog != nil
	if usageLog == nil {
		if recorded, exists := c.Get(apiRequestLogRecordedKey); exists && recorded == true {
			return
		}
		usageLog = pendingAPIRequestLogUsage(c)
	} else if isAPIRequestLogUsageRecorded(c, usageLog.Id) {
		return
	}
	if usageLog != nil && common.IsCallLogExcludedUsername(usageLog.Username) {
		return
	}

	requestLog := buildRequestLogBody(c)
	responseLog := buildResponseLogBody(c)
	itemBuild := buildAPIRequestLogItems(c, relayInfo, requestLog, responseLog)

	log := &model.APIRequestLog{
		Source:                model.APIRequestLogSourceLive,
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
		APIFormat:             itemBuild.apiFormat,
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
		SchemaVersion:         model.APIRequestLogSchemaVersion,
		ParseStatus:           itemBuild.parseStatus,
		ParseError:            itemBuild.parseError,
		Items:                 itemBuild.items,
	}
	applyUsageLogToAPIRequestLog(log, usageLog)
	if log.StatusCode == 0 {
		log.StatusCode = 200
	}
	log.TurnMeta = apiRequestLogMaterializationMeta(log, itemBuild)
	if err := model.CreateAPIRequestLog(log); err != nil {
		logger.LogError(c, "failed to record api request log: "+err.Error())
		return
	}
	if usageLog != nil {
		markAPIRequestLogUsageRecorded(c, usageLog.Id)
		if !directUsageLog {
			c.Set(apiRequestLogRecordedKey, true)
		}
		return
	}
	c.Set(apiRequestLogRecordedKey, true)
}

func apiRequestLogMaterializationMeta(log *model.APIRequestLog, itemBuild apiRequestLogItemBuildResult) *model.APIRequestLogTurnMeta {
	turnMeta := itemBuild.turnMeta
	meta := &model.APIRequestLogTurnMeta{
		SessionId:        strings.TrimSpace(turnMeta.SessionID),
		TurnId:           strings.TrimSpace(turnMeta.TurnID),
		Protocol:         itemBuild.apiFormat,
		StartedAt:        turnMeta.TurnStartedAtUnixMS,
		CompletionSignal: strings.TrimSpace(turnMeta.CompletionSignal),
		WindowId:         strings.TrimSpace(turnMeta.WindowID),
		RequestKind:      strings.TrimSpace(turnMeta.RequestKind),
	}
	if meta.SessionId != "" {
		meta.CompletionStatus = model.APIRequestLogTurnStatusOpen
		meta.Attribution = model.APIRequestLogTurnAttributionInferred
	}
	if meta.SessionId != "" && meta.TurnId != "" {
		meta.Attribution = model.APIRequestLogTurnAttributionExact
	}
	if turnMeta.Completed && meta.SessionId != "" {
		meta.CompletionStatus = model.APIRequestLogTurnStatusCompleted
		meta.CompletedAt = common.GetTimestamp()
	}
	if meta.SessionId == "" {
		meta.CompletionStatus = model.APIRequestLogTurnStatusUnknown
		meta.Attribution = model.APIRequestLogTurnAttributionUnknown
	}
	meta.Items = make([]model.APIRequestLogTurnItemMeta, 0, len(turnMeta.Items))
	for _, item := range turnMeta.Items {
		meta.Items = append(meta.Items, model.APIRequestLogTurnItemMeta{
			Seq:            item.Seq,
			ProviderItemId: strings.TrimSpace(item.ProviderItemID),
			TurnId:         strings.TrimSpace(item.TurnID),
			MessagePhase:   strings.TrimSpace(item.MessagePhase),
			ItemStatus:     strings.TrimSpace(item.Status),
		})
	}
	if log != nil && meta.StartedAt == 0 {
		meta.StartedAt = log.CreatedAt
	}
	return meta
}

func RecordAPIRequestLogForConsume(c *gin.Context, relayInfo *relaycommon.RelayInfo, usageLog *model.Log) {
	if c == nil || usageLog == nil || usageLog.Id <= 0 || !common.APIRequestLogEnabled {
		return
	}
	if common.IsCallLogExcludedUsername(c.GetString("username")) || common.IsCallLogExcludedUsername(usageLog.Username) {
		return
	}
	rememberAPIRequestLogUsageContext(c, usageLog)
	if common.GetContextKeyBool(c, constant.ContextKeyAPIRequestLogDeferConsumeSync) {
		rememberAPIRequestLogPendingUsage(c, usageLog)
		return
	}
	if isAPIRequestLogUsageRecorded(c, usageLog.Id) {
		return
	}
	if err := model.CreateAPIRequestLogFromConsumeLog(c, usageLog); err != nil {
		logger.LogError(c, "failed to sync consume log to api request log: "+err.Error())
		return
	}
	markAPIRequestLogUsageRecorded(c, usageLog.Id)
}

func rememberAPIRequestLogPendingUsage(c *gin.Context, usageLog *model.Log) {
	if c == nil || usageLog == nil || usageLog.Id <= 0 {
		return
	}
	c.Set(apiRequestLogPendingUsageKey, usageLog)
}

func pendingAPIRequestLogUsage(c *gin.Context) *model.Log {
	if c == nil {
		return nil
	}
	raw, exists := c.Get(apiRequestLogPendingUsageKey)
	if !exists {
		return nil
	}
	usageLog, _ := raw.(*model.Log)
	return usageLog
}

func rememberAPIRequestLogUsageContext(c *gin.Context, usageLog *model.Log) {
	if c == nil || usageLog == nil {
		return
	}
	if c.GetString(common.RequestIdKey) == "" && usageLog.RequestId != "" {
		c.Set(common.RequestIdKey, usageLog.RequestId)
	}
	if c.GetString(common.UpstreamRequestIdKey) == "" && usageLog.UpstreamRequestId != "" {
		c.Set(common.UpstreamRequestIdKey, usageLog.UpstreamRequestId)
	}
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
	body, size, truncated, err := readAPIRequestLogBodyStorage(storage)
	if err != nil {
		return apiRequestLogBody{
			contentType:   contentType,
			size:          storage.Size(),
			omittedReason: "read_failed",
		}
	}
	text, redacted := auditBodyToStringWithRedact(body, contentType, common.APIRequestLogRedactSecrets)
	return apiRequestLogBody{
		body:          text,
		contentType:   contentType,
		size:          size,
		omittedReason: truncatedReason(truncated),
		redacted:      redacted,
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
	body, seen, truncated := writer.snapshot()
	if !isAuditableContentType(contentType) {
		return apiRequestLogBody{
			contentType:   contentType,
			size:          seen,
			omittedReason: "non_text_content_type",
		}
	}
	text, redacted := auditBodyToStringWithRedact(body, contentType, common.APIRequestLogRedactSecrets)
	return apiRequestLogBody{
		body:          text,
		contentType:   contentType,
		size:          seen,
		omittedReason: truncatedReason(truncated),
		redacted:      redacted,
	}
}

func readAPIRequestLogBodyStorage(storage common.BodyStorage) ([]byte, int64, bool, error) {
	size := storage.Size()
	limit := common.APIRequestLogMaxBodyBytes
	if limit <= 0 {
		return []byte{}, size, size > 0, nil
	}
	if size <= int64(limit) {
		body, err := storage.Bytes()
		return body, size, false, err
	}
	if _, err := storage.Seek(0, io.SeekStart); err != nil {
		return nil, size, false, err
	}
	body, err := io.ReadAll(io.LimitReader(storage, int64(limit)))
	if _, seekErr := storage.Seek(0, io.SeekStart); err == nil {
		err = seekErr
	}
	return body, size, true, err
}

func truncatedReason(truncated bool) string {
	if truncated {
		return "truncated"
	}
	return ""
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
	log.Quota = usageLog.Quota
	log.PromptTokens = usageLog.PromptTokens
	log.CompletionTokens = usageLog.CompletionTokens
	log.TokenUsed = usageLog.PromptTokens + usageLog.CompletionTokens
	log.UseTime = usageLog.UseTime
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
