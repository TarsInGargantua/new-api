package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	APIRequestLogTurnStatusOpen      = "open"
	APIRequestLogTurnStatusCompleted = "completed"
	APIRequestLogTurnStatusUnknown   = "unknown"

	APIRequestLogTurnAttributionExact    = "exact"
	APIRequestLogTurnAttributionInferred = "inferred"
	APIRequestLogTurnAttributionUnknown  = "unknown"
)

const (
	apiRequestLogTurnsTable        = "api_request_log_turns"
	apiRequestLogTurnRequestsTable = "api_request_log_turn_requests"
	apiRequestLogTurnItemsTable    = "api_request_log_turn_items"
	apiRequestLogTurnMaxRetries    = 3
)

// APIRequestLogTurnMeta is normalized provider metadata for one agent turn.
// StartedAt and CompletedAt accept Unix seconds or Unix milliseconds.
type APIRequestLogTurnMeta struct {
	SessionId        string                      `json:"session_id"`
	TurnId           string                      `json:"turn_id"`
	Protocol         string                      `json:"protocol"`
	StartedAt        int64                       `json:"started_at"`
	CompletedAt      int64                       `json:"completed_at"`
	CompletionStatus string                      `json:"completion_status"`
	CompletionSignal string                      `json:"completion_signal,omitempty"`
	Attribution      string                      `json:"attribution"`
	WindowId         string                      `json:"window_id,omitempty"`
	RequestKind      string                      `json:"request_kind,omitempty"`
	Items            []APIRequestLogTurnItemMeta `json:"items,omitempty"`
}

type APIRequestLogTurnItemMeta struct {
	Seq            int    `json:"seq"`
	ProviderItemId string `json:"provider_item_id,omitempty"`
	TurnId         string `json:"turn_id,omitempty"`
	MessagePhase   string `json:"message_phase,omitempty"`
	ItemStatus     string `json:"item_status,omitempty"`
}

type APIRequestLogTurn struct {
	Id                     int64  `json:"id" gorm:"primaryKey;index:idx_api_request_log_turn_owner_session_order,priority:4"`
	OwnerFingerprint       string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_api_request_log_turn_identity,priority:1;index:idx_api_request_log_turn_owner_session,priority:1;index:idx_api_request_log_turn_owner_session_order,priority:1"`
	SessionId              string `json:"session_id" gorm:"type:varchar(191);not null;uniqueIndex:idx_api_request_log_turn_identity,priority:2;index:idx_api_request_log_turn_session_index,priority:1;index:idx_api_request_log_turn_owner_session,priority:2;index:idx_api_request_log_turn_owner_session_order,priority:2"`
	TurnId                 string `json:"turn_id" gorm:"type:varchar(191);not null;uniqueIndex:idx_api_request_log_turn_identity,priority:3"`
	Protocol               string `json:"protocol" gorm:"type:varchar(32);index;default:'unknown'"`
	TurnIndex              int    `json:"turn_index" gorm:"index:idx_api_request_log_turn_session_index,priority:2;index:idx_api_request_log_turn_owner_session_order,priority:3;default:1"`
	WindowId               string `json:"window_id,omitempty" gorm:"type:varchar(191);index;default:''"`
	RequestKind            string `json:"request_kind,omitempty" gorm:"type:varchar(64);index;default:''"`
	StartedAt              int64  `json:"started_at" gorm:"bigint;index"`
	CompletedAt            int64  `json:"completed_at" gorm:"bigint;index"`
	CompletionStatus       string `json:"completion_status" gorm:"type:varchar(16);index;default:'unknown'"`
	CompletionSignal       string `json:"completion_signal,omitempty" gorm:"type:varchar(128);index;default:''"`
	Attribution            string `json:"attribution" gorm:"type:varchar(16);index;default:'unknown'"`
	UserId                 int    `json:"user_id" gorm:"index;default:0"`
	Username               string `json:"username" gorm:"index;default:''"`
	TokenId                int    `json:"token_id" gorm:"index;default:0"`
	TokenName              string `json:"token_name" gorm:"index;default:''"`
	TokenFingerprint       string `json:"token_fingerprint,omitempty" gorm:"type:varchar(64);index;default:''"`
	ModelName              string `json:"model_name" gorm:"index;default:''"`
	RequestCount           int    `json:"request_count" gorm:"default:0"`
	ItemCount              int    `json:"item_count" gorm:"default:0"`
	PromptTokens           int    `json:"prompt_tokens" gorm:"default:0"`
	CompletionTokens       int    `json:"completion_tokens" gorm:"default:0"`
	TokenUsed              int    `json:"token_used" gorm:"default:0"`
	Quota                  int    `json:"quota" gorm:"default:0"`
	MaterializationVersion int64  `json:"materialization_version" gorm:"not null;default:0"`
	ExportedVersion        int64  `json:"exported_version" gorm:"not null;default:0;index"`
}

func (APIRequestLogTurn) TableName() string { return apiRequestLogTurnsTable }

