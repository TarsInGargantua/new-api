package common

import (
	"bytes"
	"regexp"
	"strings"
)

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

func AuditBodyToStringWithRedact(body []byte, contentType string, redactSecrets bool) (string, bool) {
	if len(body) == 0 {
		return "", false
	}
	text := string(body)
	if !redactSecrets {
		return text, false
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		var data interface{}
		if err := Unmarshal(trimmed, &data); err == nil {
			redacted := redactJSONValue(data)
			if encoded, err := Marshal(redacted); err == nil {
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
			lowerKey := strings.ToLower(key)
			if sensitiveJSONKeys[lowerKey] || strings.Contains(lowerKey, "secret") {
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

func IsAuditableContentType(contentType string) bool {
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
