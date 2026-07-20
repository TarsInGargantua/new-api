package model

import (
	"context"
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
	"gorm.io/gorm/logger"
)

const (
	apiRequestLogOrganizerSessionGap = 30 * time.Minute
	apiRequestLogOrganizerStateKey   = "turn-materialization-v1"
)

type APIRequestLogOrganizerOptions struct {
	BatchSize      int
	AfterID        int64
	MaxRows        int
	LagSeconds     int64
	Sleep          time.Duration
	DryRun         bool
	IgnoreProgress bool

	now func() time.Time
}

type APIRequestLogOrganizerState struct {
	Name      string `json:"name" gorm:"type:varchar(64);primaryKey"`
	LastLogId int64  `json:"last_log_id" gorm:"bigint;not null;default:0"`
	UpdatedAt int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

func (APIRequestLogOrganizerState) TableName() string {
	return "api_request_log_organizer_states"
}

type APIRequestLogOrganizerStats struct {
	Exact     int64
	Inferred  int64
	Unknown   int64
	Open      int64
	Completed int64
	Requests  int64
	Items     int64
	LastID    int64
}

type apiRequestLogOrganizerIdentity struct {
	UserKey  string
	TokenKey string
	Model    string
}

type apiRequestLogOrganizerEnvelope struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	ItemID   string `json:"item_id"`
	CallID   string `json:"call_id"`
	Status   string `json:"status"`
	Phase    string `json:"phase"`
	TurnID   string `json:"turn_id"`
	Metadata struct {
		TurnID string `json:"turn_id"`
	} `json:"metadata"`
	InternalMetadata struct {
		TurnID string `json:"turn_id"`
	} `json:"internal_chat_message_metadata_passthrough"`
}

type apiRequestLogOrganizerCanonicalItem struct {
	Source       APIRequestLogItem
	CanonicalKey string
	MessagePhase string
	ItemStatus   string
	ProviderID   string
	TurnID       string
}

type apiRequestLogOrganizerSessionState struct {
	Identity            apiRequestLogOrganizerIdentity
	SessionID           string
	LastSeenAt          int64
	CurrentTurnID       string
	CurrentTurnIndex    int
	CurrentTurnStatus   string
	CurrentTurnLastSeen int64
	CurrentUserSequence []string
	KnownTurns          map[string]string
}

type apiRequestLogOrganizerCloseAction struct {
	OwnerFingerprint string
	SessionID        string
	TurnID           string
	CompletedAt      int64
}

type apiRequestLogOrganizerRenameAction struct {
	OwnerFingerprint string
	SessionID        string
	FromTurnID       string
	ToTurnID         string
}

type apiRequestLogOrganizerDecision struct {
	Log      APIRequestLog
	Items    []APIRequestLogItem
	ItemMeta []APIRequestLogTurnItemMeta
	Meta     APIRequestLogTurnMeta
	Close    *apiRequestLogOrganizerCloseAction
	Rename   *apiRequestLogOrganizerRenameAction
}

type apiRequestLogOrganizerExistingTurn struct {
	Turn     APIRequestLogTurn
	ItemMeta []APIRequestLogTurnItemMeta
}

type apiRequestLogOrganizerTurnObservation struct {
	Attribution string
	Status      string
}

type apiRequestLogOrganizerTracker struct {
	turns map[string]apiRequestLogOrganizerTurnObservation
}

type apiRequestLogUserSequenceRelation int

const (
	apiRequestLogUserSequenceUnknown apiRequestLogUserSequenceRelation = iota
	apiRequestLogUserSequenceSame
	apiRequestLogUserSequenceAppended
	apiRequestLogUserSequenceStale
	apiRequestLogUserSequenceDiverged
)

func OpenAPIRequestLogOrganizerDB() (*gorm.DB, error) {
	if strings.TrimSpace(common.GetEnvOrDefaultString("REQUEST_LOG_SQL_DSN", "")) == "" {
		return nil, errors.New("REQUEST_LOG_SQL_DSN is required")
	}
	db, err := chooseDedicatedRequestLogDB()
	if err != nil {
		return nil, err
	}
	db.Logger = logger.Default.LogMode(logger.Silent)
	return db, nil
}

func EnsureAPIRequestLogOrganizerStateTable(db *gorm.DB) error {
	if db == nil {
		return errors.New("request log database is not initialized")
	}
	return db.AutoMigrate(&APIRequestLogOrganizerState{})
}

func organizerIdentityForLog(log APIRequestLog) (apiRequestLogOrganizerIdentity, bool) {
	identity := apiRequestLogOrganizerIdentity{Model: strings.TrimSpace(log.ModelName)}
	if log.UserId > 0 {
		identity.UserKey = "id:" + strconv.Itoa(log.UserId)
	} else if username := strings.TrimSpace(log.Username); username != "" {
		identity.UserKey = "name:" + username
	}
	if log.TokenId > 0 {
		identity.TokenKey = "id:" + strconv.Itoa(log.TokenId)
	} else if tokenName := strings.TrimSpace(log.TokenName); tokenName != "" {
		identity.TokenKey = "name:" + tokenName
	}
	return identity, identity.UserKey != "" && identity.TokenKey != "" && identity.Model != ""
}

func (i apiRequestLogOrganizerIdentity) key() string {
	return i.UserKey + "\x00" + i.TokenKey + "\x00" + i.Model
}

