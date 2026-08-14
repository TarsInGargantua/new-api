package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

const (
	// A turn can reference hundreds of raw items. Keep pages small enough that
	// one export never creates a giant source-item IN query or loses its lease
	// while MySQL is under load.
	requestLogExportPageSize     = 25
	requestLogExportLease        = 10 * time.Minute
	requestLogExportPollInterval = time.Second
)

type requestLogExportWorker struct {
	db    *gorm.DB
	dir   string
	owner string
	wake  chan struct{}
}

type stagedExportArtifactDeletion struct {
	originalPath string
	stagedPath   string
}

func newRequestLogExportWorker(db *gorm.DB, dir string) (*requestLogExportWorker, error) {
	return newRequestLogExportWorkerWithAutoRun(db, dir, true)
}

func newRequestLogExportWorkerWithAutoRun(db *gorm.DB, dir string, autoRun bool) (*requestLogExportWorker, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	absDir, err := filepath.Abs(strings.TrimSpace(dir))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absDir, 0o750); err != nil {
		return nil, err
	}
	owner, err := newRequestLogExportWorkerOwner()
	if err != nil {
		return nil, err
	}
	worker := &requestLogExportWorker{
		db:    db,
		dir:   absDir,
		owner: owner,
		wake:  make(chan struct{}, 1),
	}
	if autoRun {
		go worker.run()
	}
	return worker, nil
}

func (w *requestLogExportWorker) Recover() error {
	var count int64
	if err := w.db.Model(&model.APIRequestLogExportBatch{}).Where("status IN ?", []string{
		model.APIRequestLogExportBatchStatusPending,
		model.APIRequestLogExportBatchStatusBuilding,
	}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		w.Enqueue("recovery")
	}
	return nil
}

func (w *requestLogExportWorker) Enqueue(tag string) {
	if w == nil || strings.TrimSpace(tag) == "" {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *requestLogExportWorker) run() {
	ticker := time.NewTicker(requestLogExportPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.wake:
		case <-ticker.C:
		}
		if err := w.drain(); err != nil {
			fmt.Fprintf(os.Stderr, "request log export worker: %v\n", err)
		}
	}
}

func (w *requestLogExportWorker) drain() error {
	for {
		batch, err := model.ClaimNextAPIRequestLogExportBatch(w.db, w.owner, requestLogExportLease)
		if errors.Is(err, model.ErrAPIRequestLogExportBatchNotClaimable) {
			return nil
		}
		if err != nil {
			return err
		}
		_ = w.buildClaimed(batch)
	}
}

func (w *requestLogExportWorker) build(tag string) error {
	batch, err := model.ClaimAPIRequestLogExportBatch(w.db, tag, w.owner, requestLogExportLease)
	if err != nil {
		return err
	}
	return w.buildClaimed(batch)
}

