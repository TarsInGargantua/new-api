package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

// Exercise the real authentication, routing, provider adapter and settlement.
// Only the upstream model server is simulated. No production account is used.
func TestRyanBalanceResetRelay(t *testing.T) {
	dsn := os.Getenv("BALANCE_RELAY_TEST_DSN")
	if dsn == "" {
		dsn = "local"
	}
	t.Setenv("SQL_DSN", dsn)
	t.Setenv("SQL_MAX_OPEN_CONNS", "8")
	t.Setenv("SQL_MAX_IDLE_CONNS", "8")
	common.SQLitePath = filepath.Join(t.TempDir(), "relay.db") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	common.IsMasterNode = true
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.BatchUpdateEnabled = false
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.UsingSQLite = false
	constant.StreamingTimeout = 30
	common.LogConsumeEnabled = true
	common.APIRequestLogEnabled = true
	common.SetCallLogExcludedUsernames("ryan")
	require.NoError(t, model.InitDB())
	model.LOG_DB = model.DB
	model.REQUEST_LOG_DB = model.DB
	model.InitOptionMap()
	ratio_setting.InitRatioSettings()
	service.InitHttpClient()
	service.InitTokenEncoders()
	require.NoError(t, i18n.Init())
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	var upstreamCalls atomic.Int64
	var failUpstream atomic.Bool
	type inflightGate struct {
		started chan struct{}
		release chan struct{}
		once    sync.Once
	}
	var activeGate atomic.Pointer[inflightGate]
	var streamingGate atomic.Pointer[inflightGate]
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if gate := activeGate.Load(); gate != nil {
			gate.once.Do(func() { close(gate.started) })
			<-gate.release
		}
		if failUpstream.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"message":"simulated provider failure","type":"server_error"}}`)
			return
		}
		var body struct {
			Stream bool `json:"stream"`
		}
		if err := common.DecodeJson(r.Body, &body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			if gate := streamingGate.Load(); gate != nil {
				for i := 0; i < 100; i++ {
					fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-long\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"chunk-%03d \"},\"finish_reason\":null}]}\n\n", i)
					w.(http.Flusher).Flush()
					if i == 49 {
						gate.once.Do(func() { close(gate.started) })
						select {
						case <-gate.release:
						case <-r.Context().Done():
							return
						}
					}
				}
			}
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"},\"finish_reason\":null}]}\n\n")
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o-mini\",\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"total_tokens\":120}}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/responses" {
			fmt.Fprint(w, `{"id":"resp_test","object":"response","created_at":1,"status":"completed","model":"gpt-4o-mini","output":[{"id":"msg_test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"pong","annotations":[]}]}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}`)
			return
		}
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`)
	}))
	t.Cleanup(upstream.Close)
	target, err := model.DailyBalanceQuota()
	require.NoError(t, err)
	access := strings.Repeat("r", 32)
	adminAccess := strings.Repeat("a", 32)
	ryan := model.User{Id: 101, Username: "ryan", AffCode: "ryan", Quota: target, Group: "default", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AccessToken: &access, BalanceResetAt: model.BalanceResetDay(time.Now())}
	admin := model.User{Id: 102, Username: "admin", AffCode: "admin", Quota: 123456, Group: "default", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AccessToken: &adminAccess}
	require.NoError(t, model.DB.Create(&ryan).Error)
	require.NoError(t, model.DB.Create(&admin).Error)
	token := model.Token{UserId: 101, Key: strings.Repeat("t", 48), Name: "existing-ryan-key", UnlimitedQuota: true, Status: common.TokenStatusEnabled, Group: "default", ExpiredTime: -1}
	require.NoError(t, model.DB.Create(&token).Error)
	url := upstream.URL
	autoBan := 0
	channel := model.Channel{Id: 201, Type: constant.ChannelTypeOpenAI, Name: "local-test-provider", Key: "test-only-upstream-key", BaseURL: &url, Models: "gpt-4o-mini", Group: "default", Status: common.ChannelStatusEnabled, AutoBan: &autoBan}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-4o-mini", ChannelId: channel.Id, Enabled: true}).Error)
	model.InitChannelCache()
	if redisAddr := os.Getenv("BALANCE_RELAY_TEST_REDIS_ADDR"); redisAddr != "" {
		common.RDB = redis.NewClient(&redis.Options{Addr: redisAddr})
		require.NoError(t, common.RDB.Ping(context.Background()).Err())
		common.RedisEnabled = true
		common.SyncFrequency = 60
		t.Cleanup(func() { _ = common.RDB.Close() })
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(gin.Recovery(), sessions.Sessions("test-session", cookie.NewStore([]byte("local-only-test-secret-32-bytes!!"))))
	engine.POST("/api/user/self/reset_balance", middleware.UserAuth(), controller.ResetOwnBalance)
	engine.POST("/api/user/manage", middleware.AdminAuth(), controller.ManageUser)
	SetRelayRouter(engine)
	request := func(method, path, body, auth string, id int) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+auth)
		if id > 0 {
			req.Header.Set("New-Api-User", fmt.Sprint(id))
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}
	chat := func(stream bool) *httptest.ResponseRecorder {
		return request("POST", "/v1/chat/completions", fmt.Sprintf(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"ping"}],"stream":%t,"max_tokens":32}`, stream), "sk-"+token.Key, 0)
	}
	balance := func() int { var u model.User; require.NoError(t, model.DB.First(&u, 101).Error); return u.Quota }
	reset := func() {
		rec := request("POST", "/api/user/self/reset_balance", `{"username":"ryan"}`, access, 101)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"success":true`)
		require.Equal(t, target, balance())
	}
	override := func(value int) {
		rec := request("POST", "/api/user/manage", fmt.Sprintf(`{"id":101,"action":"add_quota","mode":"override","value":%d}`, value), adminAccess, 102)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"success":true`)
		require.Equal(t, value, balance())
	}
	t.Run("existing key works and settles", func(t *testing.T) {
		rec := chat(false)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "pong")
		require.Less(t, balance(), target)
	})
	t.Run("manual reset then same key", func(t *testing.T) {
		reset()
		rec := chat(false)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "pong")
		require.Less(t, balance(), target)
	})
	t.Run("streaming after reset", func(t *testing.T) {
		reset()
		rec := chat(true)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "[DONE]")
		require.Contains(t, rec.Body.String(), "pong")
		require.Less(t, balance(), target)
	})
	t.Run("zero balance blocks and reset restores same key", func(t *testing.T) {
		override(0)
		calls := upstreamCalls.Load()
		rec := chat(false)
		require.Equal(t, 403, rec.Code, rec.Body.String())
		require.Equal(t, calls, upstreamCalls.Load())
		reset()
		rec = chat(false)
		require.Equal(t, 200, rec.Code, rec.Body.String())
	})
	for _, action := range []string{"reset", "zero", "override"} {
		t.Run("complete 100 stream chunks across balance "+action, func(t *testing.T) {
			reset()
			gate := &inflightGate{started: make(chan struct{}), release: make(chan struct{})}
			release := sync.OnceFunc(func() { close(gate.release) })
			t.Cleanup(release)
			streamingGate.Store(gate)
			t.Cleanup(func() { streamingGate.Store(nil) })
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- chat(true) }()
			select {
			case <-gate.started:
			case <-time.After(5 * time.Second):
				t.Fatal("stream did not reach its midpoint")
			}
			switch action {
			case "reset":
				reset()
			case "zero":
				override(0)
				require.Equal(t, 403, chat(false).Code)
			case "override":
				override(target / 2)
			}
			release()
			select {
			case rec := <-done:
				require.Equal(t, 200, rec.Code, rec.Body.String())
				require.Contains(t, rec.Body.String(), "[DONE]")
				for i := 0; i < 100; i++ {
					require.Equal(t, 1, strings.Count(rec.Body.String(), fmt.Sprintf("chunk-%03d ", i)))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("balance change interrupted or stalled the stream")
			}
			streamingGate.Store(nil)
			reset()
			require.Equal(t, 200, chat(false).Code)
		})
	}
	t.Run("negative balance blocks and admin adjustment restores", func(t *testing.T) {
		override(-1)
		require.Equal(t, 403, chat(false).Code)
		override(target)
		require.Equal(t, 200, chat(false).Code)
	})
	t.Run("admin adds and subtracts balance without changing key", func(t *testing.T) {
		reset()
		for _, mode := range []string{"add", "subtract"} {
			before := balance()
			rec := request("POST", "/api/user/manage", fmt.Sprintf(`{"id":101,"action":"add_quota","mode":%q,"value":500000}`, mode), adminAccess, 102)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), `"success":true`)
			want := before + 500000
			if mode == "subtract" {
				want = before - 500000
			}
			require.Equal(t, want, balance())
			require.Equal(t, 200, chat(false).Code)
		}
	})
	t.Run("overdue balance resets on first relay", func(t *testing.T) {
		require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", 101).Updates(map[string]interface{}{"quota": 0, "balance_reset_at": model.BalanceResetDay(time.Now().Add(-48 * time.Hour))}).Error)
		rec := chat(false)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Greater(t, balance(), 0)
		require.Less(t, balance(), target)
	})

	t.Run("50 concurrent relay requests and 10 resets", func(t *testing.T) {
		reset()
		started := time.Now()
		var wg sync.WaitGroup
		results := make(chan string, 60)
		for i := 0; i < 60; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				var rec *httptest.ResponseRecorder
				if i%6 == 0 {
					rec = request("POST", "/api/user/self/reset_balance", `{"username":"ryan"}`, access, 101)
				} else {
					rec = chat(i%2 == 0)
				}
				if rec.Code != 200 || (i%6 == 0 && !strings.Contains(rec.Body.String(), `"success":true`)) {
					results <- fmt.Sprintf("request %d: status=%d body=%s", i, rec.Code, rec.Body.String())
				}
			}(i)
		}
		wg.Wait()
		t.Logf("60 concurrent operations completed in %s", time.Since(started))
		close(results)
		for result := range results {
			t.Error(result)
		}
		require.Greater(t, balance(), 0)
		require.LessOrEqual(t, balance(), target)
	})
	t.Run("50 concurrent relay requests and 10 admin balance overrides", func(t *testing.T) {
		reset()
		var wg sync.WaitGroup
		results := make(chan string, 60)
		for i := 0; i < 60; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				var rec *httptest.ResponseRecorder
				if i%6 == 0 {
					rec = request("POST", "/api/user/manage", fmt.Sprintf(`{"id":101,"action":"add_quota","mode":"override","value":%d}`, target), adminAccess, 102)
				} else {
					rec = chat(i%2 == 0)
				}
				if rec.Code != 200 || (i%6 == 0 && !strings.Contains(rec.Body.String(), `"success":true`)) {
					results <- fmt.Sprintf("request %d: status=%d body=%s", i, rec.Code, rec.Body.String())
				}
			}(i)
		}
		wg.Wait()
		close(results)
		for result := range results {
			t.Error(result)
		}
		require.Greater(t, balance(), 0)
		require.LessOrEqual(t, balance(), target)
	})
	t.Run("upstream failure refunds preconsumption", func(t *testing.T) {
		override(500000)
		failUpstream.Store(true)
		rec := chat(false)
		failUpstream.Store(false)
		require.NotEqual(t, 200, rec.Code)
		require.Eventually(t, func() bool { return balance() == 500000 }, 3*time.Second, 10*time.Millisecond)
		require.Equal(t, 200, chat(false).Code)
	})
	// Internal-use policy accepts old reservations being settled/refunded into
	// the new balance. Verify requests finish and the same key keeps working.
	t.Run("accepted balance drift - reset while request is in flight", func(t *testing.T) {
		override(500000)
		gate := &inflightGate{started: make(chan struct{}), release: make(chan struct{})}
		activeGate.Store(gate)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- chat(false) }()
		select {
		case <-gate.started:
		case <-time.After(5 * time.Second):
			t.Fatal("request did not reach upstream")
		}
		reserved := 500000 - balance()
		reset()
		close(gate.release)
		rec := <-done
		activeGate.Store(nil)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		t.Logf("preconsumed before reset=%d, balance after settlement=%d, target=%d", reserved, balance(), target)
		require.Equal(t, 200, chat(false).Code)
	})
	t.Run("accepted balance drift - reset while upstream failure refunds", func(t *testing.T) {
		override(500000)
		gate := &inflightGate{started: make(chan struct{}), release: make(chan struct{})}
		activeGate.Store(gate)
		failUpstream.Store(true)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- chat(false) }()
		select {
		case <-gate.started:
		case <-time.After(5 * time.Second):
			t.Fatal("request did not reach upstream")
		}
		reserved := 500000 - balance()
		reset()
		close(gate.release)
		rec := <-done
		activeGate.Store(nil)
		failUpstream.Store(false)
		require.NotEqual(t, 200, rec.Code)
		require.Eventually(t, func() bool { return balance() == target+reserved }, 3*time.Second, 10*time.Millisecond)
		t.Logf("preconsumed before reset=%d, balance after refund=%d, target=%d", reserved, balance(), target)
		require.Equal(t, 200, chat(false).Code)
	})

	t.Run("responses and model discovery after reset", func(t *testing.T) {
		reset()
		rec := request("POST", "/v1/responses", `{"model":"gpt-4o-mini","input":"ping","max_output_tokens":32}`, "sk-"+token.Key, 0)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "pong")
		require.Less(t, balance(), target)
		rec = request("GET", "/v1/models", "", "sk-"+token.Key, 0)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "gpt-4o-mini")
	})
	t.Run("wallet reset does not bypass token or account restrictions", func(t *testing.T) {
		limited := model.Token{UserId: 101, Key: strings.Repeat("l", 48), Name: "limited-key", RemainQuota: 1, Status: common.TokenStatusEnabled, Group: "default", ExpiredTime: -1}
		require.NoError(t, model.DB.Create(&limited).Error)
		require.NoError(t, model.DB.Model(&limited).Update("remain_quota", 0).Error)
		reset()
		rec := request("POST", "/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"ping"}]}`, "sk-"+limited.Key, 0)
		require.NotEqual(t, 200, rec.Code)
		rec = request("POST", "/api/user/manage", `{"id":101,"action":"disable"}`, adminAccess, 102)
		require.Contains(t, rec.Body.String(), `"success":true`)
		require.NotEqual(t, 200, chat(false).Code)
		rec = request("POST", "/api/user/manage", `{"id":101,"action":"enable"}`, adminAccess, 102)
		require.Contains(t, rec.Body.String(), `"success":true`)
		require.Equal(t, 200, chat(false).Code)
	})
	t.Run("reset authorization and identity are enforced", func(t *testing.T) {
		previous := balance()
		rec := request("POST", "/api/user/self/reset_balance", `{"username":"admin"}`, access, 101)
		require.Equal(t, 400, rec.Code)
		require.Equal(t, previous, balance())
		rec = request("POST", "/api/user/self/reset_balance", `{"username":"ryan"}`, access, 102)
		require.Equal(t, 401, rec.Code)
		require.Equal(t, previous, balance())
		rec = request("POST", "/api/user/self/reset_balance", `{"username":"ryan"}`, "sk-"+token.Key, 101)
		require.NotContains(t, rec.Body.String(), `"success":true`)
		require.Equal(t, previous, balance())
		rec = request("POST", "/api/user/manage", `{"id":102,"action":"add_quota","mode":"override","value":1}`, access, 101)
		require.NotContains(t, rec.Body.String(), `"success":true`)
	})

	if common.RedisEnabled {
		t.Run("stale Redis balances cannot override database balance", func(t *testing.T) {
			reset()
			require.NoError(t, common.RDB.HSet(context.Background(), "user:101", "Quota", 0).Err())
			require.Equal(t, 200, chat(false).Code)
			override(0)
			require.NoError(t, common.RDB.HSet(context.Background(), "user:101", "Quota", target).Err())
			require.Equal(t, 403, chat(false).Code)
			reset()
			require.Equal(t, 200, chat(false).Code)
		})
	}
	t.Run("account token and admin preserved", func(t *testing.T) {
		var current model.Token
		require.NoError(t, model.DB.First(&current, token.Id).Error)
		require.Equal(t, token.Key, current.Key)
		require.Equal(t, token.Status, current.Status)
		require.Equal(t, token.Group, current.Group)
		require.Equal(t, token.UnlimitedQuota, current.UnlimitedQuota)
		require.NoError(t, model.DB.First(&admin, 102).Error)
		require.Equal(t, 123456, admin.Quota)
		var count int64
		require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ? AND type = ?", 101, model.LogTypeConsume).Count(&count).Error)
		require.Zero(t, count)
	})
	// Settlement schedules metrics/cache work after the HTTP handler returns.
	// Drain it before closing fixtures or changing globals on a repeated run.
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
}
