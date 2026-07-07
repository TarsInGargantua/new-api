package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/glebarez/sqlite"
	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type legacyAPIRequestLog struct {
	Id                    int    `gorm:"column:id"`
	UsageLogId            int    `gorm:"column:usage_log_id"`
	UserId                int    `gorm:"column:user_id"`
	Username              string `gorm:"column:username"`
	TokenId               int    `gorm:"column:token_id"`
	TokenName             string `gorm:"column:token_name"`
	ModelName             string `gorm:"column:model_name"`
	CreatedAt             int64  `gorm:"column:created_at"`
	RequestId             string `gorm:"column:request_id"`
	UpstreamRequestId     string `gorm:"column:upstream_request_id"`
	Method                string `gorm:"column:method"`
	RequestPath           string `gorm:"column:request_path"`
	StatusCode            int    `gorm:"column:status_code"`
	IsStream              bool   `gorm:"column:is_stream"`
	ChannelId             int    `gorm:"column:channel_id"`
	Group                 string `gorm:"column:group"`
	RequestContentType    string `gorm:"column:request_content_type"`
	ResponseContentType   string `gorm:"column:response_content_type"`
	RequestSize           int64  `gorm:"column:request_size"`
	ResponseSize          int64  `gorm:"column:response_size"`
	RequestOmittedReason  string `gorm:"column:request_omitted_reason"`
	ResponseOmittedReason string `gorm:"column:response_omitted_reason"`
	Redacted              bool   `gorm:"column:redacted"`
	RequestBody           string `gorm:"column:request_body"`
	ResponseBody          string `gorm:"column:response_body"`
	Metadata              string `gorm:"column:metadata"`
}

func (legacyAPIRequestLog) TableName() string {
	return "api_request_logs"
}

type legacyUsageLog struct {
	Id                int    `gorm:"column:id"`
	UserId            int    `gorm:"column:user_id"`
	CreatedAt         int64  `gorm:"column:created_at"`
	Username          string `gorm:"column:username"`
	TokenName         string `gorm:"column:token_name"`
	ModelName         string `gorm:"column:model_name"`
	Quota             int    `gorm:"column:quota"`
	PromptTokens      int    `gorm:"column:prompt_tokens"`
	CompletionTokens  int    `gorm:"column:completion_tokens"`
	UseTime           int    `gorm:"column:use_time"`
	IsStream          bool   `gorm:"column:is_stream"`
	ChannelId         int    `gorm:"column:channel"`
	TokenId           int    `gorm:"column:token_id"`
	Group             string `gorm:"column:group"`
	RequestId         string `gorm:"column:request_id"`
	UpstreamRequestId string `gorm:"column:upstream_request_id"`
}

func (legacyUsageLog) TableName() string {
	return "logs"
}