func organizerSyntheticSessionID(identity apiRequestLogOrganizerIdentity, firstLogID int64) string {
	return "inferred-session-" + organizerShortHash(identity.key()+"\x00"+strconv.FormatInt(firstLogID, 10))
}

func organizerSyntheticTurnID(sessionID string, firstLogID int64) string {
	return "inferred-turn-" + organizerShortHash(sessionID+"\x00"+strconv.FormatInt(firstLogID, 10))
}

func organizerUnknownSessionID(logID int64) string {
	return fmt.Sprintf("unknown-session-%d", logID)
}

func organizerUnknownTurnID(logID int64) string {
	return fmt.Sprintf("unknown-turn-%d", logID)
}

func organizerShortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

func organizerProviderTurnID(items []APIRequestLogItem) (string, bool) {
	turnIDs := make(map[string]struct{})
	for _, item := range items {
		if item.Phase != APIRequestLogPhaseOutput || item.ContentType != "json" || organizerIsDeltaItem(item) {
			continue
		}
		envelope, ok := organizerItemEnvelope(item)
		if !ok {
			continue
		}
		turnID := firstNonEmptyString(envelope.Metadata.TurnID, envelope.InternalMetadata.TurnID, envelope.TurnID)
		if turnID != "" {
			turnIDs[turnID] = struct{}{}
		}
	}
	if len(turnIDs) != 1 {
		return "", len(turnIDs) > 1
	}
	for turnID := range turnIDs {
		return turnID, false
	}
	return "", false
}

func organizerUserSequence(items []APIRequestLogItem) []string {
	sequence := make([]string, 0)
	for _, item := range items {
		if item.Phase != APIRequestLogPhaseInput || item.ItemType != APIRequestLogItemMessage || strings.ToLower(strings.TrimSpace(item.Role)) != "user" {
			continue
		}
		content := strings.TrimSpace(string(item.Content))
		if content == "" {
			continue
		}
		sequence = append(sequence, organizerShortHash(content))
	}
	return sequence
}

func organizerCompareUserSequences(previous []string, current []string) apiRequestLogUserSequenceRelation {
	if len(previous) == 0 || len(current) == 0 {
		return apiRequestLogUserSequenceUnknown
	}
	commonLength := len(previous)
	if len(current) < commonLength {
		commonLength = len(current)
	}
	for idx := 0; idx < commonLength; idx++ {
		if previous[idx] != current[idx] {
			return apiRequestLogUserSequenceDiverged
		}
	}
	switch {
	case len(previous) == len(current):
		return apiRequestLogUserSequenceSame
	case len(previous) < len(current):
		return apiRequestLogUserSequenceAppended
	default:
		return apiRequestLogUserSequenceStale
	}
}