type APIRequestLogTurnSessionState struct {
	Id               int64  `json:"id" gorm:"primaryKey"`
	OwnerFingerprint string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_api_request_log_turn_session_state,priority:1"`
	SessionId        string `json:"session_id" gorm:"type:varchar(191);not null;uniqueIndex:idx_api_request_log_turn_session_state,priority:2"`
	NextTurnIndex    int    `json:"next_turn_index" gorm:"not null;default:1"`
	CreatedAt        int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt        int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

func (APIRequestLogTurnSessionState) TableName() string {
	return "api_request_log_turn_session_states"
}

type APIRequestLogTurnRequest struct {
	Id                 int64             `json:"id" gorm:"primaryKey;index:idx_api_request_log_turn_request_order,priority:4"`
	TurnRecordId       int64             `json:"turn_record_id" gorm:"not null;index:idx_api_request_log_turn_request_sequence,priority:1;index:idx_api_request_log_turn_request_order,priority:1"`
	LogId              int               `json:"log_id" gorm:"not null;uniqueIndex;index:idx_api_request_log_turn_request_order,priority:3"`
	Sequence           int               `json:"sequence" gorm:"index:idx_api_request_log_turn_request_sequence,priority:2"`
	RequestId          string            `json:"request_id,omitempty" gorm:"type:varchar(64);index;default:''"`
	UpstreamRequestId  string            `json:"upstream_request_id,omitempty" gorm:"type:varchar(128);index;default:''"`
	CreatedAt          int64             `json:"created_at" gorm:"bigint;index;index:idx_api_request_log_turn_request_order,priority:2"`
	StatusCode         int               `json:"status_code" gorm:"default:0"`
	IsStream           bool              `json:"is_stream"`
	PromptTokens       int               `json:"prompt_tokens" gorm:"default:0"`
	CompletionTokens   int               `json:"completion_tokens" gorm:"default:0"`
	TokenUsed          int               `json:"token_used" gorm:"default:0"`
	Quota              int               `json:"quota" gorm:"default:0"`
	InputFingerprint   APIRequestLogBody `json:"-"`
	ContextFingerprint APIRequestLogBody `json:"-"`
}

func (APIRequestLogTurnRequest) TableName() string { return apiRequestLogTurnRequestsTable }

// APIRequestLogTurnItem only maps a canonical turn item to its normalized source row.
// Content remains in api_request_log_items and is loaded only for detail/export reads.
type APIRequestLogTurnItem struct {
	Id              int64  `json:"id" gorm:"primaryKey"`
	TurnRecordId    int64  `json:"turn_record_id" gorm:"not null;uniqueIndex:idx_api_request_log_turn_item_key,priority:1;index:idx_api_request_log_turn_item_ordinal,priority:1"`
	RequestRecordId int64  `json:"request_record_id" gorm:"not null;index"`
	SourceItemId    int    `json:"source_item_id" gorm:"not null;uniqueIndex"`
	Ordinal         int    `json:"ordinal" gorm:"uniqueIndex:idx_api_request_log_turn_item_ordinal,priority:2"`
	CanonicalKey    string `json:"canonical_key" gorm:"type:char(64);not null;uniqueIndex:idx_api_request_log_turn_item_key,priority:2"`
	ProviderItemId  string `json:"provider_item_id,omitempty" gorm:"type:varchar(191);index;default:''"`
	MessagePhase    string `json:"message_phase,omitempty" gorm:"type:varchar(32);index;default:''"`
	ItemStatus      string `json:"item_status,omitempty" gorm:"type:varchar(32);index;default:''"`
}

func (APIRequestLogTurnItem) TableName() string { return apiRequestLogTurnItemsTable }

type APIRequestLogTurnItemDetail struct {
	Id              int64             `json:"id"`
	TurnRecordId    int64             `json:"turn_record_id"`
	RequestRecordId int64             `json:"request_record_id"`
	SourceItemId    int               `json:"source_item_id"`
	Ordinal         int               `json:"ordinal"`
	CanonicalKey    string            `json:"canonical_key"`
	ProviderItemId  string            `json:"provider_item_id,omitempty"`
	MessagePhase    string            `json:"message_phase,omitempty"`
	ItemStatus      string            `json:"item_status,omitempty"`
	LogId           int               `json:"log_id"`
	SourceSeq       int               `json:"source_seq"`
	Phase           string            `json:"phase"`
	ItemType        string            `json:"item_type"`
	Role            string            `json:"role,omitempty"`
	ContentType     string            `json:"content_type"`
	Content         APIRequestLogBody `json:"content,omitempty"`
	ToolCallId      string            `json:"tool_call_id,omitempty"`
	Name            string            `json:"name,omitempty"`
	Source          string            `json:"source,omitempty"`
	Redacted        bool              `json:"redacted"`
	Truncated       bool              `json:"truncated"`
}

type APIRequestLogTurnDetail struct {
	APIRequestLogTurn
	Requests  []APIRequestLogTurnRequest    `json:"requests"`
	Items     []APIRequestLogTurnItemDetail `json:"items"`
	Exported  bool                          `json:"exported"`
	ExportTag string                        `json:"export_tag,omitempty"`
}

type APIRequestLogTurnListItem struct {
	APIRequestLogTurn
	Exported bool `json:"exported"`
}

type APIRequestLogTurnQueryParams struct {
	StartTimestamp     int64
	EndTimestamp       int64
	SessionId          string
	TurnId             string
	Protocol           string
	Protocols          []string
	ModelName          string
	ModelNames         []string
	Username           string
	Usernames          []string
	TokenName          string
	CompletionStatus   string
	CompletionStatuses []string
	Attribution        string
	Attributions       []string
	Exported           *bool
	StartIdx           int
	Num                int
}

type APIRequestLogTurnFilterOptions struct {
	ModelNames         []string `json:"model_names"`
	Usernames          []string `json:"usernames"`
	Protocols          []string `json:"protocols"`
	CompletionStatuses []string `json:"completion_statuses"`
	Attributions       []string `json:"attributions"`
}

func EnsureAPIRequestLogTurnTables(db *gorm.DB) error {
	if db == nil {
		return errors.New("request log database is not initialized")
	}
	return db.AutoMigrate(&APIRequestLogTurn{}, &APIRequestLogTurnRequest{}, &APIRequestLogTurnItem{}, &APIRequestLogTurnSessionState{})
}

// MaterializeAPIRequestLogTurn idempotently associates one raw request and its
// source items with a session turn. Items without a persisted source item ID are
// ignored and may be supplied again after the async raw-item writer completes.
func MaterializeAPIRequestLogTurn(db *gorm.DB, log *APIRequestLog, meta APIRequestLogTurnMeta, items []APIRequestLogItem) (*APIRequestLogTurn, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	if log == nil || log.Id <= 0 {
		return nil, errors.New("persisted request log is required")
	}
	var err error
	meta, err = inheritAPIRequestLogTurnMetaForLog(db, log.Id, meta)
	if err != nil {
		return nil, err
	}
	resolvedItems, err := resolveAPIRequestLogSourceItems(db, log.Id, items)
	if err != nil {
		return nil, err
	}
	meta = normalizeAPIRequestLogTurnMeta(log, meta)
	candidates := buildAPIRequestLogTurnCandidates(resolvedItems, meta)
	inputKeys, contextKeys := apiRequestLogTurnCandidateFingerprints(candidates)
	var ensuredTurn *APIRequestLogTurn
	for attempt := 0; attempt < apiRequestLogTurnMaxRetries; attempt++ {
		ensuredTurn, err = ensureAPIRequestLogTurn(db, log, meta)
		if err == nil {
			break
		}
		if !isRetryableAPIRequestLogDBError(err) || attempt == apiRequestLogTurnMaxRetries-1 {
			return nil, err
		}
		delay := time.Duration((attempt+1)*10+(log.Id%17)) * time.Millisecond
		time.Sleep(delay)
	}

	var result APIRequestLogTurn
	for attempt := 0; attempt < apiRequestLogTurnMaxRetries; attempt++ {
		result = APIRequestLogTurn{}
		err = db.Transaction(func(tx *gorm.DB) error {
			turn := &APIRequestLogTurn{Id: ensuredTurn.Id}
			if err := lockAPIRequestLogTurnByID(tx, ensuredTurn.Id, turn); err != nil {
				return err
			}
			result = *turn
			exported, err := apiRequestLogTurnIsFrozen(tx, turn)
			if err != nil {
				return err
			}
			if exported {
				result = *turn
				return nil
			}
			priorInput, priorContext, err := latestAPIRequestLogTurnFingerprints(tx, turn.OwnerFingerprint, turn.SessionId, normalizeAPIRequestLogTurnTimestamp(log.CreatedAt), log.Id)
			if err != nil {
				return err
			}
			prefixLength := longestAPIRequestLogTurnPrefix(inputKeys, priorInput, priorContext)

			request, err := upsertAPIRequestLogTurnRequest(tx, turn.Id, log, inputKeys, contextKeys)
			if err != nil {
				return err
			}
			if err := mapAPIRequestLogTurnItems(tx, turn.Id, request.Id, candidates, prefixLength); err != nil {
				return err
			}
			if err := refreshAPIRequestLogTurn(tx, turn, log, meta); err != nil {
				return err
			}
			result = *turn
			return nil
		})
		if err == nil {
			return &result, nil
		}
		if !isRetryableAPIRequestLogDBError(err) || attempt == apiRequestLogTurnMaxRetries-1 {
			return nil, err
		}
		delay := time.Duration((attempt+1)*10+(log.Id%17)) * time.Millisecond
		time.Sleep(delay)
	}
	return nil, err
}

func ensureAPIRequestLogTurn(db *gorm.DB, log *APIRequestLog, meta APIRequestLogTurnMeta) (*APIRequestLogTurn, error) {
	ownerFingerprint := apiRequestLogOwnerFingerprint(log)
	var existing APIRequestLogTurn
	err := db.Where("owner_fingerprint = ? AND session_id = ? AND turn_id = ?", ownerFingerprint, meta.SessionId, meta.TurnId).First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var result APIRequestLogTurn
	err = db.Transaction(func(tx *gorm.DB) error {
		existing = APIRequestLogTurn{}
		lookup := tx.Where("owner_fingerprint = ? AND session_id = ? AND turn_id = ?", ownerFingerprint, meta.SessionId, meta.TurnId)
		if tx.Dialector != nil && tx.Dialector.Name() != "sqlite" {
			lookup = lookup.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		err := lookup.First(&existing).Error
		if err == nil {
			result = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		turnIndex, err := allocateAPIRequestLogTurnIndex(tx, ownerFingerprint, meta.SessionId)
		if err != nil {
			return err
		}
		turn := newAPIRequestLogTurn(log, meta, ownerFingerprint, turnIndex)
		create := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "owner_fingerprint"}, {Name: "session_id"}, {Name: "turn_id"}},
			DoNothing: true,
		}).Create(&turn)
		if create.Error != nil {
			return create.Error
		}
		if create.RowsAffected > 0 {
			result = turn
			return nil
		}
		return tx.Where("owner_fingerprint = ? AND session_id = ? AND turn_id = ?", ownerFingerprint, meta.SessionId, meta.TurnId).First(&result).Error
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func allocateAPIRequestLogTurnIndex(tx *gorm.DB, ownerFingerprint, sessionId string) (int, error) {
	var state APIRequestLogTurnSessionState
	query := tx.Where("owner_fingerprint = ? AND session_id = ?", ownerFingerprint, sessionId)
	if tx.Dialector != nil && tx.Dialector.Name() != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var maxIndex int
		if err := tx.Model(&APIRequestLogTurn{}).
			Where("owner_fingerprint = ? AND session_id = ?", ownerFingerprint, sessionId).
			Select("COALESCE(MAX(turn_index), 0)").Scan(&maxIndex).Error; err != nil {
			return 0, err
		}
		allocated := maxIndex + 1
		state = APIRequestLogTurnSessionState{
			OwnerFingerprint: ownerFingerprint,
			SessionId:        sessionId,
			NextTurnIndex:    allocated + 1,
		}
		create := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "owner_fingerprint"}, {Name: "session_id"}},
			DoNothing: true,
		}).Create(&state)
		if create.Error != nil {
			return 0, create.Error
		}
		if create.RowsAffected > 0 {
			return allocated, nil
		}
		state = APIRequestLogTurnSessionState{}
		reload := tx.Where("owner_fingerprint = ? AND session_id = ?", ownerFingerprint, sessionId)
		if tx.Dialector != nil && tx.Dialector.Name() != "sqlite" {
			reload = reload.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := reload.First(&state).Error; err != nil {
			return 0, err
		}
		allocated = state.NextTurnIndex
		if allocated < 1 {
			allocated = 1
		}
		if err := tx.Model(&APIRequestLogTurnSessionState{}).
			Where("id = ?", state.Id).
			Update("next_turn_index", allocated+1).Error; err != nil {
			return 0, err
		}
		return allocated, nil
	}
	if err != nil {
		return 0, err
	}
	allocated := state.NextTurnIndex
	if allocated < 1 {
		allocated = 1
	}
	if err := tx.Model(&APIRequestLogTurnSessionState{}).
		Where("id = ?", state.Id).
		Update("next_turn_index", allocated+1).Error; err != nil {
		return 0, err
	}
	return allocated, nil
}