func main() {
	sourceDSN := flag.String("source-dsn", firstNonEmpty(os.Getenv("SOURCE_REQUEST_LOG_SQL_DSN"), os.Getenv("SOURCE_SQL_DSN")), "legacy api_request_logs database DSN")
	targetDSN := flag.String("target-dsn", os.Getenv("REQUEST_LOG_SQL_DSN"), "normalized request log database DSN")
	batchSize := flag.Int("batch-size", 500, "number of rows per batch")
	maxRows := flag.Int("max-rows", 0, "maximum number of legacy rows to process, 0 means all rows")
	afterId := flag.Int("after-id", 0, "only process legacy rows with id greater than this value")
	countOnly := flag.Bool("count-only", false, "only count source legacy rows without parsing or writing")
	dryRun := flag.Bool("dry-run", false, "parse and count rows without writing target database")
	initOnly := flag.Bool("init-only", false, "only initialize normalized target tables")
	flag.Parse()

	if strings.TrimSpace(*sourceDSN) == "" && !*initOnly {
		log.Fatal("source DSN is required: set SOURCE_REQUEST_LOG_SQL_DSN or pass -source-dsn")
	}
	if strings.TrimSpace(*targetDSN) == "" && (!*dryRun || *initOnly) && !*countOnly {
		log.Fatal("target DSN is required: set REQUEST_LOG_SQL_DSN or pass -target-dsn")
	}
	if *batchSize <= 0 {
		log.Fatal("batch-size must be greater than 0")
	}
	if *maxRows < 0 {
		log.Fatal("max-rows must be greater than or equal to 0")
	}
	if *afterId < 0 {
		log.Fatal("after-id must be greater than or equal to 0")
	}
	if *countOnly && *initOnly {
		log.Fatal("count-only and init-only cannot be used together")
	}

	if !*dryRun && !*countOnly {
		targetDB, err := openDB(*targetDSN)
		if err != nil {
			log.Fatalf("open target database: %v", err)
		}
		model.REQUEST_LOG_DB = targetDB
		if err := model.EnsureAPIRequestLogTable(); err != nil {
			log.Fatalf("ensure target tables: %v", err)
		}
	}
	if *initOnly {
		log.Print("target tables initialized")
		return
	}

	sourceDB, err := openDB(*sourceDSN)
	if err != nil {
		log.Fatalf("open source database: %v", err)
	}
	if *countOnly {
		var total int64
		if err := sourceDB.Model(&legacyAPIRequestLog{}).Where("id > ?", *afterId).Count(&total).Error; err != nil {
			log.Fatalf("count legacy rows after id %d: %v", *afterId, err)
		}
		log.Printf("source rows after id %d: %d", *afterId, total)
		return
	}

	var migrated, failed int
	lastId := *afterId
	for {
		processed := migrated + failed
		if *maxRows > 0 && processed >= *maxRows {
			log.Printf("stopped at max-rows=%d", *maxRows)
			break
		}
		limit := *batchSize
		if *maxRows > 0 && *maxRows-processed < limit {
			limit = *maxRows - processed
		}
		var rows []legacyAPIRequestLog
		if err := sourceDB.Where("id > ?", lastId).Order("id asc").Limit(limit).Find(&rows).Error; err != nil {
			log.Fatalf("read legacy rows after id %d: %v", lastId, err)
		}
		if len(rows) == 0 {
			break
		}
		usageById, err := loadLegacyUsageLogs(sourceDB, rows)
		if err != nil {
			log.Printf("warning: failed to hydrate usage logs after id %d: %v", lastId, err)
		}
		for _, row := range rows {
			lastId = row.Id
			normalized := normalizeLegacy(row, usageById[row.UsageLogId])
			if *dryRun {
				migrated++
				continue
			}
			if err := model.CreateAPIRequestLog(normalized); err != nil {
				failed++
				log.Printf("failed legacy id=%d: %v", row.Id, err)
				continue
			}
			migrated++
		}
		log.Printf("progress: migrated=%d failed=%d last_id=%d", migrated, failed, lastId)
	}
	log.Printf("done: migrated=%d failed=%d", migrated, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func loadLegacyUsageLogs(sourceDB *gorm.DB, rows []legacyAPIRequestLog) (map[int]legacyUsageLog, error) {
	ids := make([]int, 0, len(rows))
	seen := map[int]bool{}
	for _, row := range rows {
		if row.UsageLogId <= 0 || seen[row.UsageLogId] {
			continue
		}
		seen[row.UsageLogId] = true
		ids = append(ids, row.UsageLogId)
	}
	if len(ids) == 0 {
		return map[int]legacyUsageLog{}, nil
	}

	var usageLogs []legacyUsageLog
	if err := sourceDB.Where("id IN ?", ids).Find(&usageLogs).Error; err != nil {
		return map[int]legacyUsageLog{}, err
	}
	byId := make(map[int]legacyUsageLog, len(usageLogs))
	for _, usage := range usageLogs {
		byId[usage.Id] = usage
	}
	return byId, nil
}

func normalizeLegacy(row legacyAPIRequestLog, usage legacyUsageLog) *model.APIRequestLog {
	items, parseStatus, parseError := service.BuildAPIRequestLogTrainingItems(row.RequestBody, row.ResponseBody)
	log := &model.APIRequestLog{
		Source:                model.APIRequestLogSourceLegacy,
		SourceId:              row.Id,
		UsageLogId:            row.UsageLogId,
		UserId:                row.UserId,
		Username:              row.Username,
		TokenId:               row.TokenId,
		TokenName:             row.TokenName,
		ModelName:             row.ModelName,
		CreatedAt:             row.CreatedAt,
		RequestId:             row.RequestId,
		UpstreamRequestId:     row.UpstreamRequestId,
		Method:                row.Method,
		RequestPath:           row.RequestPath,
		APIFormat:             legacyAPIFormat(row.Metadata, row.RequestPath),
		StatusCode:            row.StatusCode,
		IsStream:              row.IsStream,
		ChannelId:             row.ChannelId,
		Group:                 row.Group,
		RequestContentType:    row.RequestContentType,
		ResponseContentType:   row.ResponseContentType,
		RequestSize:           fallbackSize(row.RequestSize, row.RequestBody),
		ResponseSize:          fallbackSize(row.ResponseSize, row.ResponseBody),
		RequestOmittedReason:  row.RequestOmittedReason,
		ResponseOmittedReason: row.ResponseOmittedReason,
		Redacted:              row.Redacted,
		SchemaVersion:         model.APIRequestLogSchemaVersion,
		ParseStatus:           parseStatus,
		ParseError:            parseError,
		Items:                 items,
	}
	applyLegacyUsage(log, usage)
	return log
}

func applyLegacyUsage(log *model.APIRequestLog, usage legacyUsageLog) {
	if log == nil || usage.Id <= 0 {
		return
	}
	if log.UserId == 0 {
		log.UserId = usage.UserId
	}
	if log.Username == "" {
		log.Username = usage.Username
	}
	if log.TokenId == 0 {
		log.TokenId = usage.TokenId
	}
	if log.TokenName == "" {
		log.TokenName = usage.TokenName
	}
	if log.ModelName == "" {
		log.ModelName = usage.ModelName
	}
	if log.CreatedAt == 0 {
		log.CreatedAt = usage.CreatedAt
	}
	if log.RequestId == "" {
		log.RequestId = usage.RequestId
	}
	if log.UpstreamRequestId == "" {
		log.UpstreamRequestId = usage.UpstreamRequestId
	}
	if !log.IsStream {
		log.IsStream = usage.IsStream
	}
	if log.ChannelId == 0 {
		log.ChannelId = usage.ChannelId
	}
	if log.Group == "" {
		log.Group = usage.Group
	}
	log.Quota = usage.Quota
	log.PromptTokens = usage.PromptTokens
	log.CompletionTokens = usage.CompletionTokens
	log.TokenUsed = usage.PromptTokens + usage.CompletionTokens
	log.UseTime = usage.UseTime
}

func legacyAPIFormat(metadata string, path string) string {
	var meta map[string]interface{}
	if strings.TrimSpace(metadata) != "" && common.UnmarshalJsonStr(metadata, &meta) == nil {
		for _, key := range []string{"relay_format", "final_request_format"} {
			if value := common.Interface2String(meta[key]); value != "" {
				return value
			}
		}
	}
	switch {
	case strings.Contains(path, "/responses"):
		return "responses"
	case strings.Contains(path, "/chat/completions"):
		return "chat_completions"
	case strings.Contains(path, "/messages"):
		return "claude_messages"
	default:
		return ""
	}
}

func fallbackSize(size int64, body string) int64 {
	if size > 0 {
		return size
	}
	return int64(len(body))
}

func openDB(dsn string) (*gorm.DB, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("empty dsn")
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), migrationGormConfig())
	}
	if strings.HasPrefix(dsn, "local") || strings.HasSuffix(dsn, ".db") || strings.HasSuffix(dsn, ".sqlite") {
		return gorm.Open(sqlite.Open(strings.TrimPrefix(dsn, "local:")), migrationGormConfig())
	}
	if strings.HasPrefix(strings.ToLower(dsn), "mysql://") {
		converted, err := mysqlURLToDriverDSN(dsn)
		if err != nil {
			return nil, err
		}
		dsn = converted
	}
	return gorm.Open(mysql.Open(ensureMySQLDefaults(dsn)), migrationGormConfig())
}