func organizerCanonicalItems(items []APIRequestLogItem) []apiRequestLogOrganizerCanonicalItem {
	completedProviderItems := make(map[string]bool)
	for _, item := range items {
		envelope, ok := organizerItemEnvelope(item)
		if !ok {
			continue
		}
		providerID := firstNonEmptyString(envelope.ID, envelope.ItemID, item.ToolCallId, envelope.CallID)
		if providerID != "" && strings.EqualFold(envelope.Status, "completed") {
			completedProviderItems[providerID] = true
		}
	}

	out := make([]apiRequestLogOrganizerCanonicalItem, 0, len(items))
	for _, item := range items {
		if item.Id <= 0 || item.ContentType == "encrypted" || organizerIsDeltaItem(item) {
			continue
		}
		envelope, _ := organizerItemEnvelope(item)
		providerID := firstNonEmptyString(envelope.ID, envelope.ItemID, item.ToolCallId, envelope.CallID)
		if providerID != "" && completedProviderItems[providerID] && !strings.EqualFold(envelope.Status, "completed") {
			continue
		}
		if strings.EqualFold(envelope.Status, "in_progress") {
			continue
		}
		canonicalKey := organizerItemCanonicalKey(item, providerID)
		if canonicalKey == "" {
			continue
		}
		out = append(out, apiRequestLogOrganizerCanonicalItem{
			Source:       item,
			CanonicalKey: canonicalKey,
			MessagePhase: strings.TrimSpace(envelope.Phase),
			ItemStatus:   strings.TrimSpace(envelope.Status),
			ProviderID:   providerID,
			TurnID:       firstNonEmptyString(envelope.Metadata.TurnID, envelope.InternalMetadata.TurnID, envelope.TurnID),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source.Seq == out[j].Source.Seq {
			return out[i].Source.Id < out[j].Source.Id
		}
		return out[i].Source.Seq < out[j].Source.Seq
	})
	return out
}

func organizerTurnItemMeta(items []apiRequestLogOrganizerCanonicalItem) []APIRequestLogTurnItemMeta {
	meta := make([]APIRequestLogTurnItemMeta, 0, len(items))
	for _, item := range items {
		meta = append(meta, APIRequestLogTurnItemMeta{
			Seq:            item.Source.Seq,
			ProviderItemId: item.ProviderID,
			TurnId:         item.TurnID,
			MessagePhase:   item.MessagePhase,
			ItemStatus:     item.ItemStatus,
		})
	}
	return meta
}

func organizerItemEnvelope(item APIRequestLogItem) (apiRequestLogOrganizerEnvelope, bool) {
	var envelope apiRequestLogOrganizerEnvelope
	if item.ContentType != "json" || strings.TrimSpace(string(item.Content)) == "" {
		return envelope, false
	}
	if err := common.Unmarshal([]byte(item.Content), &envelope); err != nil {
		return apiRequestLogOrganizerEnvelope{}, false
	}
	return envelope, true
}

func organizerIsDeltaItem(item APIRequestLogItem) bool {
	source := strings.ToLower(strings.TrimSpace(item.Source))
	if strings.Contains(source, ".delta") {
		return true
	}
	envelope, ok := organizerItemEnvelope(item)
	if !ok {
		return false
	}
	eventType := strings.ToLower(strings.TrimSpace(envelope.Type))
	return strings.HasSuffix(eventType, ".delta") || strings.HasSuffix(eventType, ".done")
}

func organizerItemCanonicalKey(item APIRequestLogItem, providerID string) string {
	itemType := strings.TrimSpace(item.ItemType)
	role := strings.ToLower(strings.TrimSpace(item.Role))
	name := strings.TrimSpace(item.Name)
	callID := firstNonEmptyString(strings.TrimSpace(item.ToolCallId), providerID)
	content := strings.TrimSpace(string(item.Content))
	if itemType == "" || content == "" {
		return ""
	}
	semantic := strings.Join([]string{item.Phase, itemType, role, callID, name, content}, "\x00")
	return itemType + ":" + organizerShortHash(semantic)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func OrganizeAPIRequestLogTurns(ctx context.Context, db *gorm.DB, options APIRequestLogOrganizerOptions) (APIRequestLogOrganizerStats, error) {
	var stats APIRequestLogOrganizerStats
	if db == nil {
		return stats, errors.New("request log database is not initialized")
	}
	if err := normalizeAPIRequestLogOrganizerOptions(&options); err != nil {
		return stats, err
	}
	if !db.Migrator().HasTable(&APIRequestLog{}) || !db.Migrator().HasTable(&APIRequestLogItem{}) {
		return stats, errors.New("request log source tables are not initialized")
	}
	if !options.DryRun {
		if err := EnsureAPIRequestLogMaterializedTables(db); err != nil {
			return stats, err
		}
		if err := EnsureAPIRequestLogOrganizerStateTable(db); err != nil {
			return stats, err
		}
	}

	now := time.Now
	if options.now != nil {
		now = options.now
	}
	cutoff := now().Unix() - options.LagSeconds
	cursor := options.AfterID
	if !options.DryRun && options.AfterID == 0 && !options.IgnoreProgress {
		savedProgress, err := loadAPIRequestLogOrganizerProgress(ctx, db)
		if err != nil {
			return stats, err
		}
		cursor = savedProgress
	}
	stats.LastID = cursor
	remaining := options.MaxRows
	states := make(map[string]*apiRequestLogOrganizerSessionState)
	tracker := apiRequestLogOrganizerTracker{turns: make(map[string]apiRequestLogOrganizerTurnObservation)}

	for {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		limit := options.BatchSize
		if remaining > 0 && remaining < limit {
			limit = remaining
		}
		if limit <= 0 {
			break
		}
		logs, blockedByLag, err := loadAPIRequestLogOrganizerParents(ctx, db, cursor, limit, cutoff)
		if err != nil {
			return stats, err
		}
		if len(logs) == 0 {
			break
		}
		itemsByLog, err := loadAPIRequestLogOrganizerItems(ctx, db, logs)
		if err != nil {
			return stats, err
		}
		existing, err := loadAPIRequestLogOrganizerExistingTurns(ctx, db, logs, options.DryRun)
		if err != nil {
			return stats, err
		}

		batchStates := cloneAPIRequestLogOrganizerStates(states)
		decisions := make([]apiRequestLogOrganizerDecision, 0, len(logs))
		for idx := range logs {
			decision, err := planAPIRequestLogOrganizerDecision(ctx, db, logs[idx], itemsByLog[logs[idx].Id], existing[logs[idx].Id], batchStates, options.DryRun)
			if err != nil {
				return stats, err
			}
			decision.Meta.Items = decision.ItemMeta
			decision.Meta = normalizeAPIRequestLogTurnMeta(&decision.Log, decision.Meta)
			decisions = append(decisions, decision)
			tracker.observe(apiRequestLogOwnerFingerprint(&decision.Log), decision.Meta)
			if decision.Close != nil {
				tracker.complete(decision.Close.OwnerFingerprint, decision.Close.SessionID, decision.Close.TurnID)
			}
			stats.Requests++
			stats.Items += int64(len(decision.Items))
			stats.LastID = int64(logs[idx].Id)
		}

		if !options.DryRun {
			batchLastID := int64(logs[len(logs)-1].Id)
			err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				for idx := range decisions {
					if err := applyAPIRequestLogOrganizerDecision(tx, &decisions[idx]); err != nil {
						return err
					}
				}
				return advanceAPIRequestLogOrganizerProgress(tx, batchLastID)
			})
			if err != nil {
				return stats, err
			}
		}
		states = batchStates
		cursor = stats.LastID
		if remaining > 0 {
			remaining -= len(logs)
		}
		if blockedByLag || (remaining == 0 && options.MaxRows > 0) {
			break
		}
		if options.Sleep > 0 {
			select {
			case <-ctx.Done():
				return stats, ctx.Err()
			case <-time.After(options.Sleep):
			}
		}
	}

	tracker.writeStats(&stats)
	return stats, nil
}

func loadAPIRequestLogOrganizerProgress(ctx context.Context, db *gorm.DB) (int64, error) {
	var state APIRequestLogOrganizerState
	err := db.WithContext(ctx).Where("name = ?", apiRequestLogOrganizerStateKey).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return state.LastLogId, nil
}