func newAPIRequestLogTurn(log *APIRequestLog, meta APIRequestLogTurnMeta, ownerFingerprint string, turnIndex int) APIRequestLogTurn {
	return APIRequestLogTurn{
		OwnerFingerprint: ownerFingerprint,
		SessionId:        meta.SessionId,
		TurnId:           meta.TurnId,
		Protocol:         meta.Protocol,
		TurnIndex:        turnIndex,
		WindowId:         meta.WindowId,
		RequestKind:      meta.RequestKind,
		StartedAt:        meta.StartedAt,
		CompletedAt:      meta.CompletedAt,
		CompletionStatus: meta.CompletionStatus,
		CompletionSignal: meta.CompletionSignal,
		Attribution:      meta.Attribution,
		UserId:           log.UserId,
		Username:         log.Username,
		TokenId:          log.TokenId,
		TokenName:        log.TokenName,
		TokenFingerprint: apiRequestLogTokenFingerprint(log),
		ModelName:        log.ModelName,
	}
}

func lockAPIRequestLogTurnByID(tx *gorm.DB, turnId int64, turn *APIRequestLogTurn) error {
	query := tx.Where("id = ?", turnId)
	if tx.Dialector != nil && tx.Dialector.Name() != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return query.First(turn).Error
}

func inheritAPIRequestLogTurnMetaForLog(db *gorm.DB, logId int, meta APIRequestLogTurnMeta) (APIRequestLogTurnMeta, error) {
	var existing APIRequestLogTurn
	err := db.Table(apiRequestLogTurnsTable+" turn_row").
		Select("turn_row.*").
		Joins("JOIN "+apiRequestLogTurnRequestsTable+" turn_request ON turn_request.turn_record_id = turn_row.id").
		Where("turn_request.log_id = ?", logId).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return meta, nil
	}
	if err != nil {
		return meta, err
	}
	if strings.TrimSpace(meta.SessionId) == "" {
		meta.SessionId = existing.SessionId
		meta.TurnId = existing.TurnId
	} else if strings.TrimSpace(meta.SessionId) == existing.SessionId && strings.TrimSpace(meta.TurnId) == "" {
		meta.TurnId = existing.TurnId
	}
	if strings.TrimSpace(meta.Protocol) == "" {
		meta.Protocol = existing.Protocol
	}
	if meta.StartedAt <= 0 {
		meta.StartedAt = existing.StartedAt
	}
	if meta.CompletedAt <= 0 {
		meta.CompletedAt = existing.CompletedAt
	}
	if strings.TrimSpace(meta.CompletionStatus) == "" {
		meta.CompletionStatus = existing.CompletionStatus
	}
	if strings.TrimSpace(meta.CompletionSignal) == "" {
		meta.CompletionSignal = existing.CompletionSignal
	}
	if strings.TrimSpace(meta.Attribution) == "" {
		meta.Attribution = existing.Attribution
	}
	if strings.TrimSpace(meta.WindowId) == "" {
		meta.WindowId = existing.WindowId
	}
	if strings.TrimSpace(meta.RequestKind) == "" {
		meta.RequestKind = existing.RequestKind
	}
	return meta, nil
}

func GetAPIRequestLogTurns(db *gorm.DB, params APIRequestLogTurnQueryParams) (items []*APIRequestLogTurnListItem, total int64, err error) {
	if db == nil {
		return nil, 0, errors.New("request log database is not initialized")
	}
	tx := buildAPIRequestLogTurnsQuery(db, params)
	if err = tx.Count(&total).Error; err != nil {
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
	var turns []APIRequestLogTurn
	if err = tx.Order("CASE WHEN completed_at > 0 THEN completed_at ELSE started_at END DESC").Order("id DESC").Limit(limit).Offset(offset).Find(&turns).Error; err != nil {
		return nil, 0, err
	}
	exported, err := apiRequestLogExportedTurnIds(db, apiRequestLogTurnIds(turns))
	if err != nil {
		return nil, 0, err
	}
	items = make([]*APIRequestLogTurnListItem, 0, len(turns))
	for i := range turns {
		items = append(items, &APIRequestLogTurnListItem{APIRequestLogTurn: turns[i], Exported: exported[turns[i].Id]})
	}
	return items, total, nil
}

func GetAPIRequestLogTurnById(db *gorm.DB, id int64) (*APIRequestLogTurnDetail, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	if id <= 0 {
		return nil, errors.New("invalid request log turn id")
	}
	details, err := getAPIRequestLogTurnDetailsByIds(db, []int64{id})
	if err != nil {
		return nil, err
	}
	if len(details) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return details[0], nil
}

func GetAPIRequestLogTurnFilterOptions(db *gorm.DB, limit int) (*APIRequestLogTurnFilterOptions, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	options := &APIRequestLogTurnFilterOptions{}
	queries := []struct {
		column string
		target interface{}
	}{
		{"model_name", &options.ModelNames},
		{"username", &options.Usernames},
		{"protocol", &options.Protocols},
		{"completion_status", &options.CompletionStatuses},
		{"attribution", &options.Attributions},
	}
	for _, query := range queries {
		if err := db.Model(&APIRequestLogTurn{}).Distinct(query.column).Where(query.column+" <> ?", "").Order(query.column+" ASC").Limit(limit).Pluck(query.column, query.target).Error; err != nil {
			return nil, err
		}
	}
	return options, nil
}

func buildAPIRequestLogTurnsQuery(db *gorm.DB, params APIRequestLogTurnQueryParams) *gorm.DB {
	tx := db.Model(&APIRequestLogTurn{})
	if len(params.ModelNames) > 0 {
		tx = tx.Where("model_name IN ?", uniqueNonEmptyStrings(params.ModelNames))
	} else {
		tx = applyLogContainsFilter(tx, "model_name", params.ModelName)
	}
	if len(params.Usernames) > 0 {
		tx = tx.Where("username IN ?", uniqueNonEmptyStrings(params.Usernames))
	} else {
		tx = applyLogContainsFilter(tx, "username", params.Username)
	}
	tx = applyLogContainsFilter(tx, "token_name", params.TokenName)
	tx = applyLogContainsFilter(tx, "session_id", params.SessionId)
	tx = applyLogContainsFilter(tx, "turn_id", params.TurnId)
	if len(params.Protocols) > 0 {
		tx = tx.Where("protocol IN ?", uniqueNonEmptyStrings(params.Protocols))
	} else if value := strings.TrimSpace(params.Protocol); value != "" {
		tx = tx.Where("protocol = ?", value)
	}
	if len(params.CompletionStatuses) > 0 {
		tx = tx.Where("completion_status IN ?", uniqueNonEmptyStrings(params.CompletionStatuses))
	} else if value := strings.TrimSpace(params.CompletionStatus); value != "" {
		tx = tx.Where("completion_status = ?", value)
	}
	if len(params.Attributions) > 0 {
		tx = tx.Where("attribution IN ?", uniqueNonEmptyStrings(params.Attributions))
	} else if value := strings.TrimSpace(params.Attribution); value != "" {
		tx = tx.Where("attribution = ?", value)
	}
	if params.StartTimestamp > 0 {
		tx = tx.Where("completed_at >= ?", normalizeAPIRequestLogTurnTimestamp(params.StartTimestamp))
	}
	if params.EndTimestamp > 0 {
		tx = tx.Where("completed_at < ?", normalizeAPIRequestLogTurnTimestamp(params.EndTimestamp))
	}
	if params.Exported != nil {
		if *params.Exported {
			tx = tx.Where(apiRequestLogTurnExportedSQL())
		} else {
			tx = tx.Where("NOT " + apiRequestLogTurnExportedSQL())
		}
	}
	return tx
}

func normalizeAPIRequestLogTurnMeta(log *APIRequestLog, meta APIRequestLogTurnMeta) APIRequestLogTurnMeta {
	meta.SessionId = strings.TrimSpace(meta.SessionId)
	meta.TurnId = strings.TrimSpace(meta.TurnId)
	meta.Protocol = strings.ToLower(strings.TrimSpace(meta.Protocol))
	meta.WindowId = strings.TrimSpace(meta.WindowId)
	meta.RequestKind = strings.TrimSpace(meta.RequestKind)
	meta.CompletionSignal = strings.TrimSpace(meta.CompletionSignal)
	meta.StartedAt = normalizeAPIRequestLogTurnTimestamp(meta.StartedAt)
	meta.CompletedAt = normalizeAPIRequestLogTurnTimestamp(meta.CompletedAt)
	if meta.Protocol == "" {
		meta.Protocol = "unknown"
	}
	meta.CompletionStatus = normalizeAPIRequestLogTurnStatus(meta.CompletionStatus)
	meta.Attribution = normalizeAPIRequestLogTurnAttribution(meta.Attribution)
	if meta.TurnId == "" {
		for _, itemMeta := range meta.Items {
			if strings.TrimSpace(itemMeta.TurnId) == "" {
				continue
			}
			if IsAPIRequestLogFinalMessagePhase(itemMeta.MessagePhase) && strings.EqualFold(strings.TrimSpace(itemMeta.ItemStatus), "completed") {
				meta.TurnId = strings.TrimSpace(itemMeta.TurnId)
				break
			}
			if meta.TurnId == "" {
				meta.TurnId = strings.TrimSpace(itemMeta.TurnId)
			}
		}
	}
	for _, itemMeta := range meta.Items {
		if strings.TrimSpace(itemMeta.TurnId) != "" && strings.TrimSpace(itemMeta.TurnId) != meta.TurnId {
			continue
		}
		if IsAPIRequestLogFinalMessagePhase(itemMeta.MessagePhase) && strings.EqualFold(strings.TrimSpace(itemMeta.ItemStatus), "completed") {
			meta.CompletionStatus = APIRequestLogTurnStatusCompleted
			meta.CompletionSignal = strongerAPIRequestLogTurnCompletionSignal(meta.CompletionSignal, "message.final.completed")
			break
		}
	}
	if meta.StartedAt <= 0 {
		meta.StartedAt = normalizeAPIRequestLogTurnTimestamp(log.CreatedAt)
	}
	if meta.CompletionStatus == APIRequestLogTurnStatusCompleted && meta.CompletedAt <= 0 {
		meta.CompletedAt = normalizeAPIRequestLogTurnTimestamp(log.CreatedAt)
	}
	if meta.CompletionStatus != APIRequestLogTurnStatusCompleted {
		meta.CompletedAt = 0
	}
	if !apiRequestLogHasStableOwner(log) {
		meta.CompletionStatus = APIRequestLogTurnStatusUnknown
		meta.CompletedAt = 0
		meta.Attribution = APIRequestLogTurnAttributionUnknown
	}
	identitySuffix := fmt.Sprintf("%d", log.Id)
	missingSession := meta.SessionId == ""
	if missingSession {
		meta.SessionId = "unknown-session:" + identitySuffix
		meta.Attribution = APIRequestLogTurnAttributionUnknown
		meta.CompletionStatus = APIRequestLogTurnStatusUnknown
		meta.CompletionSignal = ""
		meta.CompletedAt = 0
	}
	if meta.TurnId == "" {
		if !missingSession && meta.Attribution == APIRequestLogTurnAttributionInferred {
			meta.TurnId = "inferred-turn:" + identitySuffix
		} else {
			meta.TurnId = "unknown-turn:" + identitySuffix
			meta.Attribution = APIRequestLogTurnAttributionUnknown
		}
	}
	return meta
}

func normalizeAPIRequestLogTurnTimestamp(value int64) int64 {
	if value >= 1_000_000_000_000_000 {
		return value / 1_000_000
	}
	if value >= 1_000_000_000_000 {
		return value / 1_000
	}
	return value
}

func normalizeAPIRequestLogTurnStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case APIRequestLogTurnStatusOpen:
		return APIRequestLogTurnStatusOpen
	case APIRequestLogTurnStatusCompleted:
		return APIRequestLogTurnStatusCompleted
	default:
		return APIRequestLogTurnStatusUnknown
	}
}