func (w *requestLogExportWorker) buildClaimed(batch *model.APIRequestLogExportBatch) (buildErr error) {
	if batch == nil || strings.TrimSpace(batch.Tag) == "" {
		return errors.New("claimed export batch is required")
	}
	temp, err := os.CreateTemp(w.dir, "."+batch.Tag+"-*.tmp")
	if err != nil {
		_, _ = model.MarkAPIRequestLogExportBatchFailed(w.db, batch.Tag, w.owner, err)
		return err
	}
	tempPath := temp.Name()
	var finalPath string
	defer func() {
		_ = temp.Close()
		if buildErr != nil {
			_ = os.Remove(tempPath)
			if finalPath != "" {
				_ = os.Remove(finalPath)
			}
			_, _ = model.MarkAPIRequestLogExportBatchFailed(w.db, batch.Tag, w.owner, buildErr)
		}
	}()

	hash := sha256.New()
	buffered := bufio.NewWriterSize(io.MultiWriter(temp, hash), 256*1024)
	var rowCount int64
	var sequence int64
	for {
		if _, err := model.RenewAPIRequestLogExportBatchLease(w.db, batch.Tag, w.owner, requestLogExportLease); err != nil {
			return err
		}
		page, err := model.GetAPIRequestLogExportBatchTurnPage(w.db, batch.Id, sequence, requestLogExportPageSize)
		if err != nil {
			return err
		}
		for _, member := range page.Items {
			if err := model.ValidateAPIRequestLogTurnForExport(member.Turn); err != nil {
				return fmt.Errorf("export member %d: %w", member.Sequence, err)
			}
			line, err := common.Marshal(trainingTurnJSONLRecord(member.Turn))
			if err != nil {
				return err
			}
			if _, err := buffered.Write(line); err != nil {
				return err
			}
			if err := buffered.WriteByte('\n'); err != nil {
				return err
			}
			rowCount++
		}
		if _, err := model.UpdateAPIRequestLogExportBatchProgress(w.db, batch.Tag, w.owner, rowCount); err != nil {
			return err
		}
		sequence = page.NextSequence
		if !page.HasMore {
			break
		}
	}
	if err := buffered.Flush(); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if _, err := model.RenewAPIRequestLogExportBatchLease(w.db, batch.Tag, w.owner, requestLogExportLease); err != nil {
		return err
	}
	finalPath = w.artifactPathForBuild(batch)
	if err := os.Rename(tempPath, finalPath); err != nil {
		return err
	}
	tempPath = ""
	if err := syncDirectory(w.dir); err != nil {
		return err
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	if err := verifyFileSHA256(finalPath, checksum); err != nil {
		return err
	}
	if _, err := model.RenewAPIRequestLogExportBatchLease(w.db, batch.Tag, w.owner, requestLogExportLease); err != nil {
		return err
	}
	if _, err := model.MarkAPIRequestLogExportBatchCompleted(w.db, batch.Tag, w.owner, finalPath, checksum, rowCount); err != nil {
		return err
	}
	finalPath = ""
	return nil
}

func (w *requestLogExportWorker) artifactPathForBuild(batch *model.APIRequestLogExportBatch) string {
	ownerHash := sha256.Sum256([]byte(w.owner))
	name := fmt.Sprintf("%s-build-%d-%s.jsonl", batch.Tag, batch.BuildAttempt, hex.EncodeToString(ownerHash[:6]))
	return filepath.Join(w.dir, name)
}

func (w *requestLogExportWorker) ArtifactPath(batch *model.APIRequestLogExportBatch) (string, error) {
	if w == nil || batch == nil {
		return "", errors.New("export worker is not initialized")
	}
	absPath, err := w.exportArtifactPath(batch)
	if err != nil {
		return "", err
	}
	if err := verifyFileSHA256(absPath, batch.SHA256); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return absPath, nil
}

func (w *requestLogExportWorker) exportArtifactPath(batch *model.APIRequestLogExportBatch) (string, error) {
	if w == nil || batch == nil {
		return "", errors.New("export worker is not initialized")
	}
	path := strings.TrimSpace(batch.ArtifactPath)
	if path == "" {
		return "", errors.New("export artifact path is empty")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(w.dir, absPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("export artifact path is outside the export directory")
	}
	return absPath, nil
}

// StageArtifactDeletion hides an artifact with an atomic rename before the
// matching database branch is removed. If the database transaction fails, the
// caller can restore it without risking an unrecoverable delete.
func (w *requestLogExportWorker) StageArtifactDeletion(batch *model.APIRequestLogExportBatch) (*stagedExportArtifactDeletion, error) {
	path, err := w.exportArtifactPath(batch)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return &stagedExportArtifactDeletion{}, nil
	} else if err != nil {
		return nil, err
	}
	stagedPath := filepath.Join(w.dir, "."+filepath.Base(path)+".deleting-"+strconv.FormatInt(time.Now().UTC().UnixNano(), 10))
	if err := os.Rename(path, stagedPath); err != nil {
		return nil, err
	}
	if err := syncDirectory(w.dir); err != nil {
		_ = os.Rename(stagedPath, path)
		return nil, err
	}
	return &stagedExportArtifactDeletion{originalPath: path, stagedPath: stagedPath}, nil
}

func (s *stagedExportArtifactDeletion) Restore() error {
	if s == nil || s.stagedPath == "" {
		return nil
	}
	if err := os.Rename(s.stagedPath, s.originalPath); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(s.originalPath))
}

func (s *stagedExportArtifactDeletion) Finalize() error {
	if s == nil || s.stagedPath == "" {
		return nil
	}
	if err := os.Remove(s.stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(s.stagedPath))
}

