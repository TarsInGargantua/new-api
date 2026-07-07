package main

import "testing"

func TestNormalizeLegacyHydratesUsageLogFields(t *testing.T) {
	row := legacyAPIRequestLog{
		Id:          10,
		UsageLogId:  20,
		RequestBody: `{"messages":[{"role":"user","content":"hello"}]}`,
	}
	usage := legacyUsageLog{
		Id:                20,
		UserId:            2,
		CreatedAt:         123,
		Username:          "alice",
		TokenName:         "prod-token",
		ModelName:         "gpt-test",
		Quota:             99,
		PromptTokens:      7,
		CompletionTokens:  8,
		UseTime:           9,
		IsStream:          true,
		ChannelId:         3,
		TokenId:           4,
		Group:             "vip",
		RequestId:         "req-1",
		UpstreamRequestId: "upstream-1",
	}

	log := normalizeLegacy(row, usage)
	if log.SourceId != row.Id || log.UsageLogId != row.UsageLogId {
		t.Fatalf("unexpected source ids: %+v", log)
	}
	if log.UserId != usage.UserId || log.Username != usage.Username || log.TokenId != usage.TokenId || log.TokenName != usage.TokenName {
		t.Fatalf("usage identity fields were not hydrated: %+v", log)
	}
	if log.ModelName != usage.ModelName || log.ChannelId != usage.ChannelId || log.Group != usage.Group || !log.IsStream {
		t.Fatalf("usage request fields were not hydrated: %+v", log)
	}
	if log.Quota != usage.Quota || log.PromptTokens != usage.PromptTokens || log.CompletionTokens != usage.CompletionTokens || log.TokenUsed != 15 || log.UseTime != usage.UseTime {
		t.Fatalf("usage accounting fields were not hydrated: %+v", log)
	}
	if len(log.Items) == 0 {
		t.Fatal("expected request body to be parsed into items")
	}
}