func IsAPIRequestLogFinalMessagePhase(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "final", "final_answer":
		return true
	default:
		return false
	}
}

func normalizeAPIRequestLogTurnAttribution(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case APIRequestLogTurnAttributionExact:
		return APIRequestLogTurnAttributionExact
	case APIRequestLogTurnAttributionInferred:
		return APIRequestLogTurnAttributionInferred
	default:
		return APIRequestLogTurnAttributionUnknown
	}
}

func findOrCreateAPIRequestLogTurn(tx *gorm.DB, log *APIRequestLog, meta APIRequestLogTurnMeta) (*APIRequestLogTurn, bool, error) {
	ownerFingerprint := apiRequestLogOwnerFingerprint(log)
	var turn APIRequestLogTurn
	err := tx.Where("owner_fingerprint = ? AND session_id = ? AND turn_id = ?", ownerFingerprint, meta.SessionId, meta.TurnId).First(&turn).Error
	if err == nil {
		if err := lockAPIRequestLogTurnByIdentity(tx, ownerFingerprint, meta.SessionId, meta.TurnId, &turn); err != nil {
			return nil, false, err
		}
		return &turn, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	turnIndex, err := allocateAPIRequestLogTurnIndex(tx, ownerFingerprint, meta.SessionId)
	if err != nil {
		return nil, false, err
	}
	turn = newAPIRequestLogTurn(log, meta, ownerFingerprint, turnIndex)
	result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "owner_fingerprint"}, {Name: "session_id"}, {Name: "turn_id"}}, DoNothing: true}).Create(&turn)
	if result.Error != nil {
		return nil, false, result.Error
	}
	created := result.RowsAffected > 0
	if created {
		return &turn, true, nil
	}
	turn = APIRequestLogTurn{}
	if err := lockAPIRequestLogTurnByIdentity(tx, ownerFingerprint, meta.SessionId, meta.TurnId, &turn); err != nil {
		return nil, false, err
	}
	return &turn, created, nil
}

func lockAPIRequestLogTurnByIdentity(tx *gorm.DB, ownerFingerprint, sessionId, turnId string, turn *APIRequestLogTurn) error {
	query := tx.Where("owner_fingerprint = ? AND session_id = ? AND turn_id = ?", ownerFingerprint, sessionId, turnId)
	if tx.Dialector != nil && tx.Dialector.Name() != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return query.First(turn).Error
}

func apiRequestLogTurnHasExportMember(tx *gorm.DB, turnRecordId int64) (bool, error) {
	if turnRecordId <= 0 {
		return false, nil
	}
	var count int64
	if err := tx.Model(&APIRequestLogExportMember{}).Where("turn_record_id = ?", turnRecordId).Limit(1).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func apiRequestLogTurnIsFrozen(tx *gorm.DB, turn *APIRequestLogTurn) (bool, error) {
	if turn == nil || turn.Id <= 0 {
		return false, nil
	}
	if turn.ExportedVersion > 0 {
		return true, nil
	}
	return apiRequestLogTurnHasExportMember(tx, turn.Id)
}

func resolveAPIRequestLogSourceItems(db *gorm.DB, logId int, items []APIRequestLogItem) ([]APIRequestLogItem, error) {
	needsReload := len(items) == 0
	for i := range items {
		if items[i].Id <= 0 {
			needsReload = true
			break
		}
	}
	if needsReload {
		var stored []APIRequestLogItem
		if err := db.Where("log_id = ?", logId).Order("seq ASC").Order("id ASC").Find(&stored).Error; err != nil {
			return nil, err
		}
		if len(stored) > 0 || len(items) == 0 {
			items = stored
		}
	}
	resolved := append([]APIRequestLogItem(nil), items...)
	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].Seq != resolved[j].Seq {
			return resolved[i].Seq < resolved[j].Seq
		}
		return resolved[i].Id < resolved[j].Id
	})
	return resolved, nil
}

type apiRequestLogTurnCandidate struct {
	item           APIRequestLogItem
	canonicalKey   string
	providerItemId string
	messagePhase   string
	itemStatus     string
	inputIndex     int
}

