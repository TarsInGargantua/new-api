package model

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	APIRequestLogExportBatchStatusPending   = "pending"
	APIRequestLogExportBatchStatusBuilding  = "building"
	APIRequestLogExportBatchStatusCompleted = "completed"
	APIRequestLogExportBatchStatusFailed    = "failed"

	APIRequestLogExportIntegrityPending  = "pending"
	APIRequestLogExportIntegrityVerified = "verified"
	APIRequestLogExportIntegrityBroken   = "broken"

	APIRequestLogExportSchemaVersion = 1
)

const (
	apiRequestLogExportBatchesTable = "api_request_log_export_batches"
	apiRequestLogExportMembersTable = "api_request_log_export_members"
	apiRequestLogExportClaimSize    = 100
)

var (
	ErrAPIRequestLogExportBatchNotClaimable = errors.New("export batch is not claimable")
	ErrAPIRequestLogExportBatchLeaseLost    = errors.New("export batch lease is no longer owned")
	ErrAPIRequestLogExportDataBroken        = errors.New("export data integrity check failed")
	ErrAPIRequestLogExportBatchNotCleaned   = errors.New("export batch must be marked cleaned before deletion")
)

type APIRequestLogExportFilter struct {
	StartTimestamp     int64    `json:"start_timestamp,omitempty"`
	EndTimestamp       int64    `json:"end_timestamp,omitempty"`
	SessionId          string   `json:"session_id,omitempty"`
	TurnId             string   `json:"turn_id,omitempty"`
	Protocols          []string `json:"protocols,omitempty"`
	ModelNames         []string `json:"model_names,omitempty"`
	Usernames          []string `json:"usernames,omitempty"`
	TokenName          string   `json:"token_name,omitempty"`
	CompletionStatuses []string `json:"completion_statuses,omitempty"`
	Attributions       []string `json:"attributions,omitempty"`
	Exported           *bool    `json:"exported,omitempty"`
	IncludeInferred    bool     `json:"include_inferred"`
}