func newRequestLogExportWorkerOwner() (string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "unknown-host"
	}
	if len(hostname) > 120 {
		hostnameHash := sha256.Sum256([]byte(hostname))
		hostname = hostname[:96] + "-" + hex.EncodeToString(hostnameHash[:8])
	}
	return fmt.Sprintf("%s:%d:%s", hostname, os.Getpid(), hex.EncodeToString(random)), nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func trainingTurnJSONLRecord(detail *model.APIRequestLogTurnDetail) map[string]interface{} {
	if detail == nil {
		return map[string]interface{}{}
	}
	requests := make([]map[string]interface{}, 0, len(detail.Requests))
	for _, request := range detail.Requests {
		requests = append(requests, map[string]interface{}{
			"sequence":            request.Sequence,
			"request_id":          request.RequestId,
			"upstream_request_id": request.UpstreamRequestId,
			"created_at":          request.CreatedAt,
			"status_code":         request.StatusCode,
			"is_stream":           request.IsStream,
		})
	}
	items, contextItemCount, turnItemCount, deduplicatedItemCount := trainingTurnJSONLItems(detail)
	return map[string]interface{}{
		"schema_version":             model.APIRequestLogExportSchemaVersion,
		"session_id":                 detail.SessionId,
		"turn_id":                    detail.TurnId,
		"turn_index":                 detail.TurnIndex,
		"protocol":                   detail.Protocol,
		"status":                     detail.CompletionStatus,
		"completion_signal":          detail.CompletionSignal,
		"attribution":                detail.Attribution,
		"started_at":                 detail.StartedAt,
		"ended_at":                   detail.CompletedAt,
		"user_id":                    detail.UserId,
		"username":                   detail.Username,
		"token_id":                   detail.TokenId,
		"token_name":                 detail.TokenName,
		"model":                      detail.ModelName,
		"prompt_tokens":              detail.PromptTokens,
		"completion_tokens":          detail.CompletionTokens,
		"token_used":                 detail.TokenUsed,
		"quota":                      detail.Quota,
		"requests":                   requests,
		"context_loaded":             detail.ContextLoaded,
		"context_complete":           detail.ContextLoaded && detail.ContextComplete,
		"context_item_count":         contextItemCount,
		"context_omitted_item_count": detail.ContextOmittedItemCount,
		"turn_item_count":            turnItemCount,
		"deduplicated_item_count":    deduplicatedItemCount,
		"training_item_count":        len(items),
		"training_items":             items,
	}
}

type trainingTurnItem struct {
	detail          model.APIRequestLogTurnItemDetail
	contextSnapshot bool
}

func trainingTurnJSONLItems(detail *model.APIRequestLogTurnDetail) ([]map[string]interface{}, int, int, int) {
	merged := make([]trainingTurnItem, 0, len(detail.ContextItems)+len(detail.Items))
	seenSourceItemIds := make(map[int]int, len(detail.ContextItems))
	contextItemCount := 0
	for _, item := range detail.ContextItems {
		if strings.Contains(strings.ToLower(strings.TrimSpace(item.ContentType)), "encrypted") {
			continue
		}
		contextItemCount++
		if item.SourceItemId > 0 {
			seenSourceItemIds[item.SourceItemId] = len(merged)
		}
		merged = append(merged, trainingTurnItem{detail: item, contextSnapshot: true})
	}

	turnItemCount := 0
	deduplicatedItemCount := 0
	for _, item := range detail.Items {
		if strings.Contains(strings.ToLower(strings.TrimSpace(item.ContentType)), "encrypted") {
			continue
		}
		turnItemCount++
		if index, exists := seenSourceItemIds[item.SourceItemId]; item.SourceItemId > 0 && exists {
			merged[index].detail = mergeTrainingTurnItemMapping(merged[index].detail, item)
			deduplicatedItemCount++
			continue
		}
		if item.SourceItemId > 0 {
			seenSourceItemIds[item.SourceItemId] = len(merged)
		}
		merged = append(merged, trainingTurnItem{detail: item})
	}

	items := make([]map[string]interface{}, 0, len(merged))
	for index, item := range merged {
		record := map[string]interface{}{
			"seq":              index + 1,
			"phase":            item.detail.Phase,
			"type":             item.detail.ItemType,
			"role":             item.detail.Role,
			"content_type":     item.detail.ContentType,
			"content":          string(item.detail.Content),
			"tool_call_id":     item.detail.ToolCallId,
			"name":             item.detail.Name,
			"source":           item.detail.Source,
			"source_item_id":   item.detail.SourceItemId,
			"source_seq":       item.detail.SourceSeq,
			"context_snapshot": item.contextSnapshot,
			"provider_item_id": item.detail.ProviderItemId,
			"message_phase":    item.detail.MessagePhase,
			"status":           item.detail.ItemStatus,
		}
		if item.detail.Ordinal > 0 {
			record["turn_ordinal"] = item.detail.Ordinal
		}
		items = append(items, record)
	}
	return items, contextItemCount, turnItemCount, deduplicatedItemCount
}

func mergeTrainingTurnItemMapping(contextItem, turnItem model.APIRequestLogTurnItemDetail) model.APIRequestLogTurnItemDetail {
	contextItem.Id = turnItem.Id
	contextItem.TurnRecordId = turnItem.TurnRecordId
	contextItem.RequestRecordId = turnItem.RequestRecordId
	contextItem.Ordinal = turnItem.Ordinal
	contextItem.CanonicalKey = turnItem.CanonicalKey
	contextItem.ProviderItemId = turnItem.ProviderItemId
	contextItem.MessagePhase = turnItem.MessagePhase
	contextItem.ItemStatus = turnItem.ItemStatus
	return contextItem
}

func verifyFileSHA256(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, strings.TrimSpace(expected)) {
		return fmt.Errorf("artifact checksum mismatch: got %s", actual)
	}
	return nil
}