func buildAPIRequestLogTurnCandidates(items []APIRequestLogItem, meta APIRequestLogTurnMeta) []apiRequestLogTurnCandidate {
	candidates := make([]apiRequestLogTurnCandidate, 0, len(items))
	itemMetaBySeq := make(map[int]APIRequestLogTurnItemMeta, len(meta.Items))
	for _, itemMeta := range meta.Items {
		itemMetaBySeq[itemMeta.Seq] = itemMeta
	}
	inputIndex := 0
	semanticOccurrences := make(map[string]int)
	retainedTurnContext := make(map[string]bool)
	for _, item := range items {
		if !apiRequestLogTurnItemAllowed(item) {
			continue
		}
		itemMeta := itemMetaBySeq[item.Seq]
		if itemMeta.TurnId != "" && itemMeta.TurnId != meta.TurnId && !apiRequestLogTurnRetainEveryTurn(item) {
			continue
		}
		messagePhase, itemStatus := apiRequestLogTurnItemMeta(item)
		if value := strings.TrimSpace(itemMeta.MessagePhase); value != "" {
			messagePhase = value
		}
		if value := strings.TrimSpace(itemMeta.ItemStatus); value != "" {
			itemStatus = value
		}
		retainEveryTurn := apiRequestLogTurnRetainEveryTurn(item)
		canonicalKey, stableIdentity := apiRequestLogTurnItemCanonicalIdentity(item, itemMeta.ProviderItemId)
		if retainEveryTurn {
			canonicalKey = apiRequestLogTurnItemSemanticKey(item)
			if retainedTurnContext[canonicalKey] {
				continue
			}
			retainedTurnContext[canonicalKey] = true
		} else if !stableIdentity {
			semanticOccurrences[canonicalKey]++
			canonicalKey = apiRequestLogSHA256("occurrence\x00" + canonicalKey + "\x00" + strconv.Itoa(semanticOccurrences[canonicalKey]))
		}
		candidate := apiRequestLogTurnCandidate{
			item:           item,
			canonicalKey:   canonicalKey,
			providerItemId: strings.TrimSpace(itemMeta.ProviderItemId),
			messagePhase:   messagePhase,
			itemStatus:     itemStatus,
			inputIndex:     -1,
		}
		if item.Phase == APIRequestLogPhaseInput {
			candidate.inputIndex = inputIndex
			inputIndex++
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}

func apiRequestLogTurnCandidateFingerprints(candidates []apiRequestLogTurnCandidate) ([]string, []string) {
	input := make([]string, 0, len(candidates))
	context := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		context = append(context, candidate.canonicalKey)
		if candidate.inputIndex >= 0 {
			input = append(input, candidate.canonicalKey)
		}
	}
	return input, context
}

func latestAPIRequestLogTurnFingerprints(tx *gorm.DB, ownerFingerprint, sessionId string, currentCreatedAt int64, currentLogId int) ([]string, []string, error) {
	var request APIRequestLogTurnRequest
	err := tx.Table(apiRequestLogTurnRequestsTable+" turn_request").
		Select("turn_request.*").
		Joins("JOIN "+apiRequestLogTurnsTable+" turn_row ON turn_row.id = turn_request.turn_record_id").
		Where("turn_row.owner_fingerprint = ? AND turn_row.session_id = ?", ownerFingerprint, sessionId).
		Where("(turn_request.created_at < ?) OR (turn_request.created_at = ? AND turn_request.log_id < ?)", currentCreatedAt, currentCreatedAt, currentLogId).
		Order("turn_request.created_at DESC").Order("turn_request.log_id DESC").First(&request).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	input, err := unmarshalAPIRequestLogTurnFingerprints(request.InputFingerprint)
	if err != nil {
		return nil, nil, err
	}
	context, err := unmarshalAPIRequestLogTurnFingerprints(request.ContextFingerprint)
	if err != nil {
		return nil, nil, err
	}
	return input, context, nil
}

func longestAPIRequestLogTurnPrefix(current, priorInput, priorContext []string) int {
	commonPrefix := func(prior []string) int {
		limit := len(current)
		if len(prior) < limit {
			limit = len(prior)
		}
		index := 0
		for index < limit && current[index] == prior[index] {
			index++
		}
		return index
	}
	inputPrefix := commonPrefix(priorInput)
	contextPrefix := commonPrefix(priorContext)
	if contextPrefix > inputPrefix {
		return contextPrefix
	}
	return inputPrefix
}

func upsertAPIRequestLogTurnRequest(tx *gorm.DB, turnRecordId int64, log *APIRequestLog, inputKeys, contextKeys []string) (*APIRequestLogTurnRequest, error) {
	inputJSON, err := common.Marshal(inputKeys)
	if err != nil {
		return nil, err
	}
	contextJSON, err := common.Marshal(contextKeys)
	if err != nil {
		return nil, err
	}
	var request APIRequestLogTurnRequest
	err = tx.Where("log_id = ?", log.Id).First(&request).Error
	needsReindex := false
	isNew := false
	previousCreatedAt := request.CreatedAt
	if errors.Is(err, gorm.ErrRecordNotFound) {
		request = APIRequestLogTurnRequest{LogId: log.Id, TurnRecordId: turnRecordId}
		isNew = true
	} else if err != nil {
		return nil, err
	} else if request.TurnRecordId != turnRecordId {
		return nil, fmt.Errorf("request log %d is already assigned to turn %d", log.Id, request.TurnRecordId)
	}
	request.RequestId = log.RequestId
	request.UpstreamRequestId = log.UpstreamRequestId
	request.CreatedAt = normalizeAPIRequestLogTurnTimestamp(log.CreatedAt)
	request.StatusCode = log.StatusCode
	request.IsStream = log.IsStream
	request.PromptTokens = log.PromptTokens
	request.CompletionTokens = log.CompletionTokens
	request.TokenUsed = log.TokenUsed
	request.Quota = log.Quota
	request.InputFingerprint = APIRequestLogBody(inputJSON)
	request.ContextFingerprint = APIRequestLogBody(contextJSON)
	if request.Id == 0 {
		var last APIRequestLogTurnRequest
		result := tx.Where("turn_record_id = ?", turnRecordId).
			Order("created_at DESC").Order("log_id DESC").Order("id DESC").
			Limit(1).Find(&last)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			request.Sequence = 1
		} else if last.Sequence > 0 && (request.CreatedAt > last.CreatedAt || (request.CreatedAt == last.CreatedAt && request.LogId > last.LogId)) {
			request.Sequence = last.Sequence + 1
		} else {
			needsReindex = true
		}
		if err := tx.Create(&request).Error; err != nil {
			return nil, err
		}
	} else if err := tx.Save(&request).Error; err != nil {
		return nil, err
	}
	if !isNew && previousCreatedAt != request.CreatedAt {
		needsReindex = true
	}
	if needsReindex {
		if err := reindexAPIRequestLogTurnRequests(tx, turnRecordId); err != nil {
			return nil, err
		}
		if err := tx.First(&request, request.Id).Error; err != nil {
			return nil, err
		}
	}
	return &request, nil
}

func reindexAPIRequestLogTurnRequests(tx *gorm.DB, turnRecordId int64) error {
	var requests []APIRequestLogTurnRequest
	if err := tx.Where("turn_record_id = ?", turnRecordId).
		Order("created_at ASC").Order("log_id ASC").Order("id ASC").Find(&requests).Error; err != nil {
		return err
	}
	needsReindex := false
	minSequence := 0
	for index := range requests {
		if requests[index].Sequence != index+1 {
			needsReindex = true
		}
		if requests[index].Sequence < minSequence {
			minSequence = requests[index].Sequence
		}
	}
	if !needsReindex {
		return nil
	}
	temporaryStart := minSequence - len(requests) - 1
	temporary := make([]apiRequestLogOrderingUpdate, len(requests))
	final := make([]apiRequestLogOrderingUpdate, len(requests))
	for index := range requests {
		temporary[index] = apiRequestLogOrderingUpdate{Id: requests[index].Id, Value: temporaryStart + index}
		final[index] = apiRequestLogOrderingUpdate{Id: requests[index].Id, Value: index + 1}
	}
	if err := bulkUpdateAPIRequestLogOrdering(tx, &APIRequestLogTurnRequest{}, "sequence", temporary); err != nil {
		return err
	}
	return bulkUpdateAPIRequestLogOrdering(tx, &APIRequestLogTurnRequest{}, "sequence", final)
}

type apiRequestLogTurnItemProposal struct {
	mapping APIRequestLogTurnItem
	request APIRequestLogTurnRequest
	source  APIRequestLogItem
}

