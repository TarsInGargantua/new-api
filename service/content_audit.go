package service

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

const contentAuditWriterKey = "content_audit_writer"

var secretValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)sk-[A-Za-z0-9_-]{12,}`),
}

var sensitiveJSONKeys = map[string]bool{
	"api_key":       true,
	"apikey":        true,
	"authorization": true,
	"key":           true,
	"password":      true,
	"secret":        true,
	"token":         true,
	"access_token":  true,
	"refresh_token": true,
}

type contentAuditWriter struct {
	gin.ResponseWriter
	capture *limitedAuditBuffer
}

func (w *contentAuditWriter) Write(data []byte) (int, error) {
	w.capture.Write(data)
	return w.ResponseWriter.Write(data)
}

func (w *contentAuditWriter) WriteString(data string) (int, error) {
	w.capture.Write(common.StringToByteSlice(data))
	return w.ResponseWriter.WriteString(data)
}

type limitedAuditBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	seen      int64
	truncated bool
}

func newLimitedAuditBuffer(limit int) *limitedAuditBuffer {
	if limit < 0 {
		limit = 0
	}
	return &limitedAuditBuffer{limit: limit}
}

func (b *limitedAuditBuffer) Write(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.seen += int64(len(data))
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		if len(data) > 0 {
			b.truncated = true
		}
		return
	}
	if len(data) > remaining {
		b.buf.Write(data[:remaining])
		b.truncated = true
		return
	}
	b.buf.Write(data)
}

func (b *limitedAuditBuffer) Snapshot() ([]byte, int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	data := make([]byte, b.buf.Len())
	copy(data, b.buf.Bytes())
	return data, b.seen, b.truncated
}

// StartContentAuditCapture wraps Gin's response writer so the final response
// sent to the client can be recorded when content auditing is explicitly enabled.
func StartContentAuditCapture(c *gin.Context) {
	if c == nil || c.Writer == nil || !common.AuditContentEnabled {
		return
	}
	if _, exists := c.Get(contentAuditWriterKey); exists {
		return
	}
	writer := &contentAuditWriter{
		ResponseWriter: c.Writer,
		capture:        newLimitedAuditBuffer(common.AuditContentMaxBytes),
	}
	c.Writer = writer
	c.Set(contentAuditWriterKey, writer)
}

// AppendContentAudit adds request/response bodies to the log metadata. This is
// intentionally opt-in via AUDIT_CONTENT_ENABLED because these bodies can contain
// private user data.
func AppendContentAudit(c *gin.Context, other map[string]interface{}) {
	if c == nil || other == nil || !common.AuditContentEnabled {
		return
	}

	audit := map[string]interface{}{
		"max_bytes":      common.AuditContentMaxBytes,
		"redact_secrets": common.AuditContentRedactSecrets,
	}

	if requestAudit := buildRequestAudit(c); requestAudit != nil {
		audit["request"] = requestAudit
	}
	if common.AuditContentCaptureResponse {
		if responseAudit := buildResponseAudit(c); responseAudit != nil {
			audit["response"] = responseAudit
		}
	}

	if len(audit) > 2 {
		other["audit_content"] = audit
	}
}

func buildRequestAudit(c *gin.Context) map[string]interface{} {
	if c == nil || c.Request == nil {
		return nil
	}
	contentType := c.Request.Header.Get("Content-Type")
	if !isAuditableContentType(contentType) {
		return map[string]interface{}{
			"content_type":    contentType,
			"omitted_reason": "non_text_content_type",
		}
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return map[string]interface{}{
			"content_type":    contentType,
			"omitted_reason": "read_failed",
			"error":          err.Error(),
		}
	}
	body, err := storage.Bytes()
	if err != nil {
		return map[string]interface{}{
			"content_type":    contentType,
			"omitted_reason": "read_failed",
			"error":          err.Error(),
		}
	}
	return buildBodyAudit(contentType, body, int64(len(body)))
}

func buildResponseAudit(c *gin.Context) map[string]interface{} {
	rawWriter, exists := c.Get(contentAuditWriterKey)
	if !exists {
		return nil
	}
	writer, ok := rawWriter.(*contentAuditWriter)
	if !ok || writer == nil || writer.capture == nil {
		return nil
	}
	contentType := c.Writer.Header().Get("Content-Type")
	body, seen, truncated := writer.capture.Snapshot()
	if !isAuditableContentType(contentType) {
		return map[string]interface{}{
			"status":         c.Writer.Status(),
			"content_type":   contentType,
			"size":           seen,
			"omitted_reason": "non_text_content_type",
		}
	}
	audit := buildBodyAudit(contentType, body, seen)
	audit["status"] = c.Writer.Status()
	if truncated {
		audit["truncated"] = true
	}
	return audit
}

func buildBodyAudit(contentType string, body []byte, size int64) map[string]interface{} {
	captured := limitAuditBytes(body)
	text, redacted := auditBodyToString(captured, contentType)
	audit := map[string]interface{}{
		"content_type":   contentType,
		"size":           size,
		"captured_bytes": len(captured),
		"body":           text,
	}
	if int64(len(captured)) < size {
		audit["truncated"] = true
	}
	if redacted {
		audit["redacted"] = true
	}
	if !utf8.ValidString(text) {
		audit["encoding"] = "binary"
	}
	return audit
}

func limitAuditBytes(body []byte) []byte {
	limit := common.AuditContentMaxBytes
	if limit <= 0 {
		return []byte{}
	}
	if len(body) <= limit {
		return body
	}
	return body[:limit]
}

func auditBodyToString(body []byte, contentType string) (string, bool) {
	if len(body) == 0 {
		return "", false
	}
	text := string(body)
	if !common.AuditContentRedactSecrets {
		return text, false
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		var data interface{}
		if err := common.Unmarshal(trimmed, &data); err == nil {
			redacted := redactJSONValue(data)
			if encoded, err := common.Marshal(redacted); err == nil {
				return string(encoded), true
			}
		}
	}

	redacted := text
	for _, pattern := range secretValuePatterns {
		redacted = pattern.ReplaceAllStringFunc(redacted, func(match string) string {
			if strings.HasPrefix(strings.ToLower(match), "bearer ") {
				return "Bearer [REDACTED]"
			}
			return "[REDACTED]"
		})
	}
	return redacted, redacted != text
}

func redactJSONValue(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, child := range v {
			if sensitiveJSONKeys[strings.ToLower(key)] || strings.Contains(strings.ToLower(key), "secret") {
				out[key] = "[REDACTED]"
				continue
			}
			out[key] = redactJSONValue(child)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, child := range v {
			out[i] = redactJSONValue(child)
		}
		return out
	default:
		return value
	}
}

func isAuditableContentType(contentType string) bool {
	if contentType == "" {
		return true
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	switch mediaType {
	case "application/json", "application/x-ndjson", "application/x-www-form-urlencoded", "multipart/form-data":
		return true
	}
	return strings.HasSuffix(mediaType, "+json")
}