func advanceAPIRequestLogOrganizerProgress(tx *gorm.DB, lastLogID int64) error {
	if tx == nil || lastLogID <= 0 {
		return nil
	}
	state := APIRequestLogOrganizerState{Name: apiRequestLogOrganizerStateKey, LastLogId: lastLogID}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "name"}},
		DoNothing: true,
	}).Create(&state).Error; err != nil {
		return err
	}
	return tx.Model(&APIRequestLogOrganizerState{}).
		Where("name = ? AND last_log_id < ?", apiRequestLogOrganizerStateKey, lastLogID).
		Update("last_log_id", lastLogID).Error
}

func normalizeAPIRequestLogOrganizerOptions(options *APIRequestLogOrganizerOptions) error {
	if options == nil {
		return errors.New("organizer options are required")
	}
	if options.BatchSize <= 0 {
		return errors.New("batch size must be greater than 0")
	}
	if options.AfterID < 0 {
		return errors.New("after id must be greater than or equal to 0")
	}
	if options.MaxRows < 0 {
		return errors.New("max rows must be greater than or equal to 0")
	}
	if options.LagSeconds < 0 {
		return errors.New("lag seconds must be greater than or equal to 0")
	}
	if options.Sleep < 0 {
		return errors.New("sleep must be greater than or equal to 0")
	}
	return nil
}

func loadAPIRequestLogOrganizerParents(ctx context.Context, db *gorm.DB, afterID int64, limit int, cutoff int64) ([]APIRequestLog, bool, error) {
	var fetched []APIRequestLog
	if err := db.WithContext(ctx).Where("id > ?", afterID).Order("id ASC").Limit(limit).Find(&fetched).Error; err != nil {
		return nil, false, err
	}
	for idx := range fetched {
		if normalizeAPIRequestLogTurnTimestamp(fetched[idx].CreatedAt) > cutoff {
			return fetched[:idx], true, nil
		}
	}
	return fetched, false, nil
}

func loadAPIRequestLogOrganizerItems(ctx context.Context, db *gorm.DB, logs []APIRequestLog) (map[int][]APIRequestLogItem, error) {
	byLog := make(map[int][]APIRequestLogItem, len(logs))
	if len(logs) == 0 {
		return byLog, nil
	}
	logIDs := make([]int, 0, len(logs))
	for idx := range logs {
		logIDs = append(logIDs, logs[idx].Id)
	}
	var items []APIRequestLogItem
	if err := db.WithContext(ctx).Where("log_id IN ?", logIDs).Order("log_id ASC").Order("seq ASC").Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	for _, item := range items {
		byLog[item.LogId] = append(byLog[item.LogId], item)
	}
	return byLog, nil
}

func loadAPIRequestLogOrganizerExistingTurns(ctx context.Context, db *gorm.DB, logs []APIRequestLog, dryRun bool) (map[int]apiRequestLogOrganizerExistingTurn, error) {
	existing := make(map[int]apiRequestLogOrganizerExistingTurn)
	if dryRun || len(logs) == 0 || !db.Migrator().HasTable(&APIRequestLogTurnRequest{}) {
		return existing, nil
	}
	logIDs := make([]int, 0, len(logs))
	for idx := range logs {
		logIDs = append(logIDs, logs[idx].Id)
	}
	var requests []APIRequestLogTurnRequest
	if err := db.WithContext(ctx).Where("log_id IN ?", logIDs).Find(&requests).Error; err != nil {
		return nil, err
	}
	turnIDs := make([]int64, 0, len(requests))
	turnByLog := make(map[int]int64, len(requests))
	for _, request := range requests {
		turnIDs = append(turnIDs, request.TurnRecordId)
		turnByLog[request.LogId] = request.TurnRecordId
	}
	if len(turnIDs) == 0 {
		return existing, nil
	}
	var turns []APIRequestLogTurn
	if err := db.WithContext(ctx).Where("id IN ?", uniquePositiveInt64s(turnIDs)).Find(&turns).Error; err != nil {
		return nil, err
	}
	turnByID := make(map[int64]APIRequestLogTurn, len(turns))
	for _, turn := range turns {
		turnByID[turn.Id] = turn
	}
	for logID, turnID := range turnByLog {
		if turn, ok := turnByID[turnID]; ok {
			existing[logID] = apiRequestLogOrganizerExistingTurn{Turn: turn}
		}
	}
	type existingItemMetaRow struct {
		LogId          int
		TurnRecordId   int64
		Seq            int
		ProviderItemId string
		MessagePhase   string
		ItemStatus     string
	}
	var itemMetaRows []existingItemMetaRow
	if err := db.WithContext(ctx).
		Table(apiRequestLogTurnItemsTable+" turn_item").
		Select("turn_request.log_id, turn_item.turn_record_id, source_item.seq, turn_item.provider_item_id, turn_item.message_phase, turn_item.item_status").
		Joins("JOIN "+apiRequestLogTurnRequestsTable+" turn_request ON turn_request.id = turn_item.request_record_id").
		Joins("JOIN api_request_log_items source_item ON source_item.id = turn_item.source_item_id").
		Where("turn_request.log_id IN ?", logIDs).
		Order("turn_request.log_id ASC").Order("source_item.seq ASC").Order("turn_item.id ASC").
		Scan(&itemMetaRows).Error; err != nil {
		return nil, err
	}
	for _, row := range itemMetaRows {
		observation, ok := existing[row.LogId]
		if !ok {
			continue
		}
		observation.ItemMeta = append(observation.ItemMeta, APIRequestLogTurnItemMeta{
			Seq: row.Seq, ProviderItemId: row.ProviderItemId, TurnId: observation.Turn.TurnId,
			MessagePhase: row.MessagePhase, ItemStatus: row.ItemStatus,
		})
		existing[row.LogId] = observation
	}
	return existing, nil
}

