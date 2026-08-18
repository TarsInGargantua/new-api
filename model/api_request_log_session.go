package model

import (
	"errors"
	"sort"
	"strings"

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

func GetAPIRequestLogSessions(db *gorm.DB, params APIRequestLogTurnQueryParams) (items []*APIRequestLogSessionListItem, total int64, err error) {
	if db == nil {
		return nil, 0, errors.New("request log database is not initialized")
	}
	grouped := buildAPIRequestLogSessionGroupQuery(db, params)
	if err = db.Table("(?) AS request_log_sessions", grouped).Count(&total).Error; err != nil {
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
	var rows []apiRequestLogSessionAggregateRow
	if err = grouped.
		Order("MAX(" + apiRequestLogTurnsTable + ".completed_at) DESC").
		Order("MAX(" + apiRequestLogTurnsTable + ".id) DESC").
		Limit(limit).Offset(offset).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	items = make([]*APIRequestLogSessionListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, apiRequestLogSessionListItemFromAggregate(row))
	}
	return items, total, nil
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
	branchSQL := apiRequestLogSessionBranchSQL()
	return buildAPIRequestLogTurnsQuery(db, params).
		Select(
			"MIN(" + apiRequestLogTurnsTable + ".id) AS id, " +
				branchSQL + " AS export_batch_id, " +
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
		Group(branchSQL)
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
	return "COALESCE(NULLIF(" + apiRequestLogTurnsTable + ".export_batch_id, 0), " +
		"(SELECT MAX(session_branch_member.batch_id) FROM " + apiRequestLogExportMembersTable + " session_branch_member WHERE session_branch_member.turn_record_id = " + apiRequestLogTurnsTable + ".id), " +
		"CASE WHEN " + apiRequestLogTurnsTable + ".exported_version > 0 THEN -1 ELSE 0 END)"
}

func apiRequestLogSessionBranchForTurn(db *gorm.DB, turn *APIRequestLogTurn) (int64, error) {
	if turn == nil || turn.Id <= 0 {
		return 0, nil
	}
	if turn.ExportBatchId > 0 {
		return turn.ExportBatchId, nil
	}
	if turn.ExportedVersion > 0 {
		return -1, nil
	}
	var member APIRequestLogExportMember
	err := db.Select("batch_id").Where("turn_record_id = ?", turn.Id).First(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if member.BatchId > 0 {
		return member.BatchId, nil
	}
	return -1, nil
}

func applyAPIRequestLogSessionBranch(query *gorm.DB, branchId int64) *gorm.DB {
	switch {
	case branchId > 0:
		return query.Where("("+apiRequestLogTurnsTable+".export_batch_id = ? OR ("+apiRequestLogTurnsTable+".export_batch_id = 0 AND EXISTS (SELECT 1 FROM "+apiRequestLogExportMembersTable+" session_member WHERE session_member.turn_record_id = "+apiRequestLogTurnsTable+".id AND session_member.batch_id = ?)))", branchId, branchId)
	case branchId < 0:
		return query.Where(apiRequestLogTurnExportedSQL()).Where(apiRequestLogTurnsTable + ".export_batch_id = 0")
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
