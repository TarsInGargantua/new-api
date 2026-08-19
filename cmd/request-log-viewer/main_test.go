package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupRequestLogViewerTest(t *testing.T) (*requestLogViewerServer, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "viewer.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.APIRequestLog{}, &model.APIRequestLogItem{}); err != nil {
		t.Fatal(err)
	}
	if err := model.EnsureAPIRequestLogMaterializedTables(db); err != nil {
		t.Fatal(err)
	}
	worker, err := newRequestLogExportWorker(db, filepath.Join(t.TempDir(), "exports"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(worker.Close)
	return &requestLogViewerServer{db: db, exports: worker}, db
}

func seedRequestLogViewerTurn(t *testing.T, db *gorm.DB) *model.APIRequestLogTurn {
	t.Helper()
	log := &model.APIRequestLog{
		Source: model.APIRequestLogSourceLive, UserId: 1, Username: "alice", TokenId: 2, TokenName: "prod",
		ModelName: "gpt-turn", CreatedAt: 100, RequestId: "req-1", StatusCode: 200,
		PromptTokens: 10, CompletionTokens: 5, TokenUsed: 15, Quota: 20,
	}
	if err := db.Create(log).Error; err != nil {
		t.Fatal(err)
	}
	items := []model.APIRequestLogItem{
		{LogId: log.Id, Seq: 1, Phase: model.APIRequestLogPhaseInput, ItemType: model.APIRequestLogItemMessage, Role: "system", ContentType: "text", Content: "instructions"},
		{LogId: log.Id, Seq: 2, Phase: model.APIRequestLogPhaseInput, ItemType: model.APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "hello"},
		{LogId: log.Id, Seq: 3, Phase: model.APIRequestLogPhaseOutput, ItemType: model.APIRequestLogItemReasoning, Role: "assistant", ContentType: "encrypted", Content: "ciphertext"},
		{LogId: log.Id, Seq: 4, Phase: model.APIRequestLogPhaseOutput, ItemType: model.APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "done"},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	turn, err := model.MaterializeAPIRequestLogTurn(db, log, model.APIRequestLogTurnMeta{
		SessionId: "session-1", TurnId: "turn-1", Protocol: "responses", StartedAt: 100, CompletedAt: 110,
		CompletionStatus: model.APIRequestLogTurnStatusCompleted, CompletionSignal: "responses.message.final.completed", Attribution: model.APIRequestLogTurnAttributionExact,
		Items: []model.APIRequestLogTurnItemMeta{{Seq: 4, ProviderItemId: "msg-1", TurnId: "turn-1", MessagePhase: "final", ItemStatus: "completed"}},
	}, items)
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func TestRequestLogViewerIndexUsesEnglishExportLabels(t *testing.T) {
	recorder := httptest.NewRecorder()
	serveIndex(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected index status: %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{"Session Log Viewer", "Session data", "Export history", "Audit", "Mark cleaned", "Reset export state", "Delete history", "Reset this export?", "Collapsed · click to expand", "No export selection"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing English viewer label %q", expected)
		}
	}
	if strings.Contains(body, "<h2>Export batches</h2>") || strings.Contains(body, "Data operations") {
		t.Fatal("found redundant export page heading")
	}
	if strings.Contains(body, `<article class="item"`) || !strings.Contains(body, "state-expanded") {
		t.Fatal("detail cards are not uniformly collapsible")
	}
	for _, unexpected := range []string{"轮次日志查看器", "导出批次", "历史导出", "完整性复核", "标记已清洗", "重置为未导出", "删除历史记录"} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("found legacy Chinese viewer label %q", unexpected)
		}
	}
	for _, unexpected := range []string{"Turn Log Viewer", "Session / turn", "/api/turns"} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("found exposed turn-level viewer concept %q", unexpected)
		}
	}
}