func planAPIRequestLogOrganizerDecision(ctx context.Context, db *gorm.DB, log APIRequestLog, rawItems []APIRequestLogItem, existing apiRequestLogOrganizerExistingTurn, states map[string]*apiRequestLogOrganizerSessionState, dryRun bool) (apiRequestLogOrganizerDecision, error) {
	canonical := organizerCanonicalItems(rawItems)
	items := make([]APIRequestLogItem, 0, len(canonical))
	for _, item := range canonical {
		items = append(items, item.Source)
	}
	decision := apiRequestLogOrganizerDecision{Log: log, Items: items, ItemMeta: organizerTurnItemMeta(canonical)}
	protocol := organizerProtocol(log)
	userSequence := organizerUserSequence(rawItems)
	identity, hasIdentity := organizerIdentityForLog(log)

	if existing.Turn.Id > 0 {
		turn := existing.Turn
		decision.ItemMeta = mergeAPIRequestLogOrganizerItemMeta(decision.ItemMeta, existing.ItemMeta)
		decision.Meta = APIRequestLogTurnMeta{
			SessionId: turn.SessionId, TurnId: turn.TurnId, Protocol: turn.Protocol,
			StartedAt: turn.StartedAt, CompletedAt: turn.CompletedAt,
			CompletionStatus: turn.CompletionStatus, Attribution: turn.Attribution,
			WindowId: turn.WindowId, RequestKind: turn.RequestKind,
		}
		if hasIdentity && turn.Attribution == APIRequestLogTurnAttributionInferred {
			observeAPIRequestLogOrganizerExistingTurn(states, identity, turn, log, userSequence)
		}
		return decision, nil
	}

	providerTurnID, ambiguousProvider := organizerProviderTurnID(rawItems)
	if !hasIdentity || ambiguousProvider || (providerTurnID == "" && len(userSequence) == 0) {
		decision.Meta = organizerUnknownMeta(log, protocol)
		return decision, nil
	}

	state := states[identity.key()]
	if state == nil && !dryRun {
		var err error
		state, err = restoreAPIRequestLogOrganizerSession(ctx, db, identity, log)
		if err != nil {
			return decision, err
		}
		if state != nil {
			states[identity.key()] = state
		}
	}
	createdAt := normalizeAPIRequestLogTurnTimestamp(log.CreatedAt)
	if state == nil || createdAt-state.LastSeenAt > int64(apiRequestLogOrganizerSessionGap/time.Second) {
		state = &apiRequestLogOrganizerSessionState{
			Identity: identity, SessionID: organizerSyntheticSessionID(identity, int64(log.Id)),
			KnownTurns: make(map[string]string),
		}
		states[identity.key()] = state
	}

	if providerTurnID != "" {
		if status, ok := state.KnownTurns[providerTurnID]; ok {
			decision.Meta = organizerInferredMeta(log, state.SessionID, providerTurnID, protocol, status, 0)
			if state.CurrentTurnID == providerTurnID {
				state.CurrentUserSequence = append([]string(nil), userSequence...)
				state.CurrentTurnLastSeen = maxInt64(state.CurrentTurnLastSeen, createdAt)
			}
			state.LastSeenAt = maxInt64(state.LastSeenAt, createdAt)
			return decision, nil
		}
		relation := organizerCompareUserSequences(state.CurrentUserSequence, userSequence)
		if strings.HasPrefix(state.CurrentTurnID, "inferred-turn-") && (relation == apiRequestLogUserSequenceSame || relation == apiRequestLogUserSequenceUnknown) {
			decision.Rename = &apiRequestLogOrganizerRenameAction{
				OwnerFingerprint: apiRequestLogOwnerFingerprint(&log),
				SessionID:        state.SessionID,
				FromTurnID:       state.CurrentTurnID,
				ToTurnID:         providerTurnID,
			}
			delete(state.KnownTurns, state.CurrentTurnID)
			state.CurrentTurnID = providerTurnID
		} else {
			decision.Close = organizerCloseCurrentTurn(state, apiRequestLogOwnerFingerprint(&log))
			state.CurrentTurnID = providerTurnID
			state.CurrentTurnIndex++
		}
		state.CurrentTurnStatus = APIRequestLogTurnStatusOpen
		state.CurrentTurnLastSeen = createdAt
		state.CurrentUserSequence = append([]string(nil), userSequence...)
		state.KnownTurns[providerTurnID] = APIRequestLogTurnStatusOpen
		state.LastSeenAt = maxInt64(state.LastSeenAt, createdAt)
		decision.Meta = organizerInferredMeta(log, state.SessionID, providerTurnID, protocol, APIRequestLogTurnStatusOpen, 0)
		return decision, nil
	}

	relation := organizerCompareUserSequences(state.CurrentUserSequence, userSequence)
	switch {
	case state.CurrentTurnID == "":
		turnID := organizerSyntheticTurnID(state.SessionID, int64(log.Id))
		state.CurrentTurnID = turnID
		state.CurrentTurnIndex = 1
		state.CurrentTurnStatus = APIRequestLogTurnStatusOpen
		state.CurrentTurnLastSeen = createdAt
		state.CurrentUserSequence = append([]string(nil), userSequence...)
		state.KnownTurns[turnID] = APIRequestLogTurnStatusOpen
		decision.Meta = organizerInferredMeta(log, state.SessionID, turnID, protocol, APIRequestLogTurnStatusOpen, 0)
	case relation == apiRequestLogUserSequenceSame:
		state.CurrentTurnLastSeen = maxInt64(state.CurrentTurnLastSeen, createdAt)
		decision.Meta = organizerInferredMeta(log, state.SessionID, state.CurrentTurnID, protocol, state.CurrentTurnStatus, 0)
	case relation == apiRequestLogUserSequenceAppended:
		decision.Close = organizerCloseCurrentTurn(state, apiRequestLogOwnerFingerprint(&log))
		turnID := organizerSyntheticTurnID(state.SessionID, int64(log.Id))
		state.CurrentTurnID = turnID
		state.CurrentTurnIndex++
		state.CurrentTurnStatus = APIRequestLogTurnStatusOpen
		state.CurrentTurnLastSeen = createdAt
		state.CurrentUserSequence = append([]string(nil), userSequence...)
		state.KnownTurns[turnID] = APIRequestLogTurnStatusOpen
		decision.Meta = organizerInferredMeta(log, state.SessionID, turnID, protocol, APIRequestLogTurnStatusOpen, 0)
	default:
		decision.Meta = organizerUnknownMeta(log, protocol)
		return decision, nil
	}
	state.LastSeenAt = maxInt64(state.LastSeenAt, createdAt)
	return decision, nil
}