func mapAPIRequestLogTurnItems(tx *gorm.DB, turnRecordId, requestRecordId int64, candidates []apiRequestLogTurnCandidate, prefixLength int) error {
	desiredSourceIds := make(map[int]bool, len(candidates))
	for _, candidate := range candidates {
		if candidate.item.Id <= 0 {
			continue
		}
		if candidate.inputIndex >= 0 && candidate.inputIndex < prefixLength && !apiRequestLogTurnRetainEveryTurn(candidate.item) {
			continue
		}
		desiredSourceIds[candidate.item.Id] = true
	}

	var existingMappings []APIRequestLogTurnItem
	if err := tx.Where("turn_record_id = ?", turnRecordId).Find(&existingMappings).Error; err != nil {
		return err
	}
	existingBySource := make(map[int]APIRequestLogTurnItem, len(existingMappings))
	for _, mapping := range existingMappings {
		existingBySource[mapping.SourceItemId] = mapping
	}

	desiredIds := make([]int, 0, len(desiredSourceIds))
	for sourceId := range desiredSourceIds {
		desiredIds = append(desiredIds, sourceId)
	}
	if len(desiredIds) > 0 {
		var assigned []APIRequestLogTurnItem
		if err := tx.Where("source_item_id IN ?", desiredIds).Find(&assigned).Error; err != nil {
			return err
		}
		for _, mapping := range assigned {
			if mapping.TurnRecordId != turnRecordId {
				return fmt.Errorf("source item %d is already assigned to turn %d", mapping.SourceItemId, mapping.TurnRecordId)
			}
			if mapping.RequestRecordId != requestRecordId {
				return fmt.Errorf("source item %d is already assigned to request %d", mapping.SourceItemId, mapping.RequestRecordId)
			}
		}
	}

	var requests []APIRequestLogTurnRequest
	if err := tx.Where("turn_record_id = ?", turnRecordId).Find(&requests).Error; err != nil {
		return err
	}
	requestById := make(map[int64]APIRequestLogTurnRequest, len(requests))
	for _, request := range requests {
		requestById[request.Id] = request
	}
	currentRequest, ok := requestById[requestRecordId]
	if !ok {
		return fmt.Errorf("turn request %d does not exist", requestRecordId)
	}

	sourceIds := make([]int, 0, len(existingMappings)+len(desiredIds))
	seenSourceIds := make(map[int]bool, len(existingMappings)+len(desiredIds))
	for _, mapping := range existingMappings {
		if mapping.SourceItemId > 0 && !seenSourceIds[mapping.SourceItemId] {
			seenSourceIds[mapping.SourceItemId] = true
			sourceIds = append(sourceIds, mapping.SourceItemId)
		}
	}
	for _, sourceId := range desiredIds {
		if sourceId > 0 && !seenSourceIds[sourceId] {
			seenSourceIds[sourceId] = true
			sourceIds = append(sourceIds, sourceId)
		}
	}
	sourceById := make(map[int]APIRequestLogItem, len(sourceIds))
	if len(sourceIds) > 0 {
		var sources []APIRequestLogItem
		if err := tx.Where("id IN ?", sourceIds).Find(&sources).Error; err != nil {
			return err
		}
		for _, source := range sources {
			sourceById[source.Id] = source
		}
	}

	replaceCurrentMappings := len(candidates) > 0
	proposals := make([]apiRequestLogTurnItemProposal, 0, len(existingMappings)+len(desiredSourceIds))
	for _, mapping := range existingMappings {
		if replaceCurrentMappings && mapping.RequestRecordId == requestRecordId {
			continue
		}
		request, requestOk := requestById[mapping.RequestRecordId]
		source, sourceOk := sourceById[mapping.SourceItemId]
		if !requestOk || !sourceOk {
			continue
		}
		proposals = append(proposals, apiRequestLogTurnItemProposal{mapping: mapping, request: request, source: source})
	}
	for _, candidate := range candidates {
		if !desiredSourceIds[candidate.item.Id] {
			continue
		}
		source, ok := sourceById[candidate.item.Id]
		if !ok {
			continue
		}
		mapping := APIRequestLogTurnItem{
			TurnRecordId:    turnRecordId,
			RequestRecordId: requestRecordId,
			SourceItemId:    source.Id,
			CanonicalKey:    candidate.canonicalKey,
			ProviderItemId:  candidate.providerItemId,
			MessagePhase:    candidate.messagePhase,
			ItemStatus:      candidate.itemStatus,
		}
		if existing, exists := existingBySource[source.Id]; exists {
			mapping.Id = existing.Id
			mapping.Ordinal = existing.Ordinal
		}
		proposals = append(proposals, apiRequestLogTurnItemProposal{mapping: mapping, request: currentRequest, source: source})
	}

	winnerByKey := make(map[string]apiRequestLogTurnItemProposal, len(proposals))
	for _, proposal := range proposals {
		winner, exists := winnerByKey[proposal.mapping.CanonicalKey]
		if !exists || apiRequestLogTurnItemProposalLess(proposal, winner) {
			winnerByKey[proposal.mapping.CanonicalKey] = proposal
		}
	}
	winners := make([]apiRequestLogTurnItemProposal, 0, len(winnerByKey))
	for _, winner := range winnerByKey {
		winners = append(winners, winner)
	}
	sort.SliceStable(winners, func(i, j int) bool {
		return apiRequestLogTurnItemProposalLess(winners[i], winners[j])
	})

	existingById := make(map[int64]APIRequestLogTurnItem, len(existingMappings))
	for _, mapping := range existingMappings {
		existingById[mapping.Id] = mapping
	}
	keepIds := make(map[int64]bool, len(winners))
	for index := range winners {
		mapping := &winners[index].mapping
		if mapping.Id <= 0 {
			continue
		}
		existing, exists := existingById[mapping.Id]
		if !exists || existing.CanonicalKey != mapping.CanonicalKey || existing.SourceItemId != mapping.SourceItemId || existing.RequestRecordId != mapping.RequestRecordId {
			mapping.Id = 0
			mapping.Ordinal = 0
			continue
		}
		keepIds[mapping.Id] = true
	}
	removeIds := make([]int64, 0)
	for _, mapping := range existingMappings {
		if !keepIds[mapping.Id] {
			removeIds = append(removeIds, mapping.Id)
		}
	}
	if len(removeIds) > 0 {
		if err := tx.Where("id IN ?", removeIds).Delete(&APIRequestLogTurnItem{}).Error; err != nil {
			return err
		}
	}

	appendOnly := true
	sawNewMapping := false
	for index := range winners {
		mapping := &winners[index].mapping
		if mapping.Id <= 0 {
			sawNewMapping = true
			mapping.Ordinal = index + 1
			continue
		}
		if sawNewMapping || mapping.Ordinal != index+1 {
			appendOnly = false
		}
	}

	newMappings := make([]APIRequestLogTurnItem, 0)
	for _, winner := range winners {
		mapping := winner.mapping
		if mapping.Id <= 0 {
			newMappings = append(newMappings, mapping)
			continue
		}
		existing := existingById[mapping.Id]
		updates := map[string]interface{}{}
		if existing.ProviderItemId != mapping.ProviderItemId {
			updates["provider_item_id"] = mapping.ProviderItemId
		}
		if existing.MessagePhase != mapping.MessagePhase {
			updates["message_phase"] = mapping.MessagePhase
		}
		if existing.ItemStatus != mapping.ItemStatus {
			updates["item_status"] = mapping.ItemStatus
		}
		if len(updates) == 0 {
			continue
		}
		if err := tx.Model(&APIRequestLogTurnItem{}).Where("id = ?", mapping.Id).Updates(updates).Error; err != nil {
			return err
		}
	}
	if len(newMappings) > 0 {
		if !appendOnly {
			var minOrdinal int
			if err := tx.Model(&APIRequestLogTurnItem{}).Where("turn_record_id = ?", turnRecordId).Select("COALESCE(MIN(ordinal), 0)").Scan(&minOrdinal).Error; err != nil {
				return err
			}
			temporaryStart := minOrdinal - len(newMappings) - 1
			for index := range newMappings {
				newMappings[index].Ordinal = temporaryStart + index
			}
		}
		if err := tx.CreateInBatches(newMappings, 100).Error; err != nil {
			return err
		}
	}
	if appendOnly {
		return nil
	}
	return reindexAPIRequestLogTurnItems(tx, turnRecordId)
}

func apiRequestLogTurnItemProposalLess(left, right apiRequestLogTurnItemProposal) bool {
	if left.request.CreatedAt != right.request.CreatedAt {
		return left.request.CreatedAt < right.request.CreatedAt
	}
	if left.request.LogId != right.request.LogId {
		return left.request.LogId < right.request.LogId
	}
	if left.source.Seq != right.source.Seq {
		return left.source.Seq < right.source.Seq
	}
	if left.source.Id != right.source.Id {
		return left.source.Id < right.source.Id
	}
	return left.mapping.Id < right.mapping.Id
}

func reindexAPIRequestLogTurnItems(tx *gorm.DB, turnRecordId int64) error {
	var mappings []APIRequestLogTurnItem
	if err := tx.Where("turn_record_id = ?", turnRecordId).Find(&mappings).Error; err != nil {
		return err
	}
	if len(mappings) == 0 {
		return nil
	}
	var requests []APIRequestLogTurnRequest
	if err := tx.Where("turn_record_id = ?", turnRecordId).Find(&requests).Error; err != nil {
		return err
	}
	requestById := make(map[int64]APIRequestLogTurnRequest, len(requests))
	for _, request := range requests {
		requestById[request.Id] = request
	}
	sourceIds := make([]int, 0, len(mappings))
	for _, mapping := range mappings {
		sourceIds = append(sourceIds, mapping.SourceItemId)
	}
	var sources []APIRequestLogItem
	if err := tx.Where("id IN ?", sourceIds).Find(&sources).Error; err != nil {
		return err
	}
	sourceById := make(map[int]APIRequestLogItem, len(sources))
	for _, source := range sources {
		sourceById[source.Id] = source
	}
	sort.SliceStable(mappings, func(i, j int) bool {
		return apiRequestLogTurnItemProposalLess(
			apiRequestLogTurnItemProposal{mapping: mappings[i], request: requestById[mappings[i].RequestRecordId], source: sourceById[mappings[i].SourceItemId]},
			apiRequestLogTurnItemProposal{mapping: mappings[j], request: requestById[mappings[j].RequestRecordId], source: sourceById[mappings[j].SourceItemId]},
		)
	})
	needsReindex := false
	minOrdinal := 0
	for index, mapping := range mappings {
		if mapping.Ordinal != index+1 {
			needsReindex = true
		}
		if mapping.Ordinal < minOrdinal {
			minOrdinal = mapping.Ordinal
		}
	}
	if !needsReindex {
		return nil
	}
	temporaryStart := minOrdinal - len(mappings) - 1
	temporary := make([]apiRequestLogOrderingUpdate, len(mappings))
	final := make([]apiRequestLogOrderingUpdate, len(mappings))
	for index, mapping := range mappings {
		temporary[index] = apiRequestLogOrderingUpdate{Id: mapping.Id, Value: temporaryStart + index}
		final[index] = apiRequestLogOrderingUpdate{Id: mapping.Id, Value: index + 1}
	}
	if err := bulkUpdateAPIRequestLogOrdering(tx, &APIRequestLogTurnItem{}, "ordinal", temporary); err != nil {
		return err
	}
	return bulkUpdateAPIRequestLogOrdering(tx, &APIRequestLogTurnItem{}, "ordinal", final)
}