func TestRequestLogViewerRequiresExplicitExportSelection(t *testing.T) {
	server, _ := setupRequestLogViewerTest(t)

	previewRecorder := httptest.NewRecorder()
	server.serveExportPreview(previewRecorder, httptest.NewRequest(http.MethodGet, "/api/export-preview", nil))
	if previewRecorder.Code != http.StatusOK || !strings.Contains(previewRecorder.Body.String(), `"available_count":0`) {
		t.Fatalf("empty export preview status=%d body=%s", previewRecorder.Code, previewRecorder.Body.String())
	}

	createRecorder := httptest.NewRecorder()
	server.serveExportBatches(createRecorder, httptest.NewRequest(http.MethodPost, "/api/export-batches", nil))
	if createRecorder.Code != http.StatusBadRequest || !strings.Contains(createRecorder.Body.String(), "select at least one session filter") {
		t.Fatalf("empty export create status=%d body=%s", createRecorder.Code, createRecorder.Body.String())
	}
}

func TestRequestLogViewerSessionRoutesAndPersistentExport(t *testing.T) {
	server, db := setupRequestLogViewerTest(t)
	turn := seedRequestLogViewerTurn(t, db)

	listRecorder := httptest.NewRecorder()
	server.serveSessions(listRecorder, httptest.NewRequest(http.MethodGet, "/api/sessions?session_id=session-1", nil))
	if listRecorder.Code != http.StatusOK || !strings.Contains(listRecorder.Body.String(), `"session_id":"session-1"`) || strings.Contains(listRecorder.Body.String(), `"turn_id"`) {
		t.Fatalf("unexpected session list: status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	detailRecorder := httptest.NewRecorder()
	server.serveSessionDetail(detailRecorder, httptest.NewRequest(http.MethodGet, "/api/sessions/"+fmt.Sprint(turn.Id), nil))
	if detailRecorder.Code != http.StatusOK || strings.Contains(detailRecorder.Body.String(), "ciphertext") || strings.Contains(detailRecorder.Body.String(), `"turn_id"`) {
		t.Fatalf("unexpected session detail: status=%d body=%s", detailRecorder.Code, detailRecorder.Body.String())
	}

	createRecorder := httptest.NewRecorder()
	server.serveExportBatches(createRecorder, httptest.NewRequest(http.MethodPost, "/api/export-batches?start_timestamp=100&end_timestamp=120", nil))
	if createRecorder.Code != http.StatusAccepted {
		t.Fatalf("create export status=%d body=%s", createRecorder.Code, createRecorder.Body.String())
	}
	var response struct {
		Data model.APIRequestLogExportBatch `json:"data"`
	}
	if err := common.Unmarshal(createRecorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	batch := waitForCompletedExport(t, db, response.Data.Tag)
	if batch.RowCount != 1 || len(batch.SHA256) != 64 {
		t.Fatalf("unexpected completed batch: %+v", batch)
	}

	first := downloadExport(t, server, batch.Tag)
	second := downloadExport(t, server, batch.Tag)
	if !bytes.Equal(first, second) {
		t.Fatal("repeated batch downloads differ")
	}
	if !bytes.Contains(first, []byte(`"session_id":"session-1"`)) || bytes.Contains(first, []byte("ciphertext")) {
		t.Fatalf("unexpected JSONL artifact: %s", first)
	}
	if err := os.WriteFile(batch.ArtifactPath, append(first, []byte("tampered")...), 0o640); err != nil {
		t.Fatal(err)
	}
	tamperedRecorder := httptest.NewRecorder()
	server.serveExportBatchAction(tamperedRecorder, httptest.NewRequest(http.MethodGet, "/api/export-batches/"+batch.Tag+"/download", nil))
	if tamperedRecorder.Code != http.StatusInternalServerError || !strings.Contains(tamperedRecorder.Body.String(), "checksum mismatch") {
		t.Fatalf("tampered download status=%d body=%s", tamperedRecorder.Code, tamperedRecorder.Body.String())
	}

	secondBatchRecorder := httptest.NewRecorder()
	server.serveExportBatches(secondBatchRecorder, httptest.NewRequest(http.MethodPost, "/api/export-batches?start_timestamp=100&end_timestamp=120", nil))
	if secondBatchRecorder.Code != http.StatusAccepted {
		t.Fatalf("second export status=%d body=%s", secondBatchRecorder.Code, secondBatchRecorder.Body.String())
	}
	var secondResponse struct {
		Data model.APIRequestLogExportBatch `json:"data"`
	}
	if err := common.Unmarshal(secondBatchRecorder.Body.Bytes(), &secondResponse); err != nil {
		t.Fatal(err)
	}
	if secondResponse.Data.RowCount != 0 {
		t.Fatalf("expected no duplicate members, got %+v", secondResponse.Data)
	}
	waitForCompletedExport(t, db, secondResponse.Data.Tag)
}

func TestRequestLogViewerLaterSessionContentCannotMutateExportedArtifact(t *testing.T) {
	server, db := setupRequestLogViewerTest(t)
	seedRequestLogViewerTurn(t, db)
	firstBatch, err := model.CreateAPIRequestLogExportBatch(db, model.APIRequestLogTurnQueryParams{SessionId: "session-1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	server.exports.Enqueue(firstBatch.Tag)
	firstCompleted := waitForCompletedExport(t, db, firstBatch.Tag)
	firstArtifact := downloadExport(t, server, firstCompleted.Tag)

	log := &model.APIRequestLog{
		Source: model.APIRequestLogSourceLive, UserId: 1, Username: "alice", TokenId: 2, TokenName: "prod",
		ModelName: "gpt-turn", CreatedAt: 200, RequestId: "req-later", StatusCode: http.StatusOK,
		ParseStatus: model.APIRequestLogParseOK, ItemsStatus: model.APIRequestLogItemsOK,
	}
	if err := db.Create(log).Error; err != nil {
		t.Fatal(err)
	}
	items := []model.APIRequestLogItem{
		{LogId: log.Id, Seq: 1, Phase: model.APIRequestLogPhaseInput, ItemType: model.APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "later question"},
		{LogId: log.Id, Seq: 2, Phase: model.APIRequestLogPhaseOutput, ItemType: model.APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "later answer"},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	later, err := model.MaterializeAPIRequestLogTurn(db, log, model.APIRequestLogTurnMeta{
		SessionId: "session-1", TurnId: "internal-record-2", Protocol: "responses", StartedAt: 200, CompletedAt: 210,
		CompletionStatus: model.APIRequestLogTurnStatusCompleted, Attribution: model.APIRequestLogTurnAttributionExact,
		Items: []model.APIRequestLogTurnItemMeta{{Seq: 2, ProviderItemId: "later-message", MessagePhase: "final", ItemStatus: "completed"}},
	}, items)
	if err != nil {
		t.Fatal(err)
	}

	sessions, total, err := model.GetAPIRequestLogSessions(db, model.APIRequestLogTurnQueryParams{SessionId: "session-1", Num: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(sessions) != 2 {
		t.Fatalf("expected frozen and unexported session branches, total=%d sessions=%+v", total, sessions)
	}
	exportedCount := 0
	unexportedAnchor := int64(0)
	for _, session := range sessions {
		if session.Exported {
			exportedCount++
		} else {
			unexportedAnchor = session.Id
		}
	}
	if exportedCount != 1 || unexportedAnchor != later.Id {
		t.Fatalf("unexpected branch state: exported=%d unexported_anchor=%d later=%d", exportedCount, unexportedAnchor, later.Id)
	}
	if current := downloadExport(t, server, firstCompleted.Tag); !bytes.Equal(current, firstArtifact) || bytes.Contains(current, []byte("later question")) {
		t.Fatalf("first session snapshot changed after later content: %s", current)
	}

	secondBatch, err := model.CreateAPIRequestLogExportBatch(db, model.APIRequestLogTurnQueryParams{SessionId: "session-1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if secondBatch.RowCount != 1 {
		t.Fatalf("expected one later session branch, got %d", secondBatch.RowCount)
	}
	server.exports.Enqueue(secondBatch.Tag)
	secondCompleted := waitForCompletedExport(t, db, secondBatch.Tag)
	secondArtifact := downloadExport(t, server, secondCompleted.Tag)
	if !bytes.Contains(secondArtifact, []byte("later question")) || bytes.Count(secondArtifact, []byte("\n")) != 1 {
		t.Fatalf("unexpected second session artifact: %s", secondArtifact)
	}
	if current := downloadExport(t, server, firstCompleted.Tag); !bytes.Equal(current, firstArtifact) {
		t.Fatal("first artifact changed after exporting the later branch")
	}
}

func TestRequestLogExportReconstructsFirstRequestContextWithoutDuplicatingTurnItems(t *testing.T) {
	server, db := setupRequestLogViewerTest(t)

	createRequest := func(createdAt int64, requestId string, items []model.APIRequestLogItem) (*model.APIRequestLog, []model.APIRequestLogItem) {
		log := &model.APIRequestLog{
			Source: model.APIRequestLogSourceLive, UserId: 1, Username: "alice", TokenId: 2, TokenName: "prod",
			ModelName: "gpt-context", CreatedAt: createdAt, RequestId: requestId, StatusCode: http.StatusOK,
			ParseStatus: model.APIRequestLogParseOK, ItemsStatus: model.APIRequestLogItemsOK,
		}
		if err := db.Create(log).Error; err != nil {
			t.Fatal(err)
		}
		for index := range items {
			items[index].LogId = log.Id
		}
		if err := db.Create(&items).Error; err != nil {
			t.Fatal(err)
		}
		return log, items
	}

	firstLog, firstItems := createRequest(100, "context-request-1", []model.APIRequestLogItem{
		{Seq: 1, Phase: model.APIRequestLogPhaseInput, ItemType: model.APIRequestLogItemMessage, Role: "developer", ContentType: "text", Content: "shared instructions"},
		{Seq: 2, Phase: model.APIRequestLogPhaseInput, ItemType: model.APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "first question"},
		{Seq: 3, Phase: model.APIRequestLogPhaseOutput, ItemType: model.APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "first answer"},
	})
	firstTurn, err := model.MaterializeAPIRequestLogTurn(db, firstLog, model.APIRequestLogTurnMeta{
		SessionId: "context-session", TurnId: "context-turn-1", Protocol: "responses", StartedAt: 100, CompletedAt: 105,
		CompletionStatus: model.APIRequestLogTurnStatusCompleted, CompletionSignal: "responses.message.final.completed", Attribution: model.APIRequestLogTurnAttributionExact,
		Items: []model.APIRequestLogTurnItemMeta{{Seq: 3, ProviderItemId: "answer-1", MessagePhase: "final", ItemStatus: "completed"}},
	}, firstItems)
	if err != nil {
		t.Fatal(err)
	}

	secondLog, secondItems := createRequest(110, "context-request-2", []model.APIRequestLogItem{
		{Seq: 1, Phase: model.APIRequestLogPhaseInput, ItemType: model.APIRequestLogItemMessage, Role: "developer", ContentType: "text", Content: "shared instructions"},
		{Seq: 2, Phase: model.APIRequestLogPhaseInput, ItemType: model.APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "first question"},
		{Seq: 3, Phase: model.APIRequestLogPhaseInput, ItemType: model.APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "first answer"},
		{Seq: 4, Phase: model.APIRequestLogPhaseInput, ItemType: model.APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "second question"},
		{Seq: 5, Phase: model.APIRequestLogPhaseOutput, ItemType: model.APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "second answer"},
	})
	secondTurn, err := model.MaterializeAPIRequestLogTurn(db, secondLog, model.APIRequestLogTurnMeta{
		SessionId: "context-session", TurnId: "context-turn-2", Protocol: "responses", StartedAt: 110, CompletedAt: 115,
		CompletionStatus: model.APIRequestLogTurnStatusCompleted, CompletionSignal: "responses.message.final.completed", Attribution: model.APIRequestLogTurnAttributionExact,
		Items: []model.APIRequestLogTurnItemMeta{
			{Seq: 3, ProviderItemId: "answer-1"},
			{Seq: 5, ProviderItemId: "answer-2", MessagePhase: "final", ItemStatus: "completed"},
		},
	}, secondItems)
	if err != nil {
		t.Fatal(err)
	}
	if firstTurn.TurnIndex != 1 || secondTurn.TurnIndex != 2 {
		t.Fatalf("unexpected turn order: first=%d second=%d", firstTurn.TurnIndex, secondTurn.TurnIndex)
	}

	detail, err := model.GetAPIRequestLogTurnById(db, secondTurn.Id)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.ContextItems) != 0 {
		t.Fatalf("regular viewer detail should not eagerly load export context, got %d items", len(detail.ContextItems))
	}
	deltaContent := make([]string, 0, len(detail.Items))
	for _, item := range detail.Items {
		deltaContent = append(deltaContent, string(item.Content))
	}
	if strings.Contains(strings.Join(deltaContent, "\n"), "first question") || strings.Contains(strings.Join(deltaContent, "\n"), "first answer") {
		t.Fatalf("materialized turn should stay incremental, got %v", deltaContent)
	}

	batch, err := model.CreateAPIRequestLogExportBatch(db, model.APIRequestLogTurnQueryParams{SessionId: "context-session"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if batch.RowCount != 1 {
		t.Fatalf("expected one exported session, got %d", batch.RowCount)
	}
	completed := waitForCompletedExport(t, db, batch.Tag)
	lines := strings.Split(strings.TrimSpace(string(downloadExport(t, server, completed.Tag))), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one JSONL row per session, got %d", len(lines))
	}
	type exportedItem struct {
		Seq             int    `json:"seq"`
		Content         string `json:"content"`
		SourceItemId    int    `json:"source_item_id"`
		ContextSnapshot bool   `json:"context_snapshot"`
	}
	type exportedRecord struct {
		SchemaVersion     int            `json:"schema_version"`
		SessionId         string         `json:"session_id"`
		Requests          []interface{}  `json:"requests"`
		ContextLoaded     bool           `json:"context_loaded"`
		ContextComplete   bool           `json:"context_complete"`
		ContextItemCount  int            `json:"context_item_count"`
		TrainingItemCount int            `json:"training_item_count"`
		TrainingItems     []exportedItem `json:"training_items"`
	}
	var record exportedRecord
	if err := common.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	if record.SchemaVersion != model.APIRequestLogExportSchemaVersion || record.SessionId != "context-session" || len(record.Requests) != 2 {
		t.Fatalf("unexpected session export identity: %+v", record)
	}
	if !record.ContextLoaded || !record.ContextComplete || record.ContextItemCount != 4 || record.TrainingItemCount != 5 {
		t.Fatalf("unexpected context audit metadata: %+v", record)
	}
	if strings.Contains(lines[0], `"turn_id"`) || strings.Contains(lines[0], `"turn_index"`) || strings.Contains(lines[0], `"turn_item_count"`) {
		t.Fatalf("session schema leaked turn-level fields: %s", lines[0])
	}
	expectedContent := []string{"shared instructions", "first question", "first answer", "second question", "second answer"}
	seenSourceItems := make(map[int]bool, len(record.TrainingItems))
	currentQuestionCount := 0
	for index, item := range record.TrainingItems {
		if item.Seq != index+1 || item.Content != expectedContent[index] {
			t.Fatalf("unexpected training item %d: %+v", index, item)
		}
		if seenSourceItems[item.SourceItemId] {
			t.Fatalf("duplicated source item %d in export", item.SourceItemId)
		}
		seenSourceItems[item.SourceItemId] = true
		if item.SourceItemId == secondItems[3].Id {
			currentQuestionCount++
		}
		if index < 4 && !item.ContextSnapshot {
			t.Fatalf("expected item %d to come from the first-request context snapshot", index)
		}
		if index == 4 && item.ContextSnapshot {
			t.Fatal("current turn output must follow the context snapshot")
		}
	}
	if currentQuestionCount != 1 {
		t.Fatalf("current user item should appear exactly once, got %d", currentQuestionCount)
	}
}

func TestRequestLogExportWorkerPollsDatabaseWithoutEnqueue(t *testing.T) {
	server, db := setupRequestLogViewerTest(t)
	seedRequestLogViewerTurn(t, db)
	batch, err := model.CreateAPIRequestLogExportBatch(db, model.APIRequestLogTurnQueryParams{ModelName: "gpt-turn"}, false)
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForCompletedExport(t, db, batch.Tag)
	artifact, err := server.exports.ArtifactPath(completed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatal(err)
	}
}

func TestRequestLogViewerAuditsCleansAndDeletesExportBatch(t *testing.T) {
	server, db := setupRequestLogViewerTest(t)
	seedRequestLogViewerTurn(t, db)
	batch, err := model.CreateAPIRequestLogExportBatch(db, model.APIRequestLogTurnQueryParams{ModelName: "gpt-turn"}, false)
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForCompletedExport(t, db, batch.Tag)
	if err := db.Model(&model.APIRequestLogExportBatch{}).Where("id = ?", completed.Id).Update("integrity_status", model.APIRequestLogExportIntegrityPending).Error; err != nil {
		t.Fatal(err)
	}

	auditRecorder := httptest.NewRecorder()
	server.serveExportBatchAction(auditRecorder, httptest.NewRequest(http.MethodPost, "/api/export-batches/"+batch.Tag+"/audit", nil))
	if auditRecorder.Code != http.StatusOK || !strings.Contains(auditRecorder.Body.String(), `"integrity_status":"verified"`) {
		t.Fatalf("unexpected audit response: status=%d body=%s", auditRecorder.Code, auditRecorder.Body.String())
	}
	cleanRecorder := httptest.NewRecorder()
	server.serveExportBatchAction(cleanRecorder, httptest.NewRequest(http.MethodPost, "/api/export-batches/"+batch.Tag+"/mark-cleaned", nil))
	if cleanRecorder.Code != http.StatusOK || !strings.Contains(cleanRecorder.Body.String(), `"cleaned_at":`) {
		t.Fatalf("unexpected clean response: status=%d body=%s", cleanRecorder.Code, cleanRecorder.Body.String())
	}
	deleteRecorder := httptest.NewRecorder()
	server.serveExportBatchAction(deleteRecorder, httptest.NewRequest(http.MethodPost, "/api/export-batches/"+batch.Tag+"/delete", nil))
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected delete response: status=%d body=%s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	if _, err := os.Stat(completed.ArtifactPath); !os.IsNotExist(err) {
		t.Fatalf("artifact should be deleted, stat err=%v", err)
	}
	if _, err := model.GetAPIRequestLogExportBatchByTag(db, batch.Tag); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("batch should be deleted, err=%v", err)
	}
}

func TestRequestLogViewerDeletesHistoricalRecordWithoutCleanedMarker(t *testing.T) {
	server, db := setupRequestLogViewerTest(t)
	turn := seedRequestLogViewerTurn(t, db)
	batch, err := model.CreateAPIRequestLogExportBatch(db, model.APIRequestLogTurnQueryParams{SessionId: turn.SessionId}, false)
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForCompletedExport(t, db, batch.Tag)

	deleteRecorder := httptest.NewRecorder()
	server.serveExportBatchAction(deleteRecorder, httptest.NewRequest(http.MethodPost, "/api/export-batches/"+batch.Tag+"/delete-history", nil))
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected historical delete response: status=%d body=%s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	if _, err := os.Stat(completed.ArtifactPath); !os.IsNotExist(err) {
		t.Fatalf("historical delete should remove artifact, stat err=%v", err)
	}
	if _, err := model.GetAPIRequestLogExportBatchByTag(db, batch.Tag); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("historical batch should be deleted, err=%v", err)
	}
	var storedTurn model.APIRequestLogTurn
	if err := db.First(&storedTurn, turn.Id).Error; err != nil {
		t.Fatal(err)
	}
	if storedTurn.ExportedVersion <= 0 {
		t.Fatalf("historical delete must not change turn export state, got %d", storedTurn.ExportedVersion)
	}
}

func TestRequestLogViewerResetsCompletedExportBatch(t *testing.T) {
	server, db := setupRequestLogViewerTest(t)
	turn := seedRequestLogViewerTurn(t, db)
	batch, err := model.CreateAPIRequestLogExportBatch(db, model.APIRequestLogTurnQueryParams{SessionId: turn.SessionId}, false)
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForCompletedExport(t, db, batch.Tag)
	artifactPath := completed.ArtifactPath

	resetRecorder := httptest.NewRecorder()
	server.serveExportBatchAction(resetRecorder, httptest.NewRequest(http.MethodPost, "/api/export-batches/"+batch.Tag+"/reset", nil))
	if resetRecorder.Code != http.StatusOK || !strings.Contains(resetRecorder.Body.String(), `"reset_at":`) || !strings.Contains(resetRecorder.Body.String(), `"artifact_deleted_at":`) {
		t.Fatalf("unexpected reset response: status=%d body=%s", resetRecorder.Code, resetRecorder.Body.String())
	}
	if _, err := os.Stat(artifactPath); !os.IsNotExist(err) {
		t.Fatalf("reset should delete the JSONL artifact, stat err=%v", err)
	}
	downloadRecorder := httptest.NewRecorder()
	server.serveExportBatchAction(downloadRecorder, httptest.NewRequest(http.MethodGet, "/api/export-batches/"+batch.Tag+"/download", nil))
	if downloadRecorder.Code != http.StatusGone {
		t.Fatalf("reset artifact download should be gone, got status=%d body=%s", downloadRecorder.Code, downloadRecorder.Body.String())
	}
	var storedTurn model.APIRequestLogTurn
	if err := db.First(&storedTurn, turn.Id).Error; err != nil {
		t.Fatal(err)
	}
	if storedTurn.ExportedVersion != 0 {
		t.Fatalf("turn should be released for a future export, got exported version %d", storedTurn.ExportedVersion)
	}
	var members int64
	if err := db.Model(&model.APIRequestLogExportMember{}).Where("batch_id = ?", completed.Id).Count(&members).Error; err != nil {
		t.Fatal(err)
	}
	if members != 0 {
		t.Fatalf("expected reset batch members to be removed, got %d", members)
	}

	secondReset := httptest.NewRecorder()
	server.serveExportBatchAction(secondReset, httptest.NewRequest(http.MethodPost, "/api/export-batches/"+batch.Tag+"/reset", nil))
	if secondReset.Code != http.StatusConflict {
		t.Fatalf("expected second reset conflict, got status=%d body=%s", secondReset.Code, secondReset.Body.String())
	}
}

func TestRequestLogViewerDeletesArtifactForPreviouslyResetBatch(t *testing.T) {
	server, db := setupRequestLogViewerTest(t)
	turn := seedRequestLogViewerTurn(t, db)
	batch, err := model.CreateAPIRequestLogExportBatch(db, model.APIRequestLogTurnQueryParams{SessionId: turn.SessionId}, false)
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForCompletedExport(t, db, batch.Tag)
	artifactPath := completed.ArtifactPath
	if err := db.Model(&model.APIRequestLogExportBatch{}).Where("id = ?", completed.Id).Updates(map[string]interface{}{
		"reset_at":   time.Now().UTC().Unix(),
		"reset_rows": completed.RowCount,
	}).Error; err != nil {
		t.Fatal(err)
	}

	deleteArtifactRecorder := httptest.NewRecorder()
	server.serveExportBatchAction(deleteArtifactRecorder, httptest.NewRequest(http.MethodPost, "/api/export-batches/"+batch.Tag+"/delete-artifact", nil))
	if deleteArtifactRecorder.Code != http.StatusOK || !strings.Contains(deleteArtifactRecorder.Body.String(), `"artifact_deleted_at":`) {
		t.Fatalf("unexpected delete artifact response: status=%d body=%s", deleteArtifactRecorder.Code, deleteArtifactRecorder.Body.String())
	}
	if _, err := os.Stat(artifactPath); !os.IsNotExist(err) {
		t.Fatalf("legacy reset artifact should be deleted, stat err=%v", err)
	}
}

func TestRequestLogExportWorkerEnqueueIsNonBlockingAndCoalesced(t *testing.T) {
	worker := &requestLogExportWorker{wake: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		for index := 0; index < 1000; index++ {
			worker.Enqueue("batch")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("enqueue blocked on a full wake channel")
	}
	if got := len(worker.wake); got != 1 {
		t.Fatalf("expected one coalesced wake signal, got %d", got)
	}
}

func TestRequestLogExportWorkersClaimBatchOnceAcrossInstances(t *testing.T) {
	server, db := setupRequestLogViewerTest(t)
	seedRequestLogViewerTurn(t, db)
	secondWorker, err := newRequestLogExportWorker(db, server.exports.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secondWorker.Close)
	batch, err := model.CreateAPIRequestLogExportBatch(db, model.APIRequestLogTurnQueryParams{ModelName: "gpt-turn"}, false)
	if err != nil {
		t.Fatal(err)
	}
	server.exports.Enqueue(batch.Tag)
	secondWorker.Enqueue(batch.Tag)
	completed := waitForCompletedExport(t, db, batch.Tag)
	if completed.BuildAttempt != 1 {
		t.Fatalf("expected one successful lease claim, got %d attempts", completed.BuildAttempt)
	}
	artifacts, err := filepath.Glob(filepath.Join(server.exports.dir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected one immutable artifact, got %v", artifacts)
	}
}

func TestLegacyRequestExportReturnsGone(t *testing.T) {
	recorder := httptest.NewRecorder()
	serveJSONL(recorder, httptest.NewRequest(http.MethodGet, "/api/export.jsonl", nil))
	if recorder.Code != http.StatusGone {
		t.Fatalf("legacy export status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestLegacyRequestViewsReturnGone(t *testing.T) {
	for _, path := range []string{"/api/logs", "/api/logs/123"} {
		recorder := httptest.NewRecorder()
		serveLegacyRequestLogs(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusGone {
			t.Fatalf("legacy request view %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestViewerStatusDoesNotReadRawRequestTables(t *testing.T) {
	_, db := setupRequestLogViewerTest(t)
	if err := db.Create(&model.APIRequestLogTurn{
		OwnerFingerprint: strings.Repeat("a", 64), SessionId: "status-session", TurnId: "status-turn",
		TurnIndex: 1, CompletionStatus: model.APIRequestLogTurnStatusUnknown, Attribution: model.APIRequestLogTurnAttributionUnknown,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&model.APIRequestLogItem{}, &model.APIRequestLog{}); err != nil {
		t.Fatal(err)
	}
	previous := model.REQUEST_LOG_DB
	model.REQUEST_LOG_DB = db
	t.Cleanup(func() { model.REQUEST_LOG_DB = previous })

	recorder := httptest.NewRecorder()
	serveStatus(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"session_count":1`) || strings.Contains(recorder.Body.String(), `"turn_count"`) {
		t.Fatalf("unexpected viewer status: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
func waitForCompletedExport(t *testing.T, db *gorm.DB, tag string) *model.APIRequestLogExportBatch {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		batch, err := model.GetAPIRequestLogExportBatchByTag(db, tag)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Status == model.APIRequestLogExportBatchStatusCompleted {
			return batch
		}
		if batch.Status == model.APIRequestLogExportBatchStatusFailed {
			t.Fatalf("export failed: %s", batch.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("export %s did not complete", tag)
	return nil
}

func downloadExport(t *testing.T, server *requestLogViewerServer, tag string) []byte {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.serveExportBatchAction(recorder, httptest.NewRequest(http.MethodGet, "/api/export-batches/"+tag+"/download", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("download status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	return append([]byte(nil), recorder.Body.Bytes()...)
}