func mergeAPIRequestLogOrganizerItemMeta(current, existing []APIRequestLogTurnItemMeta) []APIRequestLogTurnItemMeta {
	merged := append([]APIRequestLogTurnItemMeta(nil), current...)
	bySeq := make(map[int]int, len(merged))
	for index := range merged {
		bySeq[merged[index].Seq] = index
	}
	for _, item := range existing {
		index, ok := bySeq[item.Seq]
		if !ok {
			bySeq[item.Seq] = len(merged)
			merged = append(merged, item)
			continue
		}
		if value := strings.TrimSpace(item.ProviderItemId); value != "" {
			merged[index].ProviderItemId = value
		}
		if value := strings.TrimSpace(item.TurnId); value != "" {
			merged[index].TurnId = value
		}
		if value := strings.TrimSpace(item.MessagePhase); value != "" {
			merged[index].MessagePhase = value
		}
		if value := strings.TrimSpace(item.ItemStatus); value != "" {
			merged[index].ItemStatus = value
		}
	}
	return merged
}

func observeAPIRequestLogOrganizerExistingTurn(states map[string]*apiRequestLogOrganizerSessionState, identity apiRequestLogOrganizerIdentity, turn APIRequestLogTurn, log APIRequestLog, userSequence []string) {
	key := identity.key()
	state := states[key]
	if state == nil || state.SessionID != turn.SessionId {
		state = &apiRequestLogOrganizerSessionState{Identity: identity, SessionID: turn.SessionId, KnownTurns: make(map[string]string)}
		states[key] = state
	}
	state.KnownTurns[turn.TurnId] = turn.CompletionStatus
	createdAt := normalizeAPIRequestLogTurnTimestamp(log.CreatedAt)
	if state.CurrentTurnID == "" || turn.TurnIndex >= state.CurrentTurnIndex {
		state.CurrentTurnID = turn.TurnId
		state.CurrentTurnIndex = turn.TurnIndex
		state.CurrentTurnStatus = turn.CompletionStatus
		state.CurrentTurnLastSeen = createdAt
		state.CurrentUserSequence = append([]string(nil), userSequence...)
	}
	state.LastSeenAt = maxInt64(state.LastSeenAt, createdAt)
}