type apiRequestLogOrderingUpdate struct {
	Id    int64
	Value int
}

func bulkUpdateAPIRequestLogOrdering(tx *gorm.DB, table interface{}, column string, updates []apiRequestLogOrderingUpdate) error {
	for start := 0; start < len(updates); start += 100 {
		end := start + 100
		if end > len(updates) {
			end = len(updates)
		}
		chunk := updates[start:end]
		var expression strings.Builder
		expression.WriteString("CASE id")
		args := make([]interface{}, 0, len(chunk)*2)
		ids := make([]int64, 0, len(chunk))
		for _, update := range chunk {
			expression.WriteString(" WHEN ? THEN ?")
			args = append(args, update.Id, update.Value)
			ids = append(ids, update.Id)
		}
		expression.WriteString(" END")
		if err := tx.Model(table).Where("id IN ?", ids).Update(column, gorm.Expr(expression.String(), args...)).Error; err != nil {
			return err
		}
	}
	return nil
}

func refreshAPIRequestLogTurn(tx *gorm.DB, turn *APIRequestLogTurn, log *APIRequestLog, meta APIRequestLogTurnMeta) error {
	type requestAggregate struct {
		RequestCount     int `gorm:"column:request_count"`
		PromptTokens     int `gorm:"column:prompt_tokens"`
		CompletionTokens int `gorm:"column:completion_tokens"`
		TokenUsed        int `gorm:"column:token_used"`
		Quota            int `gorm:"column:quota"`
	}
	var aggregate requestAggregate
	if err := tx.Model(&APIRequestLogTurnRequest{}).
		Where("turn_record_id = ?", turn.Id).
		Select(
			"COUNT(*) AS request_count, COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens, " +
				"COALESCE(SUM(completion_tokens), 0) AS completion_tokens, COALESCE(SUM(token_used), 0) AS token_used, " +
				"COALESCE(SUM(quota), 0) AS quota",
		).
		Scan(&aggregate).Error; err != nil {
		return err
	}
	var itemCount int64
	if err := tx.Model(&APIRequestLogTurnItem{}).Where("turn_record_id = ?", turn.Id).Count(&itemCount).Error; err != nil {
		return err
	}
	startedAt := turn.StartedAt
	if startedAt <= 0 || (meta.StartedAt > 0 && meta.StartedAt < startedAt) {
		startedAt = meta.StartedAt
	}
	completedAt := turn.CompletedAt
	if meta.CompletionStatus == APIRequestLogTurnStatusCompleted && meta.CompletedAt > completedAt {
		completedAt = meta.CompletedAt
	}
	status := strongerAPIRequestLogTurnStatus(turn.CompletionStatus, meta.CompletionStatus)
	attribution := strongerAPIRequestLogTurnAttribution(turn.Attribution, meta.Attribution)
	completionSignal := strongerAPIRequestLogTurnCompletionSignal(turn.CompletionSignal, meta.CompletionSignal)
	updates := map[string]interface{}{
		"protocol":                firstNonEmptyTurnValue(turn.Protocol, meta.Protocol),
		"window_id":               firstNonEmptyTurnValue(turn.WindowId, meta.WindowId),
		"request_kind":            firstNonEmptyTurnValue(turn.RequestKind, meta.RequestKind),
		"started_at":              startedAt,
		"completed_at":            completedAt,
		"completion_status":       status,
		"completion_signal":       completionSignal,
		"attribution":             attribution,
		"request_count":           aggregate.RequestCount,
		"item_count":              int(itemCount),
		"prompt_tokens":           aggregate.PromptTokens,
		"completion_tokens":       aggregate.CompletionTokens,
		"token_used":              aggregate.TokenUsed,
		"quota":                   aggregate.Quota,
		"materialization_version": gorm.Expr("materialization_version + 1"),
	}
	if turn.UserId == 0 && log.UserId != 0 {
		updates["user_id"] = log.UserId
	}
	if turn.Username == "" && log.Username != "" {
		updates["username"] = log.Username
	}
	if turn.TokenId == 0 && log.TokenId != 0 {
		updates["token_id"] = log.TokenId
	}
	if turn.TokenName == "" && log.TokenName != "" {
		updates["token_name"] = log.TokenName
	}
	if turn.TokenFingerprint == "" {
		updates["token_fingerprint"] = apiRequestLogTokenFingerprint(log)
	}
	if turn.ModelName == "" && log.ModelName != "" {
		updates["model_name"] = log.ModelName
	}
	if err := tx.Model(&APIRequestLogTurn{}).Where("id = ?", turn.Id).Updates(updates).Error; err != nil {
		return err
	}
	return tx.First(turn, turn.Id).Error
}

func getAPIRequestLogTurnDetailsByIds(db *gorm.DB, ids []int64) ([]*APIRequestLogTurnDetail, error) {
	ids = uniquePositiveInt64s(ids)
	if len(ids) == 0 {
		return []*APIRequestLogTurnDetail{}, nil
	}
	var turns []APIRequestLogTurn
	if err := db.Where("id IN ?", ids).Find(&turns).Error; err != nil {
		return nil, err
	}
	turnById := make(map[int64]*APIRequestLogTurnDetail, len(turns))
	for index := range turns {
		detail := &APIRequestLogTurnDetail{APIRequestLogTurn: turns[index], Requests: []APIRequestLogTurnRequest{}, Items: []APIRequestLogTurnItemDetail{}}
		turnById[turns[index].Id] = detail
	}
	var requests []APIRequestLogTurnRequest
	if err := db.Where("turn_record_id IN ?", ids).Order("turn_record_id ASC").Order("sequence ASC").Find(&requests).Error; err != nil {
		return nil, err
	}
	for _, request := range requests {
		if detail := turnById[request.TurnRecordId]; detail != nil {
			detail.Requests = append(detail.Requests, request)
		}
	}
	var mappings []APIRequestLogTurnItem
	if err := db.Where("turn_record_id IN ?", ids).Order("turn_record_id ASC").Order("ordinal ASC").Find(&mappings).Error; err != nil {
		return nil, err
	}
	sourceIds := make([]int, 0, len(mappings))
	for _, mapping := range mappings {
		sourceIds = append(sourceIds, mapping.SourceItemId)
	}
	sourceById := map[int]APIRequestLogItem{}
	if len(sourceIds) > 0 {
		var sourceItems []APIRequestLogItem
		if err := db.Where("id IN ?", sourceIds).Find(&sourceItems).Error; err != nil {
			return nil, err
		}
		for _, sourceItem := range sourceItems {
			sourceById[sourceItem.Id] = sourceItem
		}
	}
	for _, mapping := range mappings {
		detail := turnById[mapping.TurnRecordId]
		sourceItem, ok := sourceById[mapping.SourceItemId]
		if detail == nil || !ok || !apiRequestLogTurnItemAllowed(sourceItem) {
			continue
		}
		detail.Items = append(detail.Items, APIRequestLogTurnItemDetail{
			Id:              mapping.Id,
			TurnRecordId:    mapping.TurnRecordId,
			RequestRecordId: mapping.RequestRecordId,
			SourceItemId:    mapping.SourceItemId,
			Ordinal:         mapping.Ordinal,
			CanonicalKey:    mapping.CanonicalKey,
			ProviderItemId:  mapping.ProviderItemId,
			MessagePhase:    mapping.MessagePhase,
			ItemStatus:      mapping.ItemStatus,
			LogId:           sourceItem.LogId,
			SourceSeq:       sourceItem.Seq,
			Phase:           sourceItem.Phase,
			ItemType:        sourceItem.ItemType,
			Role:            sourceItem.Role,
			ContentType:     sourceItem.ContentType,
			Content:         sourceItem.Content,
			ToolCallId:      sourceItem.ToolCallId,
			Name:            sourceItem.Name,
			Source:          sourceItem.Source,
			Redacted:        sourceItem.Redacted,
			Truncated:       sourceItem.Truncated,
		})
	}
	if db.Migrator().HasTable(&APIRequestLogExportMember{}) {
		var members []APIRequestLogExportMember
		if err := db.Where("turn_record_id IN ?", ids).Find(&members).Error; err != nil {
			return nil, err
		}
		batchTags := map[int64]string{}
		batchIds := make([]int64, 0, len(members))
		for _, member := range members {
			batchIds = append(batchIds, member.BatchId)
		}
		if len(batchIds) > 0 {
			var batches []APIRequestLogExportBatch
			if err := db.Where("id IN ?", uniquePositiveInt64s(batchIds)).Find(&batches).Error; err != nil {
				return nil, err
			}
			for _, batch := range batches {
				batchTags[batch.Id] = batch.Tag
			}
		}
		for _, member := range members {
			if detail := turnById[member.TurnRecordId]; detail != nil {
				detail.Exported = true
				detail.ExportTag = batchTags[member.BatchId]
			}
		}
	}
	ordered := make([]*APIRequestLogTurnDetail, 0, len(ids))
	for _, id := range ids {
		if detail := turnById[id]; detail != nil {
			ordered = append(ordered, detail)
		}
	}
	return ordered, nil
}

