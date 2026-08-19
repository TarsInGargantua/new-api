package model

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

type APIRequestLogSessionListItem struct {
	Id               int64  `json:"id"`
	ExportBatchId    int64  `json:"-"`
	SessionId        string `json:"session_id"`
	Protocol         string `json:"protocol"`
	StartedAt        int64  `json:"started_at"`
	CompletedAt      int64  `json:"completed_at"`
	CompletionStatus string `json:"completion_status"`
	Attribution      string `json:"attribution"`
	UserId           int    `json:"user_id"`
	Username         string `json:"username"`
	TokenId          int    `json:"token_id"`
	TokenName        string `json:"token_name"`
	ModelName        string `json:"model_name"`
	RequestCount     int    `json:"request_count"`
	ItemCount        int    `json:"item_count"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TokenUsed        int    `json:"token_used"`
	Quota            int    `json:"quota"`
	Exported         bool   `json:"exported"`
}

type APIRequestLogSessionRequest struct {
	Sequence          int    `json:"sequence"`
	LogId             int    `json:"log_id"`
	RequestId         string `json:"request_id,omitempty"`
	UpstreamRequestId string `json:"upstream_request_id,omitempty"`
	CreatedAt         int64  `json:"created_at"`
	StatusCode        int    `json:"status_code"`
	IsStream          bool   `json:"is_stream"`
}

type APIRequestLogSessionItem struct {
	Sequence        int               `json:"sequence"`
	SourceItemId    int               `json:"source_item_id"`
	SourceSequence  int               `json:"source_sequence"`
	Phase           string            `json:"phase"`
	ItemType        string            `json:"item_type"`
	Role            string            `json:"role,omitempty"`
	ContentType     string            `json:"content_type"`
	Content         APIRequestLogBody `json:"content,omitempty"`
	ToolCallId      string            `json:"tool_call_id,omitempty"`
	Name            string            `json:"name,omitempty"`
	Source          string            `json:"source,omitempty"`
	ProviderItemId  string            `json:"provider_item_id,omitempty"`
	MessagePhase    string            `json:"message_phase,omitempty"`
	ItemStatus      string            `json:"item_status,omitempty"`
	Redacted        bool              `json:"redacted"`
	Truncated       bool              `json:"truncated"`
	ContextSnapshot bool              `json:"context_snapshot"`
}

type APIRequestLogSessionDetail struct {
	APIRequestLogSessionListItem
	Requests                []APIRequestLogSessionRequest `json:"requests"`
	Items                   []APIRequestLogSessionItem    `json:"items"`
	ContextLoaded           bool                          `json:"context_loaded"`
	ContextComplete         bool                          `json:"context_complete"`
	ContextOmittedItemCount int                           `json:"context_omitted_item_count"`
	InternalTurns           []*APIRequestLogTurnDetail    `json:"-"`
}

type apiRequestLogSessionAggregateRow struct {
	Id                  int64
	ExportBatchId       int64
	SessionId           string
	Protocol            string
	StartedAt           int64
	CompletedAt         int64
	UserId              int
	Username            string
	TokenId             int
	TokenName           string
	ModelName           string
	RequestCount        int
	ItemCount           int
	PromptTokens        int
	CompletionTokens    int
	TokenUsed           int
	Quota               int
	IncompleteCount     int64
	OpenCount           int64
	UnknownAttribution  int64
	InferredAttribution int64
}

type apiRequestLogSessionKey struct {
	OwnerFingerprint string
	SessionId        string
	ExportBatchId    int64
}

type apiRequestLogSessionKeyRow struct {
	OwnerFingerprint string
	SessionId        string
	ExportBatchId    int64
	Id               int64
	CompletedAt      int64
}

func (row apiRequestLogSessionKeyRow) key() apiRequestLogSessionKey {
	return apiRequestLogSessionKey{
		OwnerFingerprint: row.OwnerFingerprint,
		SessionId:        row.SessionId,
		ExportBatchId:    row.ExportBatchId,
	}
}

type apiRequestLogSessionCountCacheEntry struct {
	Total      int64
	ExpiresAt  time.Time
	Refreshing bool
}

var apiRequestLogSessionCountCache = struct {
	sync.Mutex
	entries map[string]apiRequestLogSessionCountCacheEntry
}{entries: make(map[string]apiRequestLogSessionCountCacheEntry)}

func GetAPIRequestLogSessions(db *gorm.DB, params APIRequestLogTurnQueryParams) (items []*APIRequestLogSessionListItem, total int64, err error) {
	if db == nil {
		return nil, 0, errors.New("request log database is not initialized")
	}
	total, err = countAPIRequestLogSessions(db, params)
	if err != nil {
		return nil, 0, err
	}
	limit := params.Num
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := params.StartIdx
	if offset < 0 {
		offset = 0
	}
	keys, err := listAPIRequestLogSessionKeys(db, params, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	if len(keys) == 0 {
		return []*APIRequestLogSessionListItem{}, total, nil
	}
	rows, err := aggregateAPIRequestLogSessions(db, params, keys)
	if err != nil {
		return nil, 0, err
	}
	items = make([]*APIRequestLogSessionListItem, 0, len(keys))
	for _, key := range keys {
		if row, ok := rows[key]; ok {
			items = append(items, apiRequestLogSessionListItemFromAggregate(row))
		}
	}
	return items, total, nil
}

func countAPIRequestLogSessions(db *gorm.DB, params APIRequestLogTurnQueryParams) (int64, error) {
	params.StartIdx = 0
	params.Num = 0
	cacheKey := fmt.Sprintf("%p|%s|%s", db, db.Dialector.Name(), apiRequestLogTurnQueryCacheKey(params))
	now := time.Now()
	apiRequestLogSessionCountCache.Lock()
	if cached, ok := apiRequestLogSessionCountCache.entries[cacheKey]; ok {
		if now.After(cached.ExpiresAt) && !cached.Refreshing {
			cached.Refreshing = true
			apiRequestLogSessionCountCache.entries[cacheKey] = cached
			go refreshAPIRequestLogSessionCount(db, params, cacheKey)
		}
		apiRequestLogSessionCountCache.Unlock()
		return cached.Total, nil
	}
	total, err := queryAPIRequestLogSessionCount(db, params)
	if err == nil {
		storeAPIRequestLogSessionCount(cacheKey, total, now)
	}
	apiRequestLogSessionCountCache.Unlock()
	return total, err
}

func queryAPIRequestLogSessionCount(db *gorm.DB, params APIRequestLogTurnQueryParams) (int64, error) {
	query := buildAPIRequestLogTurnsQuery(db, params)
	var total int64
	var err error
	switch db.Dialector.Name() {
	case "mysql":
		var result struct{ Total int64 }
		err = query.Select("COUNT(DISTINCT " + apiRequestLogTurnsTable + ".owner_fingerprint, " + apiRequestLogTurnsTable + ".session_id, " + apiRequestLogTurnsTable + ".export_batch_id) AS total").Scan(&result).Error
		total = result.Total
	case "postgres":
		var result struct{ Total int64 }
		err = query.Select("COUNT(DISTINCT (" + apiRequestLogTurnsTable + ".owner_fingerprint, " + apiRequestLogTurnsTable + ".session_id, " + apiRequestLogTurnsTable + ".export_batch_id)) AS total").Scan(&result).Error
		total = result.Total
	default:
		grouped := buildAPIRequestLogSessionGroupQuery(db, params).Select("1")
		err = db.Table("(?) AS request_log_sessions", grouped).Count(&total).Error
	}
	if err != nil {
		return 0, err
	}
	return total, nil
}

func refreshAPIRequestLogSessionCount(db *gorm.DB, params APIRequestLogTurnQueryParams, cacheKey string) {
	total, err := queryAPIRequestLogSessionCount(db, params)
	now := time.Now()
	apiRequestLogSessionCountCache.Lock()
	defer apiRequestLogSessionCountCache.Unlock()
	if err != nil {
		if cached, ok := apiRequestLogSessionCountCache.entries[cacheKey]; ok {
			cached.Refreshing = false
			cached.ExpiresAt = now.Add(time.Minute)
			apiRequestLogSessionCountCache.entries[cacheKey] = cached
		}
		return
	}
	storeAPIRequestLogSessionCount(cacheKey, total, now)
}

func storeAPIRequestLogSessionCount(cacheKey string, total int64, now time.Time) {
	if len(apiRequestLogSessionCountCache.entries) >= 256 {
		for key, entry := range apiRequestLogSessionCountCache.entries {
			if now.After(entry.ExpiresAt) && !entry.Refreshing {
				delete(apiRequestLogSessionCountCache.entries, key)
			}
		}
	}
	apiRequestLogSessionCountCache.entries[cacheKey] = apiRequestLogSessionCountCacheEntry{Total: total, ExpiresAt: now.Add(5 * time.Minute)}
}

func apiRequestLogTurnQueryCacheKey(params APIRequestLogTurnQueryParams) string {
	return strings.Join([]string{
		params.SessionId,
		params.TurnId,
		params.Protocol,
		strings.Join(params.Protocols, "\x1f"),
		params.ModelName,
		strings.Join(params.ModelNames, "\x1f"),
		params.Username,
		strings.Join(params.Usernames, "\x1f"),
		params.TokenName,
		params.CompletionStatus,
		strings.Join(params.CompletionStatuses, "\x1f"),
		params.Attribution,
		strings.Join(params.Attributions, "\x1f"),
		strconv.FormatInt(params.StartTimestamp, 10),
		strconv.FormatInt(params.EndTimestamp, 10),
		apiRequestLogOptionalBoolCacheKey(params.Exported),
	}, "\x1e")
}

func apiRequestLogOptionalBoolCacheKey(value *bool) string {
	if value == nil {
		return ""
	}
	if *value {
		return "1"
	}
	return "0"
}

func listAPIRequestLogSessionKeys(db *gorm.DB, params APIRequestLogTurnQueryParams, offset, limit int) ([]apiRequestLogSessionKey, error) {
	params.StartIdx = 0
	params.Num = 0
	target := offset + limit
	if target <= 0 {
		return []apiRequestLogSessionKey{}, nil
	}
	seen := make(map[apiRequestLogSessionKey]struct{}, target)
	keys := make([]apiRequestLogSessionKey, 0, target)
	var cursorCompletedAt int64
	var cursorId int64
	hasCursor := false
	for len(keys) < target {
		query := buildAPIRequestLogTurnsQuery(db, params).
			Select(apiRequestLogTurnsTable + ".owner_fingerprint, " + apiRequestLogTurnsTable + ".session_id, " + apiRequestLogTurnsTable + ".export_batch_id, " + apiRequestLogTurnsTable + ".id, " + apiRequestLogTurnsTable + ".completed_at")
		if hasCursor {
			query = query.Where("("+apiRequestLogTurnsTable+".completed_at < ? OR ("+apiRequestLogTurnsTable+".completed_at = ? AND "+apiRequestLogTurnsTable+".id < ?))", cursorCompletedAt, cursorCompletedAt, cursorId)
		}
		var rows []apiRequestLogSessionKeyRow
		if err := query.Order(apiRequestLogTurnsTable + ".completed_at DESC").Order(apiRequestLogTurnsTable + ".id DESC").Limit(2000).Scan(&rows).Error; err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			key := row.key()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
			if len(keys) >= target {
				break
			}
		}
		last := rows[len(rows)-1]
		cursorCompletedAt = last.CompletedAt
		cursorId = last.Id
		hasCursor = true
		if len(rows) < 2000 {
			break
		}
	}
	if offset >= len(keys) {
		return []apiRequestLogSessionKey{}, nil
	}
	end := offset + limit
	if end > len(keys) {
		end = len(keys)
	}
	return keys[offset:end], nil
}

func aggregateAPIRequestLogSessions(db *gorm.DB, params APIRequestLogTurnQueryParams, keys []apiRequestLogSessionKey) (map[apiRequestLogSessionKey]apiRequestLogSessionAggregateRow, error) {
	params.StartIdx = 0
	params.Num = 0
	keyQuery := db.Where("1 = 0")
	for _, key := range keys {
		keyQuery = keyQuery.Or("("+apiRequestLogTurnsTable+".owner_fingerprint = ? AND "+apiRequestLogTurnsTable+".session_id = ? AND "+apiRequestLogTurnsTable+".export_batch_id = ?)", key.OwnerFingerprint, key.SessionId, key.ExportBatchId)
	}
	var turns []APIRequestLogTurn
	if err := buildAPIRequestLogTurnsQuery(db, params).Where(keyQuery).Find(&turns).Error; err != nil {
		return nil, err
	}
	rows := make(map[apiRequestLogSessionKey]apiRequestLogSessionAggregateRow, len(keys))
	for _, turn := range turns {
		key := apiRequestLogSessionKey{OwnerFingerprint: turn.OwnerFingerprint, SessionId: turn.SessionId, ExportBatchId: turn.ExportBatchId}
		row, exists := rows[key]
		if !exists {
			row = apiRequestLogSessionAggregateRow{
				Id: turn.Id, ExportBatchId: turn.ExportBatchId, SessionId: turn.SessionId,
				Protocol: turn.Protocol, StartedAt: turn.StartedAt, CompletedAt: turn.CompletedAt,
				UserId: turn.UserId, Username: turn.Username, TokenId: turn.TokenId, TokenName: turn.TokenName, ModelName: turn.ModelName,
			}
		} else {
			row.Id = min(row.Id, turn.Id)
			row.Protocol = min(row.Protocol, turn.Protocol)
			row.StartedAt = min(row.StartedAt, turn.StartedAt)
			row.CompletedAt = max(row.CompletedAt, turn.CompletedAt)
			row.UserId = min(row.UserId, turn.UserId)
			row.Username = min(row.Username, turn.Username)
			row.TokenId = min(row.TokenId, turn.TokenId)
			row.TokenName = min(row.TokenName, turn.TokenName)
			row.ModelName = min(row.ModelName, turn.ModelName)
		}
		row.RequestCount += turn.RequestCount
		row.ItemCount += turn.ItemCount
		row.PromptTokens += turn.PromptTokens
		row.CompletionTokens += turn.CompletionTokens
		row.TokenUsed += turn.TokenUsed
		row.Quota += turn.Quota
		if turn.CompletionStatus != APIRequestLogTurnStatusCompleted {
			row.IncompleteCount++
		}
		if turn.CompletionStatus == APIRequestLogTurnStatusOpen {
			row.OpenCount++
		}
		if turn.Attribution == APIRequestLogTurnAttributionUnknown {
			row.UnknownAttribution++
		}
		if turn.Attribution == APIRequestLogTurnAttributionInferred {
			row.InferredAttribution++
		}
		rows[key] = row
	}
	return rows, nil
}

func GetAPIRequestLogSessionByAnchorId(db *gorm.DB, anchorId int64) (*APIRequestLogSessionDetail, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	if anchorId <= 0 {
		return nil, errors.New("invalid request log session id")
	}
	var anchor APIRequestLogTurn
	if err := db.First(&anchor, anchorId).Error; err != nil {
		return nil, err
	}
	branchId, err := apiRequestLogSessionBranchForTurn(db, &anchor)
	if err != nil {
		return nil, err
	}
	query := db.Model(&APIRequestLogTurn{}).
		Where("owner_fingerprint = ? AND session_id = ?", anchor.OwnerFingerprint, anchor.SessionId)
	query = applyAPIRequestLogSessionBranch(query, branchId)
	var turns []APIRequestLogTurn
	if err := query.Order("turn_index ASC").Order("started_at ASC").Order("id ASC").Find(&turns).Error; err != nil {
		return nil, err
	}
	if len(turns) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	turnIds := apiRequestLogTurnIds(turns)
	details, err := getAPIRequestLogTurnDetails(db, turnIds, true)
	if err != nil {
		return nil, err
	}
	return buildAPIRequestLogSessionDetail(details, branchId), nil
}

func buildAPIRequestLogSessionGroupQuery(db *gorm.DB, params APIRequestLogTurnQueryParams) *gorm.DB {
	params.StartIdx = 0
	params.Num = 0
	return buildAPIRequestLogTurnsQuery(db, params).
		Select(
			"MIN(" + apiRequestLogTurnsTable + ".id) AS id, " +
				apiRequestLogTurnsTable + ".export_batch_id AS export_batch_id, " +
				apiRequestLogTurnsTable + ".session_id AS session_id, " +
				"MIN(" + apiRequestLogTurnsTable + ".protocol) AS protocol, " +
				"MIN(" + apiRequestLogTurnsTable + ".started_at) AS started_at, " +
				"MAX(" + apiRequestLogTurnsTable + ".completed_at) AS completed_at, " +
				"MIN(" + apiRequestLogTurnsTable + ".user_id) AS user_id, " +
				"MIN(" + apiRequestLogTurnsTable + ".username) AS username, " +
				"MIN(" + apiRequestLogTurnsTable + ".token_id) AS token_id, " +
				"MIN(" + apiRequestLogTurnsTable + ".token_name) AS token_name, " +
				"MIN(" + apiRequestLogTurnsTable + ".model_name) AS model_name, " +
				"SUM(" + apiRequestLogTurnsTable + ".request_count) AS request_count, " +
				"SUM(" + apiRequestLogTurnsTable + ".item_count) AS item_count, " +
				"SUM(" + apiRequestLogTurnsTable + ".prompt_tokens) AS prompt_tokens, " +
				"SUM(" + apiRequestLogTurnsTable + ".completion_tokens) AS completion_tokens, " +
				"SUM(" + apiRequestLogTurnsTable + ".token_used) AS token_used, " +
				"SUM(" + apiRequestLogTurnsTable + ".quota) AS quota, " +
				"SUM(CASE WHEN " + apiRequestLogTurnsTable + ".completion_status <> '" + APIRequestLogTurnStatusCompleted + "' THEN 1 ELSE 0 END) AS incomplete_count, " +
				"SUM(CASE WHEN " + apiRequestLogTurnsTable + ".completion_status = '" + APIRequestLogTurnStatusOpen + "' THEN 1 ELSE 0 END) AS open_count, " +
				"SUM(CASE WHEN " + apiRequestLogTurnsTable + ".attribution = '" + APIRequestLogTurnAttributionUnknown + "' THEN 1 ELSE 0 END) AS unknown_attribution, " +
				"SUM(CASE WHEN " + apiRequestLogTurnsTable + ".attribution = '" + APIRequestLogTurnAttributionInferred + "' THEN 1 ELSE 0 END) AS inferred_attribution").
		Group(apiRequestLogTurnsTable + ".owner_fingerprint").
		Group(apiRequestLogTurnsTable + ".session_id").
		Group(apiRequestLogTurnsTable + ".export_batch_id")
}

func apiRequestLogSessionListItemFromAggregate(row apiRequestLogSessionAggregateRow) *APIRequestLogSessionListItem {
	status := APIRequestLogTurnStatusCompleted
	if row.IncompleteCount > 0 {
		status = APIRequestLogTurnStatusUnknown
		if row.OpenCount > 0 {
			status = APIRequestLogTurnStatusOpen
		}
	}
	attribution := APIRequestLogTurnAttributionExact
	if row.UnknownAttribution > 0 {
		attribution = APIRequestLogTurnAttributionUnknown
	} else if row.InferredAttribution > 0 {
		attribution = APIRequestLogTurnAttributionInferred
	}
	return &APIRequestLogSessionListItem{
		Id: row.Id, ExportBatchId: row.ExportBatchId, SessionId: row.SessionId,
		Protocol: row.Protocol, StartedAt: row.StartedAt, CompletedAt: row.CompletedAt,
		CompletionStatus: status, Attribution: attribution,
		UserId: row.UserId, Username: row.Username, TokenId: row.TokenId, TokenName: row.TokenName,
		ModelName: row.ModelName, RequestCount: row.RequestCount, ItemCount: row.ItemCount,
		PromptTokens: row.PromptTokens, CompletionTokens: row.CompletionTokens,
		TokenUsed: row.TokenUsed, Quota: row.Quota, Exported: row.ExportBatchId != 0,
	}
}

func apiRequestLogSessionBranchSQL() string {
	return apiRequestLogTurnsTable + ".export_batch_id"
}

func apiRequestLogSessionBranchForTurn(db *gorm.DB, turn *APIRequestLogTurn) (int64, error) {
	if turn == nil || turn.Id <= 0 {
		return 0, nil
	}
	if turn.ExportBatchId != 0 {
		return turn.ExportBatchId, nil
	}
	var member APIRequestLogExportMember
	err := db.Select("batch_id").Where("turn_record_id = ?", turn.Id).First(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if turn.ExportedVersion > 0 {
			return -1, nil
		}
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if member.BatchId > 0 {
		return member.BatchId, nil
	}
	if turn.ExportedVersion > 0 {
		return -1, nil
	}
	return 0, nil
}

func applyAPIRequestLogSessionBranch(query *gorm.DB, branchId int64) *gorm.DB {
	switch {
	case branchId > 0:
		return query.Where("("+apiRequestLogTurnsTable+".export_batch_id = ? OR ("+apiRequestLogTurnsTable+".export_batch_id = 0 AND EXISTS (SELECT 1 FROM "+apiRequestLogExportMembersTable+" session_member WHERE session_member.turn_record_id = "+apiRequestLogTurnsTable+".id AND session_member.batch_id = ?)))", branchId, branchId)
	case branchId < 0:
		return query.Where("(" + apiRequestLogTurnsTable + ".export_batch_id < 0 OR (" + apiRequestLogTurnsTable + ".export_batch_id = 0 AND " + apiRequestLogTurnsTable + ".exported_version > 0))")
	default:
		return query.Where("NOT " + apiRequestLogTurnExportedSQL())
	}
}

func buildAPIRequestLogSessionDetail(turns []*APIRequestLogTurnDetail, branchId int64) *APIRequestLogSessionDetail {
	if len(turns) == 0 {
		return nil
	}
	sort.SliceStable(turns, func(i, j int) bool {
		if turns[i].TurnIndex != turns[j].TurnIndex {
			return turns[i].TurnIndex < turns[j].TurnIndex
		}
		if turns[i].StartedAt != turns[j].StartedAt {
			return turns[i].StartedAt < turns[j].StartedAt
		}
		return turns[i].Id < turns[j].Id
	})
	first := turns[0]
	last := turns[len(turns)-1]
	detail := &APIRequestLogSessionDetail{
		APIRequestLogSessionListItem: APIRequestLogSessionListItem{
			Id: first.Id, ExportBatchId: branchId, SessionId: first.SessionId,
			Protocol: first.Protocol, StartedAt: first.StartedAt, CompletedAt: last.CompletedAt,
			CompletionStatus: APIRequestLogTurnStatusCompleted, Attribution: APIRequestLogTurnAttributionExact,
			UserId: first.UserId, Username: first.Username, TokenId: first.TokenId, TokenName: first.TokenName,
			ModelName: first.ModelName, Exported: branchId != 0,
		},
		Requests: []APIRequestLogSessionRequest{}, Items: []APIRequestLogSessionItem{}, InternalTurns: turns,
	}
	for _, turn := range turns {
		detail.RequestCount += turn.RequestCount
		detail.ItemCount += turn.ItemCount
		detail.PromptTokens += turn.PromptTokens
		detail.CompletionTokens += turn.CompletionTokens
		detail.TokenUsed += turn.TokenUsed
		detail.Quota += turn.Quota
		if turn.StartedAt > 0 && (detail.StartedAt <= 0 || turn.StartedAt < detail.StartedAt) {
			detail.StartedAt = turn.StartedAt
		}
		if turn.CompletedAt > detail.CompletedAt {
			detail.CompletedAt = turn.CompletedAt
		}
		if turn.CompletionStatus != APIRequestLogTurnStatusCompleted {
			detail.CompletionStatus = APIRequestLogTurnStatusUnknown
			if turn.CompletionStatus == APIRequestLogTurnStatusOpen {
				detail.CompletionStatus = APIRequestLogTurnStatusOpen
			}
		}
		if turn.Attribution == APIRequestLogTurnAttributionUnknown {
			detail.Attribution = APIRequestLogTurnAttributionUnknown
		} else if turn.Attribution == APIRequestLogTurnAttributionInferred && detail.Attribution != APIRequestLogTurnAttributionUnknown {
			detail.Attribution = APIRequestLogTurnAttributionInferred
		}
		for _, request := range turn.Requests {
			detail.Requests = append(detail.Requests, APIRequestLogSessionRequest{
				LogId: request.LogId, RequestId: request.RequestId, UpstreamRequestId: request.UpstreamRequestId,
				CreatedAt: request.CreatedAt, StatusCode: request.StatusCode, IsStream: request.IsStream,
			})
		}
	}
	sort.SliceStable(detail.Requests, func(i, j int) bool {
		if detail.Requests[i].CreatedAt != detail.Requests[j].CreatedAt {
			return detail.Requests[i].CreatedAt < detail.Requests[j].CreatedAt
		}
		return detail.Requests[i].LogId < detail.Requests[j].LogId
	})
	for index := range detail.Requests {
		detail.Requests[index].Sequence = index + 1
	}
	detail.ContextLoaded = last.ContextLoaded
	detail.ContextComplete = last.ContextLoaded && last.ContextComplete
	detail.ContextOmittedItemCount = last.ContextOmittedItemCount
	detail.Items = apiRequestLogSessionSnapshotItems(last)
	return detail
}

func apiRequestLogSessionSnapshotItems(latest *APIRequestLogTurnDetail) []APIRequestLogSessionItem {
	if latest == nil {
		return []APIRequestLogSessionItem{}
	}
	type source struct {
		item    APIRequestLogTurnItemDetail
		context bool
	}
	merged := make([]source, 0, len(latest.ContextItems)+len(latest.Items))
	seen := make(map[int]int, len(latest.ContextItems))
	for _, item := range latest.ContextItems {
		if strings.Contains(strings.ToLower(strings.TrimSpace(item.ContentType)), "encrypted") {
			continue
		}
		if item.SourceItemId > 0 {
			seen[item.SourceItemId] = len(merged)
		}
		merged = append(merged, source{item: item, context: true})
	}
	for _, item := range latest.Items {
		if strings.Contains(strings.ToLower(strings.TrimSpace(item.ContentType)), "encrypted") {
			continue
		}
		if index, ok := seen[item.SourceItemId]; item.SourceItemId > 0 && ok {
			merged[index].item.ProviderItemId = item.ProviderItemId
			merged[index].item.MessagePhase = item.MessagePhase
			merged[index].item.ItemStatus = item.ItemStatus
			continue
		}
		if item.SourceItemId > 0 {
			seen[item.SourceItemId] = len(merged)
		}
		merged = append(merged, source{item: item})
	}
	items := make([]APIRequestLogSessionItem, 0, len(merged))
	for index, entry := range merged {
		item := entry.item
		items = append(items, APIRequestLogSessionItem{
			Sequence: index + 1, SourceItemId: item.SourceItemId, SourceSequence: item.SourceSeq,
			Phase: item.Phase, ItemType: item.ItemType, Role: item.Role, ContentType: item.ContentType,
			Content: item.Content, ToolCallId: item.ToolCallId, Name: item.Name, Source: item.Source,
			ProviderItemId: item.ProviderItemId, MessagePhase: item.MessagePhase, ItemStatus: item.ItemStatus,
			Redacted: item.Redacted, Truncated: item.Truncated, ContextSnapshot: entry.context,
		})
	}
	return items
}