func restoreAPIRequestLogOrganizerSession(ctx context.Context, db *gorm.DB, identity apiRequestLogOrganizerIdentity, log APIRequestLog) (*apiRequestLogOrganizerSessionState, error) {
	createdAt := normalizeAPIRequestLogTurnTimestamp(log.CreatedAt)
	ownerFingerprint := apiRequestLogOwnerFingerprint(&log)
	query := db.WithContext(ctx).Table(apiRequestLogTurnRequestsTable+" turn_request").
		Select("turn_row.*, turn_request.log_id AS restore_log_id, turn_request.created_at AS restore_created_at").
		Joins("JOIN "+apiRequestLogTurnsTable+" turn_row ON turn_row.id = turn_request.turn_record_id").
		Where("turn_row.owner_fingerprint = ?", ownerFingerprint).
		Where("turn_row.attribution = ? AND turn_row.session_id LIKE ?", APIRequestLogTurnAttributionInferred, "inferred-session-%").
		Where("turn_row.model_name = ?", log.ModelName).
		Where("turn_request.created_at <= ? AND turn_request.created_at >= ?", createdAt, createdAt-int64(apiRequestLogOrganizerSessionGap/time.Second)).
		Where("turn_request.log_id < ?", log.Id)
	if log.UserId > 0 {
		query = query.Where("turn_row.user_id = ?", log.UserId)
	} else {
		query = query.Where("turn_row.user_id = 0 AND turn_row.username = ?", log.Username)
	}
	if log.TokenId > 0 {
		query = query.Where("turn_row.token_id = ?", log.TokenId)
	} else {
		query = query.Where("turn_row.token_id = 0 AND turn_row.token_name = ?", log.TokenName)
	}
	var row struct {
		APIRequestLogTurn
		RestoreLogID     int   `gorm:"column:restore_log_id"`
		RestoreCreatedAt int64 `gorm:"column:restore_created_at"`
	}
	err := query.Order("turn_request.created_at DESC").Order("turn_request.log_id DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sourceItems []APIRequestLogItem
	if err := db.WithContext(ctx).Where("log_id = ?", row.RestoreLogID).Order("seq ASC").Order("id ASC").Find(&sourceItems).Error; err != nil {
		return nil, err
	}
	state := &apiRequestLogOrganizerSessionState{
		Identity: identity, SessionID: row.SessionId, LastSeenAt: row.RestoreCreatedAt,
		CurrentTurnID: row.TurnId, CurrentTurnIndex: row.TurnIndex,
		CurrentTurnStatus: row.CompletionStatus, CurrentTurnLastSeen: row.RestoreCreatedAt,
		CurrentUserSequence: organizerUserSequence(sourceItems), KnownTurns: make(map[string]string),
	}
	var sessionTurns []APIRequestLogTurn
	if err := db.WithContext(ctx).
		Where("owner_fingerprint = ? AND session_id = ?", ownerFingerprint, row.SessionId).
		Find(&sessionTurns).Error; err != nil {
		return nil, err
	}
	for _, turn := range sessionTurns {
		state.KnownTurns[turn.TurnId] = turn.CompletionStatus
	}
	return state, nil
}

func organizerCloseCurrentTurn(state *apiRequestLogOrganizerSessionState, ownerFingerprint string) *apiRequestLogOrganizerCloseAction {
	if state == nil || state.CurrentTurnID == "" || state.CurrentTurnStatus == APIRequestLogTurnStatusCompleted {
		return nil
	}
	action := &apiRequestLogOrganizerCloseAction{
		OwnerFingerprint: ownerFingerprint,
		SessionID:        state.SessionID,
		TurnID:           state.CurrentTurnID,
		CompletedAt:      state.CurrentTurnLastSeen,
	}
	state.CurrentTurnStatus = APIRequestLogTurnStatusCompleted
	state.KnownTurns[state.CurrentTurnID] = APIRequestLogTurnStatusCompleted
	return action
}

func organizerUnknownMeta(log APIRequestLog, protocol string) APIRequestLogTurnMeta {
	return APIRequestLogTurnMeta{
		SessionId: organizerUnknownSessionID(int64(log.Id)), TurnId: organizerUnknownTurnID(int64(log.Id)),
		Protocol: protocol, StartedAt: log.CreatedAt,
		CompletionStatus: APIRequestLogTurnStatusUnknown, Attribution: APIRequestLogTurnAttributionUnknown,
	}
}

func organizerInferredMeta(log APIRequestLog, sessionID, turnID, protocol, status string, completedAt int64) APIRequestLogTurnMeta {
	return APIRequestLogTurnMeta{
		SessionId: sessionID, TurnId: turnID, Protocol: protocol, StartedAt: log.CreatedAt,
		CompletedAt: completedAt, CompletionStatus: status, Attribution: APIRequestLogTurnAttributionInferred,
	}
}

func organizerProtocol(log APIRequestLog) string {
	format := strings.ToLower(strings.TrimSpace(log.APIFormat))
	switch {
	case strings.Contains(format, "responses"), strings.Contains(log.RequestPath, "/responses"):
		return "responses"
	case strings.Contains(format, "claude"), strings.Contains(log.RequestPath, "/messages"):
		return "claude_messages"
	case strings.Contains(format, "gemini"), strings.Contains(log.RequestPath, "generateContent"):
		return "gemini"
	case strings.Contains(format, "chat"), strings.Contains(format, "openai"), strings.Contains(log.RequestPath, "/chat/completions"):
		return "chat_completions"
	case format != "":
		return format
	default:
		return "unknown"
	}
}

func applyAPIRequestLogOrganizerDecision(tx *gorm.DB, decision *apiRequestLogOrganizerDecision) error {
	if decision == nil {
		return nil
	}
	if decision.Close != nil {
		if err := lockAPIRequestLogOrganizerTurnForExportCoordination(
			tx,
			decision.Close.OwnerFingerprint,
			decision.Close.SessionID,
			decision.Close.TurnID,
		); err != nil {
			return err
		}
		updates := map[string]interface{}{
			"completion_status": APIRequestLogTurnStatusCompleted,
			"completed_at":      decision.Close.CompletedAt,
		}
		if err := tx.Model(&APIRequestLogTurn{}).
			Where("owner_fingerprint = ? AND session_id = ? AND turn_id = ?", decision.Close.OwnerFingerprint, decision.Close.SessionID, decision.Close.TurnID).
			Where("completion_status <> ?", APIRequestLogTurnStatusCompleted).
			Where("NOT " + apiRequestLogExportMemberExistsSQL()).
			Updates(updates).Error; err != nil {
			return err
		}
	}
	if decision.Rename != nil && decision.Rename.FromTurnID != decision.Rename.ToTurnID {
		if err := lockAPIRequestLogOrganizerTurnForExportCoordination(
			tx,
			decision.Rename.OwnerFingerprint,
			decision.Rename.SessionID,
			decision.Rename.FromTurnID,
		); err != nil {
			return err
		}
		var targetCount int64
		if err := tx.Model(&APIRequestLogTurn{}).
			Where("owner_fingerprint = ? AND session_id = ? AND turn_id = ?", decision.Rename.OwnerFingerprint, decision.Rename.SessionID, decision.Rename.ToTurnID).
			Count(&targetCount).Error; err != nil {
			return err
		}
		if targetCount == 0 {
			if err := tx.Model(&APIRequestLogTurn{}).
				Where("owner_fingerprint = ? AND session_id = ? AND turn_id = ?", decision.Rename.OwnerFingerprint, decision.Rename.SessionID, decision.Rename.FromTurnID).
				Where("NOT "+apiRequestLogExportMemberExistsSQL()).
				Update("turn_id", decision.Rename.ToTurnID).Error; err != nil {
				return err
			}
		}
	}
	decision.Meta.Items = decision.ItemMeta
	_, err := materializeAPIRequestLogOrganizerTurn(tx, &decision.Log, decision.Meta, decision.Items)
	return err
}

func lockAPIRequestLogOrganizerTurnForExportCoordination(tx *gorm.DB, ownerFingerprint, sessionId, turnId string) error {
	if tx == nil || tx.Dialector == nil || tx.Dialector.Name() != "postgres" {
		return nil
	}
	var turn APIRequestLogTurn
	err := tx.Select("id").
		Where("owner_fingerprint = ? AND session_id = ? AND turn_id = ?", ownerFingerprint, sessionId, turnId).
		First(&turn).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return lockAPIRequestLogTurnForExportCoordination(tx, turn.Id)
}

func materializeAPIRequestLogOrganizerTurn(tx *gorm.DB, log *APIRequestLog, meta APIRequestLogTurnMeta, items []APIRequestLogItem) (*APIRequestLogTurn, error) {
	meta = normalizeAPIRequestLogTurnMeta(log, meta)
	turn, _, err := findOrCreateAPIRequestLogTurn(tx, log, meta)
	if err != nil {
		return nil, err
	}
	if err := lockAPIRequestLogTurnForExportCoordination(tx, turn.Id); err != nil {
		return nil, err
	}
	var exportMemberCount int64
	if err := tx.Model(&APIRequestLogExportMember{}).
		Where("turn_record_id = ?", turn.Id).
		Limit(1).
		Count(&exportMemberCount).Error; err != nil {
		return nil, err
	}
	if exportMemberCount > 0 {
		return turn, nil
	}
	priorInput, priorContext, err := latestAPIRequestLogTurnFingerprints(tx, turn.OwnerFingerprint, turn.SessionId, normalizeAPIRequestLogTurnTimestamp(log.CreatedAt), log.Id)
	if err != nil {
		return nil, err
	}
	candidates := buildAPIRequestLogTurnCandidates(items, meta)
	inputKeys, contextKeys := apiRequestLogTurnCandidateFingerprints(candidates)
	prefixLength := longestAPIRequestLogTurnPrefix(inputKeys, priorInput, priorContext)
	request, err := upsertAPIRequestLogTurnRequest(tx, turn.Id, log, inputKeys, contextKeys)
	if err != nil {
		return nil, err
	}
	if err := mapAPIRequestLogTurnItems(tx, turn.Id, request.Id, candidates, prefixLength); err != nil {
		return nil, err
	}
	if err := refreshAPIRequestLogTurn(tx, turn, log, meta); err != nil {
		return nil, err
	}
	return turn, nil
}

func cloneAPIRequestLogOrganizerStates(states map[string]*apiRequestLogOrganizerSessionState) map[string]*apiRequestLogOrganizerSessionState {
	out := make(map[string]*apiRequestLogOrganizerSessionState, len(states))
	for key, state := range states {
		clone := *state
		clone.CurrentUserSequence = append([]string(nil), state.CurrentUserSequence...)
		clone.KnownTurns = make(map[string]string, len(state.KnownTurns))
		for turnID, status := range state.KnownTurns {
			clone.KnownTurns[turnID] = status
		}
		out[key] = &clone
	}
	return out
}

func (t *apiRequestLogOrganizerTracker) observe(ownerFingerprint string, meta APIRequestLogTurnMeta) {
	key := ownerFingerprint + "\x00" + meta.SessionId + "\x00" + meta.TurnId
	current := t.turns[key]
	current.Attribution = strongerAPIRequestLogTurnAttribution(current.Attribution, meta.Attribution)
	current.Status = strongerAPIRequestLogTurnStatus(current.Status, meta.CompletionStatus)
	t.turns[key] = current
}

func (t *apiRequestLogOrganizerTracker) complete(ownerFingerprint, sessionID, turnID string) {
	key := ownerFingerprint + "\x00" + sessionID + "\x00" + turnID
	current := t.turns[key]
	current.Attribution = strongerAPIRequestLogTurnAttribution(current.Attribution, APIRequestLogTurnAttributionInferred)
	current.Status = APIRequestLogTurnStatusCompleted
	t.turns[key] = current
}

func (t *apiRequestLogOrganizerTracker) writeStats(stats *APIRequestLogOrganizerStats) {
	for _, turn := range t.turns {
		switch normalizeAPIRequestLogTurnAttribution(turn.Attribution) {
		case APIRequestLogTurnAttributionExact:
			stats.Exact++
		case APIRequestLogTurnAttributionInferred:
			stats.Inferred++
		default:
			stats.Unknown++
		}
		switch normalizeAPIRequestLogTurnStatus(turn.Status) {
		case APIRequestLogTurnStatusOpen:
			stats.Open++
		case APIRequestLogTurnStatusCompleted:
			stats.Completed++
		}
	}
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
