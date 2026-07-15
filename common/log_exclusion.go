package common

import (
	"strings"
	"sync"
)

var callLogExcludedUsernamesMu sync.RWMutex
var callLogExcludedUsernames = parseCallLogExcludedUsernames("ryan")

func SetCallLogExcludedUsernames(value string) {
	usernames := parseCallLogExcludedUsernames(value)
	callLogExcludedUsernamesMu.Lock()
	callLogExcludedUsernames = usernames
	callLogExcludedUsernamesMu.Unlock()
}

func IsCallLogExcludedUsername(username string) bool {
	normalized := strings.ToLower(strings.TrimSpace(username))
	if normalized == "" {
		return false
	}
	callLogExcludedUsernamesMu.RLock()
	_, excluded := callLogExcludedUsernames[normalized]
	callLogExcludedUsernamesMu.RUnlock()
	return excluded
}

func parseCallLogExcludedUsernames(value string) map[string]struct{} {
	usernames := make(map[string]struct{})
	for _, username := range strings.Split(value, ",") {
		normalized := strings.ToLower(strings.TrimSpace(username))
		if normalized != "" {
			usernames[normalized] = struct{}{}
		}
	}
	return usernames
}