func migrationGormConfig() *gorm.Config {
	return &gorm.Config{
		PrepareStmt: true,
		Logger:      logger.Default.LogMode(logger.Silent),
	}
}

func mysqlURLToDriverDSN(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	cfg := drivermysql.NewConfig()
	cfg.User = u.User.Username()
	cfg.Passwd, _ = u.User.Password()
	cfg.Net = "tcp"
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "3306"
	}
	cfg.Addr = net.JoinHostPort(host, port)
	cfg.DBName = strings.TrimPrefix(u.Path, "/")
	cfg.ParseTime = true
	cfg.Params = map[string]string{"charset": "utf8mb4"}
	for key, values := range u.Query() {
		if len(values) > 0 {
			cfg.Params[key] = values[len(values)-1]
		}
	}
	return cfg.FormatDSN(), nil
}

func ensureMySQLDefaults(dsn string) string {
	lower := strings.ToLower(dsn)
	if !strings.Contains(lower, "parsetime=") {
		dsn = appendDSNParam(dsn, "parseTime", "true")
		lower = strings.ToLower(dsn)
	}
	if !strings.Contains(lower, "charset=") && !strings.Contains(lower, "collation=") {
		dsn = appendDSNParam(dsn, "charset", "utf8mb4")
	}
	return dsn
}

func appendDSNParam(dsn string, key string, value string) string {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + key + "=" + value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