func apiRequestLogTurnItemAllowed(item APIRequestLogItem) bool {
	if strings.Contains(strings.ToLower(strings.TrimSpace(item.ContentType)), "encrypted") {
		return false
	}
	if strings.TrimSpace(string(item.Content)) == "" {
		return true
	}
	var value interface{}
	if err := common.Unmarshal([]byte(item.Content), &value); err != nil {
		return true
	}
	return !apiRequestLogTurnContainsEncryptedField(value)
}

func apiRequestLogTurnContainsEncryptedField(value interface{}) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if normalized == "encrypted_content" || normalized == "encrypted_reasoning" || normalized == "reasoning_encrypted" {
				return true
			}
			if apiRequestLogTurnContainsEncryptedField(child) {
				return true
			}
		}
	case []interface{}:
		for _, child := range typed {
			if apiRequestLogTurnContainsEncryptedField(child) {
				return true
			}
		}
	}
	return false
}

func apiRequestLogTurnItemMeta(item APIRequestLogItem) (string, string) {
	var value map[string]interface{}
	if err := common.Unmarshal([]byte(item.Content), &value); err != nil {
		return "", ""
	}
	phase, _ := value["phase"].(string)
	status, _ := value["status"].(string)
	return strings.TrimSpace(phase), strings.TrimSpace(status)
}

func apiRequestLogTurnItemCanonicalKey(item APIRequestLogItem, providerItemId string) string {
	key, _ := apiRequestLogTurnItemCanonicalIdentity(item, providerItemId)
	return key
}

func apiRequestLogTurnItemCanonicalIdentity(item APIRequestLogItem, providerItemId string) (string, bool) {
	if providerItemId = strings.TrimSpace(providerItemId); providerItemId != "" {
		return apiRequestLogSHA256("provider\x00" + providerItemId), true
	}
	if toolCallId := strings.TrimSpace(item.ToolCallId); toolCallId != "" {
		return apiRequestLogSHA256("call\x00" + strings.ToLower(strings.TrimSpace(item.ItemType)) + "\x00" + toolCallId), true
	}
	return apiRequestLogTurnItemSemanticKey(item), false
}

func apiRequestLogTurnItemSemanticKey(item APIRequestLogItem) string {
	value := strings.Join([]string{
		strings.ToLower(strings.TrimSpace(item.ItemType)),
		strings.ToLower(strings.TrimSpace(item.Role)),
		strings.ToLower(strings.TrimSpace(item.ContentType)),
		strings.TrimSpace(item.ToolCallId),
		strings.TrimSpace(item.Name),
		string(item.Content),
	}, "\x00")
	return apiRequestLogSHA256("semantic\x00" + value)
}

func apiRequestLogTurnRetainEveryTurn(item APIRequestLogItem) bool {
	if item.ItemType == APIRequestLogItemToolSpec {
		return true
	}
	if item.ItemType != APIRequestLogItemMessage {
		return false
	}
	role := strings.ToLower(strings.TrimSpace(item.Role))
	return role == "system" || role == "developer"
}

func apiRequestLogTokenFingerprint(log *APIRequestLog) string {
	if log == nil {
		return ""
	}
	if log.TokenId > 0 {
		return apiRequestLogSHA256("token-id\x00" + strconv.Itoa(log.TokenId))
	}
	if tokenName := strings.TrimSpace(log.TokenName); tokenName != "" {
		return apiRequestLogSHA256("token-name\x00" + tokenName)
	}
	return ""
}

func apiRequestLogHasStableOwner(log *APIRequestLog) bool {
	if log == nil {
		return false
	}
	return log.UserId > 0 || strings.TrimSpace(log.Username) != "" || log.TokenId > 0 || strings.TrimSpace(log.TokenName) != ""
}

func apiRequestLogOwnerFingerprint(log *APIRequestLog) string {
	if !apiRequestLogHasStableOwner(log) {
		logId := 0
		if log != nil {
			logId = log.Id
		}
		return apiRequestLogSHA256("request-owner\x00" + strconv.Itoa(logId))
	}
	userKey := ""
	if log.UserId > 0 {
		userKey = "id:" + strconv.Itoa(log.UserId)
	} else if username := strings.TrimSpace(log.Username); username != "" {
		userKey = "name:" + username
	}
	tokenKey := ""
	if log.TokenId > 0 {
		tokenKey = "id:" + strconv.Itoa(log.TokenId)
	} else if tokenName := strings.TrimSpace(log.TokenName); tokenName != "" {
		tokenKey = "name:" + tokenName
	}
	return apiRequestLogSHA256("stable-owner\x00" + userKey + "\x00" + tokenKey)
}

func apiRequestLogSHA256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func marshalAPIRequestLogTurnFingerprints(values []string) (APIRequestLogBody, error) {
	data, err := common.Marshal(values)
	return APIRequestLogBody(data), err
}

func unmarshalAPIRequestLogTurnFingerprints(value APIRequestLogBody) ([]string, error) {
	if strings.TrimSpace(string(value)) == "" {
		return nil, nil
	}
	var values []string
	if err := common.Unmarshal([]byte(value), &values); err != nil {
		return nil, err
	}
	return values, nil
}

func strongerAPIRequestLogTurnStatus(current, incoming string) string {
	rank := map[string]int{APIRequestLogTurnStatusUnknown: 0, APIRequestLogTurnStatusOpen: 1, APIRequestLogTurnStatusCompleted: 2}
	current = normalizeAPIRequestLogTurnStatus(current)
	incoming = normalizeAPIRequestLogTurnStatus(incoming)
	if rank[incoming] > rank[current] {
		return incoming
	}
	return current
}

func strongerAPIRequestLogTurnAttribution(current, incoming string) string {
	rank := map[string]int{APIRequestLogTurnAttributionUnknown: 0, APIRequestLogTurnAttributionInferred: 1, APIRequestLogTurnAttributionExact: 2}
	current = normalizeAPIRequestLogTurnAttribution(current)
	incoming = normalizeAPIRequestLogTurnAttribution(incoming)
	if rank[incoming] > rank[current] {
		return incoming
	}
	return current
}

func strongerAPIRequestLogTurnCompletionSignal(current, incoming string) string {
	current = strings.TrimSpace(current)
	incoming = strings.TrimSpace(incoming)
	if incoming == "" {
		return current
	}
	if current == "" || apiRequestLogTurnCompletionSignalRank(incoming) > apiRequestLogTurnCompletionSignalRank(current) {
		return incoming
	}
	return current
}

func apiRequestLogTurnCompletionSignalRank(value string) int {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(value, "final"):
		return 3
	case strings.Contains(value, "stop"), strings.Contains(value, "end"), strings.Contains(value, "complete"):
		return 2
	case value != "":
		return 1
	default:
		return 0
	}
}

func firstNonEmptyTurnValue(current, incoming string) string {
	if strings.TrimSpace(current) != "" && current != "unknown" {
		return current
	}
	if strings.TrimSpace(incoming) != "" {
		return incoming
	}
	return current
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func uniquePositiveInt64s(values []int64) []int64 {
	seen := make(map[int64]bool, len(values))
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func apiRequestLogTurnIds(turns []APIRequestLogTurn) []int64 {
	ids := make([]int64, 0, len(turns))
	for _, turn := range turns {
		ids = append(ids, turn.Id)
	}
	return ids
}

func apiRequestLogExportedTurnIds(db *gorm.DB, ids []int64) (map[int64]bool, error) {
	exported := make(map[int64]bool, len(ids))
	if len(ids) == 0 || !db.Migrator().HasTable(&APIRequestLogExportMember{}) {
		return exported, nil
	}
	var memberIds []int64
	if err := db.Model(&APIRequestLogExportMember{}).Where("turn_record_id IN ?", ids).Pluck("turn_record_id", &memberIds).Error; err != nil {
		return nil, err
	}
	for _, id := range memberIds {
		exported[id] = true
	}
	return exported, nil
}