type APIRequestLogExportBatch struct {
	Id                 int64                     `json:"id" gorm:"primaryKey"`
	Tag                string                    `json:"tag" gorm:"type:varchar(128);not null;uniqueIndex"`
	Status             string                    `json:"status" gorm:"type:varchar(16);index;default:'pending'"`
	CutoffTurnId       int64                     `json:"cutoff_turn_id" gorm:"default:0"`
	ArtifactPath       string                    `json:"artifact_path,omitempty" gorm:"type:text"`
	SHA256             string                    `json:"sha256,omitempty" gorm:"type:char(64);default:''"`
	SchemaVersion      int                       `json:"schema_version" gorm:"default:1"`
	Error              string                    `json:"error,omitempty" gorm:"type:text"`
	RowCount           int64                     `json:"row_count" gorm:"default:0"`
	FilterJSON         APIRequestLogBody         `json:"-"`
	Filter             APIRequestLogExportFilter `json:"filter" gorm:"-"`
	IncludeInferred    bool                      `json:"include_inferred"`
	CreatedAt          int64                     `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt          int64                     `json:"updated_at" gorm:"autoUpdateTime"`
	CompletedAt        int64                     `json:"completed_at,omitempty" gorm:"bigint;index"`
	IntegrityStatus    string                    `json:"integrity_status" gorm:"type:varchar(16);index;default:'pending'"`
	IntegrityCheckedAt int64                     `json:"integrity_checked_at,omitempty" gorm:"bigint;index"`
	IntegrityError     string                    `json:"integrity_error,omitempty" gorm:"type:text"`
	CleanedAt          int64                     `json:"cleaned_at,omitempty" gorm:"bigint;index"`
	BuildOwner         string                    `json:"-" gorm:"type:varchar(191);index;default:''"`
	LeaseExpiresAt     int64                     `json:"lease_expires_at,omitempty" gorm:"bigint;index;default:0"`
	BuildAttempt       int                       `json:"build_attempt" gorm:"default:0"`
}

func (APIRequestLogExportBatch) TableName() string { return apiRequestLogExportBatchesTable }

type APIRequestLogExportMember struct {
	Id           int64 `json:"id" gorm:"primaryKey"`
	BatchId      int64 `json:"batch_id" gorm:"not null;uniqueIndex:idx_api_request_log_export_member_sequence,priority:1;index"`
	TurnRecordId int64 `json:"turn_record_id" gorm:"not null;uniqueIndex"`
	Sequence     int64 `json:"sequence" gorm:"not null;uniqueIndex:idx_api_request_log_export_member_sequence,priority:2"`
	CreatedAt    int64 `json:"created_at" gorm:"autoCreateTime"`
}

func (APIRequestLogExportMember) TableName() string { return apiRequestLogExportMembersTable }

type APIRequestLogExportPreview struct {
	MatchedCount         int64 `json:"matched_count"`
	SafeMatchedCount     int64 `json:"safe_matched_count"`
	BrokenCount          int64 `json:"broken_count"`
	BrokenTimeCount      int64 `json:"broken_time_count"`
	BrokenRequestCount   int64 `json:"broken_request_count"`
	BrokenItemCount      int64 `json:"broken_item_count"`
	AvailableCount       int64 `json:"available_count"`
	AlreadyExportedCount int64 `json:"already_exported_count"`
	ExactCount           int64 `json:"exact_count"`
	InferredCount        int64 `json:"inferred_count"`
}

type APIRequestLogExportBatchQueryParams struct {
	Statuses []string
	StartIdx int
	Num      int
}

type APIRequestLogExportBatchDetail struct {
	APIRequestLogExportBatch
	Members []APIRequestLogExportMember `json:"members"`
}

type APIRequestLogExportBatchTurn struct {
	Sequence int64                    `json:"sequence"`
	Turn     *APIRequestLogTurnDetail `json:"turn,omitempty"`
}

type APIRequestLogExportBatchTurnPage struct {
	Items        []APIRequestLogExportBatchTurn `json:"items"`
	NextSequence int64                          `json:"next_sequence"`
	HasMore      bool                           `json:"has_more"`
}

func EnsureAPIRequestLogExportTables(db *gorm.DB) error {
	if db == nil {
		return errors.New("request log database is not initialized")
	}
	return db.AutoMigrate(&APIRequestLogExportBatch{}, &APIRequestLogExportMember{})
}

func EnsureAPIRequestLogMaterializedTables(db *gorm.DB) error {
	if err := EnsureAPIRequestLogTurnTables(db); err != nil {
		return err
	}
	if err := EnsureAPIRequestLogExportTables(db); err != nil {
		return err
	}
	return EnsureAPIRequestLogMaterializationJobTable(db)
}

func PreviewAPIRequestLogExport(db *gorm.DB, params APIRequestLogTurnQueryParams, includeInferred bool) (*APIRequestLogExportPreview, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	candidate := buildAPIRequestLogExportCandidateQuery(db, params, includeInferred)
	preview := &APIRequestLogExportPreview{}
	if err := candidate.Count(&preview.MatchedCount).Error; err != nil {
		return nil, err
	}
	brokenSQL := apiRequestLogTurnBrokenSQL()
	broken := candidate.Session(&gorm.Session{}).Where(brokenSQL)
	if err := broken.Count(&preview.BrokenCount).Error; err != nil {
		return nil, err
	}
	if err := broken.Session(&gorm.Session{}).Where(apiRequestLogTurnBrokenTimeSQL()).Count(&preview.BrokenTimeCount).Error; err != nil {
		return nil, err
	}
	if err := broken.Session(&gorm.Session{}).Where(apiRequestLogTurnBrokenRequestSQL()).Count(&preview.BrokenRequestCount).Error; err != nil {
		return nil, err
	}
	if err := broken.Session(&gorm.Session{}).Where(apiRequestLogTurnBrokenItemSQL()).Count(&preview.BrokenItemCount).Error; err != nil {
		return nil, err
	}
	eligible := candidate.Session(&gorm.Session{}).Where("NOT " + brokenSQL)
	if err := eligible.Count(&preview.SafeMatchedCount).Error; err != nil {
		return nil, err
	}
	exportedSQL := apiRequestLogTurnExportedSQL()
	if err := eligible.Session(&gorm.Session{}).Where("NOT " + exportedSQL).Count(&preview.AvailableCount).Error; err != nil {
		return nil, err
	}
	if err := eligible.Session(&gorm.Session{}).Where(exportedSQL).Count(&preview.AlreadyExportedCount).Error; err != nil {
		return nil, err
	}
	available := eligible.Session(&gorm.Session{}).Where("NOT " + exportedSQL)
	if err := available.Session(&gorm.Session{}).Where("attribution = ?", APIRequestLogTurnAttributionExact).Count(&preview.ExactCount).Error; err != nil {
		return nil, err
	}
	if includeInferred {
		if err := available.Session(&gorm.Session{}).Where("attribution = ?", APIRequestLogTurnAttributionInferred).Count(&preview.InferredCount).Error; err != nil {
			return nil, err
		}
	}
	return preview, nil
}

// CreateAPIRequestLogExportBatch atomically creates a persistent batch and
// claims every currently-unexported matching turn. Pagination fields are ignored.
func CreateAPIRequestLogExportBatch(db *gorm.DB, params APIRequestLogTurnQueryParams, includeInferred bool) (*APIRequestLogExportBatch, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	filter := apiRequestLogExportFilterFromQuery(params, includeInferred)
	tag, err := newAPIRequestLogExportTag(time.Now().UTC(), filter)
	if err != nil {
		return nil, err
	}
	filterJSON, err := common.Marshal(filter)
	if err != nil {
		return nil, err
	}
	batch := &APIRequestLogExportBatch{
		Tag:             tag,
		Status:          APIRequestLogExportBatchStatusPending,
		SchemaVersion:   APIRequestLogExportSchemaVersion,
		FilterJSON:      APIRequestLogBody(filterJSON),
		Filter:          filter,
		IncludeInferred: includeInferred,
		IntegrityStatus: APIRequestLogExportIntegrityPending,
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(batch).Error; err != nil {
			return err
		}
		if err := buildAPIRequestLogExportEligibleQuery(tx, params, includeInferred).
			Select("COALESCE(MAX(" + apiRequestLogTurnsTable + ".id), 0)").
			Scan(&batch.CutoffTurnId).Error; err != nil {
			return err
		}
		if err := tx.Model(&APIRequestLogExportBatch{}).
			Where("id = ?", batch.Id).
			Update("cutoff_turn_id", batch.CutoffTurnId).Error; err != nil {
			return err
		}
		var lastId int64
		var sequence int64
		for batch.CutoffTurnId > 0 {
			var turnIds []int64
			query := buildAPIRequestLogExportEligibleQuery(tx, params, includeInferred).
				Where(apiRequestLogTurnAvailableForExportSQL()).
				Where(apiRequestLogTurnsTable+".id > ?", lastId).
				Where(apiRequestLogTurnsTable+".id <= ?", batch.CutoffTurnId).
				Order(apiRequestLogTurnsTable + ".id ASC").
				Limit(apiRequestLogExportClaimSize)
			if err := query.Pluck(apiRequestLogTurnsTable+".id", &turnIds).Error; err != nil {
				return err
			}
			if len(turnIds) == 0 {
				break
			}
			lastId = turnIds[len(turnIds)-1]
			lockedTurnIds, err := lockAPIRequestLogExportTurnIds(tx, params, includeInferred, batch.CutoffTurnId, turnIds)
			if err != nil {
				return err
			}
			members := make([]APIRequestLogExportMember, 0, len(lockedTurnIds))
			for _, turnId := range lockedTurnIds {
				sequence++
				members = append(members, APIRequestLogExportMember{BatchId: batch.Id, TurnRecordId: turnId, Sequence: sequence})
			}
			if len(members) > 0 {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(members, apiRequestLogExportClaimSize).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Model(&APIRequestLogExportMember{}).Where("batch_id = ?", batch.Id).Count(&batch.RowCount).Error; err != nil {
			return err
		}
		return tx.Model(&APIRequestLogExportBatch{}).Where("id = ?", batch.Id).Update("row_count", batch.RowCount).Error
	})
	if err != nil {
		return nil, err
	}
	return GetAPIRequestLogExportBatchByTag(db, batch.Tag)
}

func GetAPIRequestLogExportBatches(db *gorm.DB, params APIRequestLogExportBatchQueryParams) (items []*APIRequestLogExportBatch, total int64, err error) {
	if db == nil {
		return nil, 0, errors.New("request log database is not initialized")
	}
	tx := db.Model(&APIRequestLogExportBatch{})
	if statuses := normalizeAPIRequestLogExportStatuses(params.Statuses); len(statuses) > 0 {
		tx = tx.Where("status IN ?", statuses)
	}
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
	if err = tx.Order("id DESC").Limit(limit).Offset(offset).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	if err := hydrateAPIRequestLogExportBatchFilters(items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func GetAPIRequestLogExportBatchesForRecovery(db *gorm.DB, limit int) ([]*APIRequestLogExportBatch, error) {
	items, _, err := GetAPIRequestLogExportBatches(db, APIRequestLogExportBatchQueryParams{
		Statuses: []string{APIRequestLogExportBatchStatusPending, APIRequestLogExportBatchStatusBuilding},
		Num:      limit,
	})
	return items, err
}

func GetAPIRequestLogExportBatchByTag(db *gorm.DB, tag string) (*APIRequestLogExportBatch, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	var batch APIRequestLogExportBatch
	if err := db.Where("tag = ?", strings.TrimSpace(tag)).First(&batch).Error; err != nil {
		return nil, err
	}
	if err := hydrateAPIRequestLogExportBatchFilter(&batch); err != nil {
		return nil, err
	}
	return &batch, nil
}

func GetAPIRequestLogExportBatchById(db *gorm.DB, id int64) (*APIRequestLogExportBatch, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	var batch APIRequestLogExportBatch
	if err := db.First(&batch, id).Error; err != nil {
		return nil, err
	}
	if err := hydrateAPIRequestLogExportBatchFilter(&batch); err != nil {
		return nil, err
	}
	return &batch, nil
}

func GetAPIRequestLogExportBatchDetail(db *gorm.DB, tag string, afterSequence int64, limit int) (*APIRequestLogExportBatchDetail, error) {
	batch, err := GetAPIRequestLogExportBatchByTag(db, tag)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	var members []APIRequestLogExportMember
	if err := db.Where("batch_id = ? AND sequence > ?", batch.Id, afterSequence).Order("sequence ASC").Limit(limit).Find(&members).Error; err != nil {
		return nil, err
	}
	return &APIRequestLogExportBatchDetail{APIRequestLogExportBatch: *batch, Members: members}, nil
}

// GetAPIRequestLogExportBatchTurnPage uses fixed-count batch reads: members,
// turns, requests, mappings, and source items are each loaded in bulk.
func GetAPIRequestLogExportBatchTurnPage(db *gorm.DB, batchId int64, afterSequence int64, limit int) (*APIRequestLogExportBatchTurnPage, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	if batchId <= 0 {
		return nil, errors.New("invalid export batch id")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	var members []APIRequestLogExportMember
	if err := db.Where("batch_id = ? AND sequence > ?", batchId, afterSequence).Order("sequence ASC").Limit(limit + 1).Find(&members).Error; err != nil {
		return nil, err
	}
	page := &APIRequestLogExportBatchTurnPage{Items: []APIRequestLogExportBatchTurn{}}
	if len(members) == 0 {
		return page, nil
	}
	if len(members) > limit {
		page.HasMore = true
		members = members[:limit]
	}
	turnIds := make([]int64, 0, len(members))
	for _, member := range members {
		turnIds = append(turnIds, member.TurnRecordId)
	}
	details, err := getAPIRequestLogTurnDetailsByIds(db, turnIds)
	if err != nil {
		return nil, err
	}
	detailById := make(map[int64]*APIRequestLogTurnDetail, len(details))
	for _, detail := range details {
		detailById[detail.Id] = detail
	}
	for _, member := range members {
		detail := detailById[member.TurnRecordId]
		if detail == nil {
			return nil, fmt.Errorf("%w: export member %d references missing turn %d", ErrAPIRequestLogExportDataBroken, member.Id, member.TurnRecordId)
		}
		page.Items = append(page.Items, APIRequestLogExportBatchTurn{Sequence: member.Sequence, Turn: detail})
		page.NextSequence = member.Sequence
	}
	return page, nil
}

// ClaimAPIRequestLogExportBatch leases one pending batch, or takes over a
// building batch whose previous lease has expired. The compare-and-swap keeps
// multiple viewer instances from building the same artifact concurrently.
func ClaimAPIRequestLogExportBatch(db *gorm.DB, tag, owner string, leaseDuration time.Duration) (*APIRequestLogExportBatch, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	tag = strings.TrimSpace(tag)
	owner = strings.TrimSpace(owner)
	if tag == "" {
		return nil, errors.New("export batch tag is required")
	}
	if owner == "" {
		return nil, errors.New("export build owner is required")
	}
	now, leaseExpiresAt, err := apiRequestLogExportLeaseWindow(leaseDuration)
	if err != nil {
		return nil, err
	}
	result := db.Model(&APIRequestLogExportBatch{}).
		Where("tag = ? AND (status = ? OR (status = ? AND lease_expires_at <= ?))", tag, APIRequestLogExportBatchStatusPending, APIRequestLogExportBatchStatusBuilding, now).
		Updates(map[string]interface{}{
			"status":               APIRequestLogExportBatchStatusBuilding,
			"build_owner":          owner,
			"lease_expires_at":     leaseExpiresAt,
			"build_attempt":        gorm.Expr("build_attempt + ?", 1),
			"artifact_path":        "",
			"sha256":               "",
			"error":                "",
			"completed_at":         0,
			"integrity_status":     APIRequestLogExportIntegrityPending,
			"integrity_checked_at": 0,
			"integrity_error":      "",
			"cleaned_at":           0,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, apiRequestLogExportTransitionError(db, tag, ErrAPIRequestLogExportBatchNotClaimable)
	}
	return GetAPIRequestLogExportBatchByTag(db, tag)
}

// ClaimNextAPIRequestLogExportBatch claims the oldest pending or expired batch.
func ClaimNextAPIRequestLogExportBatch(db *gorm.DB, owner string, leaseDuration time.Duration) (*APIRequestLogExportBatch, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("export build owner is required")
	}
	for {
		now := time.Now().UTC().Unix()
		var candidate APIRequestLogExportBatch
		result := db.Select("tag").
			Where("status = ? OR (status = ? AND lease_expires_at <= ?)", APIRequestLogExportBatchStatusPending, APIRequestLogExportBatchStatusBuilding, now).
			Order("id ASC").
			Limit(1).
			Find(&candidate)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			return nil, ErrAPIRequestLogExportBatchNotClaimable
		}
		batch, err := ClaimAPIRequestLogExportBatch(db, candidate.Tag, owner, leaseDuration)
		if errors.Is(err, ErrAPIRequestLogExportBatchNotClaimable) {
			continue
		}
		return batch, err
	}
}

func RenewAPIRequestLogExportBatchLease(db *gorm.DB, tag, owner string, leaseDuration time.Duration) (*APIRequestLogExportBatch, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	tag = strings.TrimSpace(tag)
	owner = strings.TrimSpace(owner)
	if tag == "" || owner == "" {
		return nil, errors.New("export batch tag and build owner are required")
	}
	now, leaseExpiresAt, err := apiRequestLogExportLeaseWindow(leaseDuration)
	if err != nil {
		return nil, err
	}
	result := db.Model(&APIRequestLogExportBatch{}).
		Where("tag = ? AND status = ? AND build_owner = ? AND lease_expires_at > ?", tag, APIRequestLogExportBatchStatusBuilding, owner, now).
		Update("lease_expires_at", leaseExpiresAt)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		batch, lookupErr := GetAPIRequestLogExportBatchByTag(db, tag)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if batch.Status == APIRequestLogExportBatchStatusBuilding && batch.BuildOwner == owner && batch.LeaseExpiresAt > now {
			return batch, nil
		}
		return nil, apiRequestLogExportTransitionError(db, tag, ErrAPIRequestLogExportBatchLeaseLost)
	}
	return GetAPIRequestLogExportBatchByTag(db, tag)
}

func MarkAPIRequestLogExportBatchCompleted(db *gorm.DB, tag, owner, artifactPath, sha256 string, rowCount int64) (*APIRequestLogExportBatch, error) {
	batch, err := GetAPIRequestLogExportBatchByTag(db, tag)
	if err != nil {
		return nil, err
	}
	if batch.RowCount != rowCount {
		return nil, fmt.Errorf("artifact row count %d does not match claimed member count %d", rowCount, batch.RowCount)
	}
	if strings.TrimSpace(artifactPath) == "" {
		return nil, errors.New("artifact path is required")
	}
	if len(strings.TrimSpace(sha256)) != 64 {
		return nil, errors.New("artifact sha256 is required")
	}
	now := time.Now().UTC().Unix()
	result := db.Model(&APIRequestLogExportBatch{}).
		Where("tag = ? AND status = ? AND build_owner = ? AND lease_expires_at > ?", strings.TrimSpace(tag), APIRequestLogExportBatchStatusBuilding, strings.TrimSpace(owner), now).
		Updates(map[string]interface{}{
			"status":               APIRequestLogExportBatchStatusCompleted,
			"artifact_path":        artifactPath,
			"sha256":               strings.ToLower(strings.TrimSpace(sha256)),
			"error":                "",
			"completed_at":         now,
			"lease_expires_at":     0,
			"integrity_status":     APIRequestLogExportIntegrityVerified,
			"integrity_checked_at": now,
			"integrity_error":      "",
			"cleaned_at":           0,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, apiRequestLogExportTransitionError(db, tag, ErrAPIRequestLogExportBatchLeaseLost)
	}
	return GetAPIRequestLogExportBatchByTag(db, tag)
}

func MarkAPIRequestLogExportBatchFailed(db *gorm.DB, tag, owner string, buildError error) (*APIRequestLogExportBatch, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	message := "export artifact build failed"
	if buildError != nil && strings.TrimSpace(buildError.Error()) != "" {
		message = buildError.Error()
	}
	now := time.Now().UTC().Unix()
	updates := map[string]interface{}{
		"status":           APIRequestLogExportBatchStatusFailed,
		"error":            message,
		"lease_expires_at": 0,
	}
	if errors.Is(buildError, ErrAPIRequestLogExportDataBroken) {
		updates["integrity_status"] = APIRequestLogExportIntegrityBroken
		updates["integrity_checked_at"] = now
		updates["integrity_error"] = message
	}
	result := db.Model(&APIRequestLogExportBatch{}).
		Where("tag = ? AND status = ? AND build_owner = ? AND lease_expires_at > ?", strings.TrimSpace(tag), APIRequestLogExportBatchStatusBuilding, strings.TrimSpace(owner), now).
		Updates(updates)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, apiRequestLogExportTransitionError(db, tag, ErrAPIRequestLogExportBatchLeaseLost)
	}
	return GetAPIRequestLogExportBatchByTag(db, tag)
}

func RetryAPIRequestLogExportBatchPending(db *gorm.DB, tag string) (*APIRequestLogExportBatch, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return nil, errors.New("export batch tag is required")
	}
	result := db.Model(&APIRequestLogExportBatch{}).
		Where("tag = ? AND status = ?", tag, APIRequestLogExportBatchStatusFailed).
		Updates(map[string]interface{}{
			"status":               APIRequestLogExportBatchStatusPending,
			"artifact_path":        "",
			"sha256":               "",
			"error":                "",
			"completed_at":         0,
			"integrity_status":     APIRequestLogExportIntegrityPending,
			"integrity_checked_at": 0,
			"integrity_error":      "",
			"cleaned_at":           0,
			"build_owner":          "",
			"lease_expires_at":     0,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, apiRequestLogExportTransitionError(db, tag, ErrAPIRequestLogExportBatchNotClaimable)
	}
	return GetAPIRequestLogExportBatchByTag(db, tag)
}

// AuditAPIRequestLogExportBatch re-reads a completed batch's immutable member
// list and records whether every turn still has a complete exportable graph.
// It is intentionally explicit for historical batches: no slow audit runs on
// normal list requests.
func AuditAPIRequestLogExportBatch(db *gorm.DB, tag string) (*APIRequestLogExportBatch, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	batch, err := GetAPIRequestLogExportBatchByTag(db, tag)
	if err != nil {
		return nil, err
	}
	if batch.Status != APIRequestLogExportBatchStatusCompleted {
		return nil, errors.New("only completed export batches can be audited")
	}
	var sequence int64
	var integrityErr error
	for {
		page, pageErr := GetAPIRequestLogExportBatchTurnPage(db, batch.Id, sequence, apiRequestLogExportClaimSize)
		if pageErr != nil {
			if errors.Is(pageErr, ErrAPIRequestLogExportDataBroken) {
				integrityErr = pageErr
				break
			}
			return nil, pageErr
		}
		for _, member := range page.Items {
			if err := ValidateAPIRequestLogTurnForExport(member.Turn); err != nil {
				integrityErr = fmt.Errorf("batch member %d: %w", member.Sequence, err)
				break
			}
		}
		if integrityErr != nil || !page.HasMore {
			break
		}
		sequence = page.NextSequence
	}
	now := time.Now().UTC().Unix()
	updates := map[string]interface{}{
		"integrity_status":     APIRequestLogExportIntegrityVerified,
		"integrity_checked_at": now,
		"integrity_error":      "",
	}
	if integrityErr != nil {
		updates["integrity_status"] = APIRequestLogExportIntegrityBroken
		updates["integrity_error"] = truncateAPIRequestLogExportError(integrityErr.Error())
	}
	if err := db.Model(&APIRequestLogExportBatch{}).Where("id = ? AND status = ?", batch.Id, APIRequestLogExportBatchStatusCompleted).Updates(updates).Error; err != nil {
		return nil, err
	}
	return GetAPIRequestLogExportBatchByTag(db, batch.Tag)
}

// MarkAPIRequestLogExportBatchCleaned records that downstream processing or a
// cold backup has completed. Deleting an export is gated on this marker.
func MarkAPIRequestLogExportBatchCleaned(db *gorm.DB, tag string) (*APIRequestLogExportBatch, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return nil, errors.New("export batch tag is required")
	}
	now := time.Now().UTC().Unix()
	result := db.Model(&APIRequestLogExportBatch{}).
		Where("tag = ? AND status = ? AND integrity_status = ?", tag, APIRequestLogExportBatchStatusCompleted, APIRequestLogExportIntegrityVerified).
		Update("cleaned_at", now)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		batch, err := GetAPIRequestLogExportBatchByTag(db, tag)
		if err != nil {
			return nil, err
		}
		if batch.Status != APIRequestLogExportBatchStatusCompleted {
			return nil, errors.New("only completed export batches can be marked cleaned")
		}
		return nil, errors.New("audit the export batch successfully before marking it cleaned")
	}
	return GetAPIRequestLogExportBatchByTag(db, tag)
}

// DeleteAPIRequestLogExportBatch removes a cleaned batch's metadata and member
// branch. exported_version is deliberately retained on the original turns, so
// deleting an artifact never causes an already-processed turn to be exported
// again by accident.
func DeleteAPIRequestLogExportBatch(db *gorm.DB, tag string) (*APIRequestLogExportBatch, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return nil, errors.New("export batch tag is required")
	}
	var deleted APIRequestLogExportBatch
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tag = ?", tag).First(&deleted).Error; err != nil {
			return err
		}
		if deleted.Status != APIRequestLogExportBatchStatusCompleted {
			return errors.New("only completed export batches can be deleted")
		}
		if deleted.CleanedAt <= 0 {
			return ErrAPIRequestLogExportBatchNotCleaned
		}
		if err := tx.Where("batch_id = ?", deleted.Id).Delete(&APIRequestLogExportMember{}).Error; err != nil {
			return err
		}
		result := tx.Where("id = ?", deleted.Id).Delete(&APIRequestLogExportBatch{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := hydrateAPIRequestLogExportBatchFilter(&deleted); err != nil {
		return nil, err
	}
	return &deleted, nil
}

// ValidateAPIRequestLogTurnForExport is the final integrity barrier before a
// JSONL record is written. The SQL eligibility filter catches cheap database
// inconsistencies; this catches source rows filtered during detail hydration.
func ValidateAPIRequestLogTurnForExport(detail *APIRequestLogTurnDetail) error {
	if detail == nil || detail.Id <= 0 {
		return fmt.Errorf("%w: turn is missing", ErrAPIRequestLogExportDataBroken)
	}
	if detail.CompletionStatus != APIRequestLogTurnStatusCompleted || detail.StartedAt <= 0 || detail.CompletedAt < detail.StartedAt {
		return fmt.Errorf("%w: turn %d has invalid completed timestamps or status", ErrAPIRequestLogExportDataBroken, detail.Id)
	}
	if detail.RequestCount <= 0 || detail.RequestCount != len(detail.Requests) {
		return fmt.Errorf("%w: turn %d has %d stored requests but %d readable requests", ErrAPIRequestLogExportDataBroken, detail.Id, detail.RequestCount, len(detail.Requests))
	}
	if detail.ItemCount <= 0 || detail.ItemCount != len(detail.Items) {
		return fmt.Errorf("%w: turn %d has %d stored items but %d readable items", ErrAPIRequestLogExportDataBroken, detail.Id, detail.ItemCount, len(detail.Items))
	}
	return nil
}

func truncateAPIRequestLogExportError(message string) string {
	const maxLength = 1000
	message = strings.TrimSpace(message)
	if len(message) <= maxLength {
		return message
	}
	return message[:maxLength]
}

func apiRequestLogExportLeaseWindow(leaseDuration time.Duration) (int64, int64, error) {
	if leaseDuration <= 0 {
		return 0, 0, errors.New("export batch lease duration must be positive")
	}
	now := time.Now().UTC()
	leaseExpiresAt := now.Add(leaseDuration).Unix()
	if leaseExpiresAt <= now.Unix() {
		leaseExpiresAt = now.Unix() + 1
	}
	return now.Unix(), leaseExpiresAt, nil
}

func apiRequestLogExportTransitionError(db *gorm.DB, tag string, transitionErr error) error {
	var count int64
	if err := db.Model(&APIRequestLogExportBatch{}).Where("tag = ?", strings.TrimSpace(tag)).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return transitionErr
}

func buildAPIRequestLogExportCandidateQuery(db *gorm.DB, params APIRequestLogTurnQueryParams, includeInferred bool) *gorm.DB {
	params.StartIdx = 0
	params.Num = 0
	tx := buildAPIRequestLogTurnsQuery(db, params).
		Where("completion_status = ?", APIRequestLogTurnStatusCompleted)
	if includeInferred {
		return tx.Where("attribution IN ?", []string{APIRequestLogTurnAttributionExact, APIRequestLogTurnAttributionInferred})
	}
	return tx.Where("attribution = ?", APIRequestLogTurnAttributionExact)
}

// buildAPIRequestLogExportEligibleQuery deliberately excludes records that are
// marked completed but no longer have a consistent request/item graph. This
// makes a completed export an integrity guarantee rather than just a status.
func buildAPIRequestLogExportEligibleQuery(db *gorm.DB, params APIRequestLogTurnQueryParams, includeInferred bool) *gorm.DB {
	return buildAPIRequestLogExportCandidateQuery(db, params, includeInferred).
		Where("NOT " + apiRequestLogTurnBrokenSQL())
}

func apiRequestLogTurnBrokenSQL() string {
	return "(" + apiRequestLogTurnBrokenTimeSQL() + " OR " + apiRequestLogTurnBrokenRequestSQL() + " OR " + apiRequestLogTurnBrokenItemSQL() + ")"
}

func apiRequestLogTurnBrokenTimeSQL() string {
	return "(" + apiRequestLogTurnsTable + ".started_at <= 0 OR " + apiRequestLogTurnsTable + ".completed_at <= 0 OR " + apiRequestLogTurnsTable + ".completed_at < " + apiRequestLogTurnsTable + ".started_at)"
}

func apiRequestLogTurnBrokenRequestSQL() string {
	return "(" + apiRequestLogTurnsTable + ".request_count <= 0 OR " + apiRequestLogTurnsTable + ".request_count <> (SELECT COUNT(*) FROM " + apiRequestLogTurnRequestsTable + " export_request WHERE export_request.turn_record_id = " + apiRequestLogTurnsTable + ".id))"
}

func apiRequestLogTurnBrokenItemSQL() string {
	return "(" + apiRequestLogTurnsTable + ".item_count <= 0 OR " + apiRequestLogTurnsTable + ".item_count <> (SELECT COUNT(*) FROM " + apiRequestLogTurnItemsTable + " export_item_count WHERE export_item_count.turn_record_id = " + apiRequestLogTurnsTable + ".id) OR EXISTS (SELECT 1 FROM " + apiRequestLogTurnItemsTable + " export_item LEFT JOIN " + apiRequestLogItemsTable + " export_source_item ON export_source_item.id = export_item.source_item_id WHERE export_item.turn_record_id = " + apiRequestLogTurnsTable + ".id AND (export_source_item.id IS NULL OR LOWER(COALESCE(export_source_item.content_type, '')) LIKE '%encrypted%')))"
}

// lockAPIRequestLogExportTurnIds rechecks eligibility and freezes real turn
// rows so materializers never need to lock a missing export-member key.
func lockAPIRequestLogExportTurnIds(db *gorm.DB, params APIRequestLogTurnQueryParams, includeInferred bool, cutoffTurnId int64, candidateIds []int64) ([]int64, error) {
	candidateIds = uniquePositiveInt64s(candidateIds)
	if len(candidateIds) == 0 {
		return []int64{}, nil
	}
	sort.Slice(candidateIds, func(i, j int) bool { return candidateIds[i] < candidateIds[j] })
	dialect := ""
	if db.Dialector != nil {
		dialect = db.Dialector.Name()
	}
	query := buildAPIRequestLogExportEligibleQuery(db, params, includeInferred).
		Where(apiRequestLogTurnsTable+".id IN ?", candidateIds).
		Where(apiRequestLogTurnsTable+".id <= ?", cutoffTurnId).
		Where(apiRequestLogTurnAvailableForExportSQL()).
		Order(apiRequestLogTurnsTable + ".id ASC")
	if dialect != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var lockedIds []int64
	if err := query.Pluck(apiRequestLogTurnsTable+".id", &lockedIds).Error; err != nil {
		return nil, err
	}
	if len(lockedIds) > 0 {
		if err := db.Model(&APIRequestLogTurn{}).
			Where("id IN ? AND exported_version = 0", lockedIds).
			Update("exported_version", gorm.Expr("CASE WHEN materialization_version > 0 THEN materialization_version ELSE 1 END")).Error; err != nil {
			return nil, err
		}
	}
	return lockedIds, nil
}

func apiRequestLogExportMemberExistsSQL() string {
	return "EXISTS (SELECT 1 FROM " + apiRequestLogExportMembersTable + " export_member WHERE export_member.turn_record_id = " + apiRequestLogTurnsTable + ".id)"
}

func apiRequestLogTurnExportedSQL() string {
	return "(" + apiRequestLogTurnsTable + ".exported_version > 0 OR " + apiRequestLogExportMemberExistsSQL() + ")"
}

func apiRequestLogTurnAvailableForExportSQL() string {
	return "(" + apiRequestLogTurnsTable + ".exported_version = 0 AND NOT " + apiRequestLogExportMemberExistsSQL() + ")"
}

func apiRequestLogExportFilterFromQuery(params APIRequestLogTurnQueryParams, includeInferred bool) APIRequestLogExportFilter {
	protocols := params.Protocols
	if len(protocols) == 0 && strings.TrimSpace(params.Protocol) != "" {
		protocols = []string{params.Protocol}
	}
	models := params.ModelNames
	if len(models) == 0 && strings.TrimSpace(params.ModelName) != "" {
		models = []string{params.ModelName}
	}
	users := params.Usernames
	if len(users) == 0 && strings.TrimSpace(params.Username) != "" {
		users = []string{params.Username}
	}
	statuses := params.CompletionStatuses
	if len(statuses) == 0 && strings.TrimSpace(params.CompletionStatus) != "" {
		statuses = []string{params.CompletionStatus}
	}
	attributions := params.Attributions
	if len(attributions) == 0 && strings.TrimSpace(params.Attribution) != "" {
		attributions = []string{params.Attribution}
	}
	return APIRequestLogExportFilter{
		StartTimestamp:     normalizeAPIRequestLogTurnTimestamp(params.StartTimestamp),
		EndTimestamp:       normalizeAPIRequestLogTurnTimestamp(params.EndTimestamp),
		SessionId:          strings.TrimSpace(params.SessionId),
		TurnId:             strings.TrimSpace(params.TurnId),
		Protocols:          uniqueNonEmptyStrings(protocols),
		ModelNames:         uniqueNonEmptyStrings(models),
		Usernames:          uniqueNonEmptyStrings(users),
		TokenName:          strings.TrimSpace(params.TokenName),
		CompletionStatuses: uniqueNonEmptyStrings(statuses),
		Attributions:       uniqueNonEmptyStrings(attributions),
		Exported:           params.Exported,
		IncludeInferred:    includeInferred,
	}
}

func hydrateAPIRequestLogExportBatchFilters(batches []*APIRequestLogExportBatch) error {
	for _, batch := range batches {
		if batch == nil {
			continue
		}
		if err := hydrateAPIRequestLogExportBatchFilter(batch); err != nil {
			return err
		}
	}
	return nil
}

func hydrateAPIRequestLogExportBatchFilter(batch *APIRequestLogExportBatch) error {
	if batch == nil || strings.TrimSpace(string(batch.FilterJSON)) == "" {
		return nil
	}
	return common.Unmarshal([]byte(batch.FilterJSON), &batch.Filter)
}

func normalizeAPIRequestLogExportStatuses(statuses []string) []string {
	out := make([]string, 0, len(statuses))
	seen := map[string]bool{}
	for _, status := range statuses {
		status = strings.ToLower(strings.TrimSpace(status))
		switch status {
		case APIRequestLogExportBatchStatusPending, APIRequestLogExportBatchStatusBuilding, APIRequestLogExportBatchStatusCompleted, APIRequestLogExportBatchStatusFailed:
			if !seen[status] {
				seen[status] = true
				out = append(out, status)
			}
		}
	}
	return out
}

func newAPIRequestLogExportTag(now time.Time, filter APIRequestLogExportFilter) (string, error) {
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return "", err
	}
	beijingNow := now.In(location)
	rangeTag := apiRequestLogExportBeijingRangeTag(filter, location)
	if rangeTag == "" {
		rangeTag = beijingNow.Format("20060102")
	}
	return "turn-export-" + rangeTag + "-" + beijingNow.Format("150405") + "-" + hex.EncodeToString(suffix), nil
}

func apiRequestLogExportBeijingRangeTag(filter APIRequestLogExportFilter, location *time.Location) string {
	if location == nil {
		return ""
	}
	if filter.StartTimestamp <= 0 && filter.EndTimestamp <= 0 {
		return ""
	}
	if filter.StartTimestamp <= 0 {
		return "until-" + time.Unix(filter.EndTimestamp, 0).In(location).Format("20060102")
	}
	start := time.Unix(filter.StartTimestamp, 0).In(location)
	if filter.EndTimestamp <= 0 {
		return "from-" + start.Format("20060102")
	}
	end := time.Unix(filter.EndTimestamp, 0).In(location)
	endFormat := "0102"
	if start.Year() != end.Year() {
		endFormat = "20060102"
	}
	return start.Format("20060102") + "-" + end.Format(endFormat)
}
