package service

import (
	"bytes"
	"sync"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

const contentAuditWriterKey = "content_audit_writer"

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
			"content_type":   contentType,
			"omitted_reason": "non_text_content_type",
		}
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return map[string]interface{}{
			"content_type":   contentType,
			"omitted_reason": "read_failed",
			"error":          err.Error(),
		}
	}
	return map[string]interface{}{
		"content_type":   contentType,
		"size":           storage.Size(),
		"omitted_reason": "request_body_not_stored",
	}
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
	return auditBodyToStringWithRedact(body, contentType, common.AuditContentRedactSecrets)
}

func auditBodyToStringWithRedact(body []byte, contentType string, redactSecrets bool) (string, bool) {
	return common.AuditBodyToStringWithRedact(body, contentType, redactSecrets)
}

func isAuditableContentType(contentType string) bool {
	return common.IsAuditableContentType(contentType)
}
