package proxy

import (
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/codex2api/database"
	"github.com/codex2api/internal/basispoints"
	"github.com/tidwall/gjson"
)

func TestBasispointsCacheCreationBillingAndDownstreamStayConsistent(t *testing.T) {
	for _, asInput := range []bool{false, true} {
		t.Run(map[bool]string{false: "write_pricing", true: "ordinary_input"}[asInput], func(t *testing.T) {
			u := map[string]any{"input_tokens": 1000, "output_tokens": 50, "total_tokens": 1050, "input_tokens_details": map[string]any{"cached_tokens": 100, "cache_write_tokens": 200}, "cache_creation_input_tokens": 200, "cache_creation": map[string]any{"ephemeral_5m_input_tokens": 150, "ephemeral_1h_input_tokens": 50}}
			raw := normalizeBasispointsUsage(u, asInput)
			if raw.InputTokens != 1000 || raw.CachedTokens != 100 || raw.CacheWriteTokens != 200 {
				t.Fatalf("raw observation lost: %+v", raw)
			}
			encoded, _ := json.Marshal(u)
			got := extractUsageFromResult(gjson.ParseBytes(encoded))
			wantWrite, wantOrdinary := 200, 700
			if asInput {
				wantWrite, wantOrdinary = 0, 900
			}
			if got.InputTokens != 1000 || got.OutputTokens != 50 || got.CachedTokens != 100 || got.CacheWriteTokens != wantWrite || got.TotalTokens != 1050 {
				t.Fatalf("normalization double counts or loses usage: %+v", got)
			}
			w5, w1 := splitClaudeCacheWrites(got)
			bill := database.CalculateCostBreakdownWithCacheWrites(got.InputTokens, got.OutputTokens, got.CachedTokens, w5, w1, "gpt-6-astra", "")
			if math.Abs(bill.InputCost/10*1000000-float64(wantOrdinary)) > 0.001 {
				t.Fatalf("ordinary input billed incorrectly: %+v", bill)
			}
			payload := []byte("{\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[],\"usage\":" + string(encoded) + "}}")
			msg := buildAnthropicResponseFromCompleted(payload, "gpt-6-astra")
			if msg.Usage.InputTokens != wantOrdinary || msg.Usage.CacheCreationInputTokens != wantWrite || msg.Usage.CacheReadInputTokens != 100 {
				t.Fatalf("Messages usage disagrees: %+v", msg.Usage)
			}
			log := database.UsageLogInput{InputTokens: got.InputTokens, OutputTokens: got.OutputTokens, CachedTokens: got.CachedTokens}
			applyUsageCacheWritesToLog(&log, got)
			if log.CacheWrite5mTokens+log.CacheWrite1hTokens != wantWrite {
				t.Fatalf("log disagrees: %+v", log)
			}
		})
	}
}

func TestBasispointsCacheCreationAliasesAndSSE(t *testing.T) {
	_, bridge, err := basispoints.Prepare([]byte("{\"model\":\"gpt-6-astra\",\"input\":\"hi\"}"), "billing-test", &basispoints.ReplayCache{})
	if err != nil {
		t.Fatal(err)
	}
	bridge.TransformUsage = func(u map[string]any) { normalizeBasispointsUsage(u, true) }
	raw := "{\"input_tokens\":1000,\"output_tokens\":50,\"input_tokens_details\":{\"cached_tokens\":100,\"cache_creation_tokens\":200,\"cache_write_tokens\":200},\"prompt_tokens_details\":{\"cache_write_tokens\":200,\"cache_creation_tokens\":200},\"cache_creation_input_tokens\":200,\"cache_write_tokens\":200,\"cache_write_input_tokens\":200,\"cache_creation_tokens\":200,\"cache_write_5m_tokens\":150,\"cache_write_1h_tokens\":50,\"cache_creation\":{\"ephemeral_5m_input_tokens\":150,\"ephemeral_1h_input_tokens\":50}}"
	body := bridge.Stream(io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"output\":[],\"usage\":" + raw + "}}\n\n")))
	out, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		u := gjson.Parse(strings.TrimPrefix(line, "data: ")).Get("response.usage")
		if !u.Exists() {
			continue
		}
		if u.Get("input_tokens").Int() != 1000 || u.Get("input_tokens_details.cached_tokens").Int() != 100 {
			t.Fatal("totals changed")
		}
		for _, field := range []string{"input_tokens_details.cache_write_tokens", "input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_creation_tokens", "cache_write_tokens", "cache_creation_tokens", "cache_creation_input_tokens", "cache_write_input_tokens", "cache_write_5m_tokens", "cache_write_1h_tokens", "cache_creation.ephemeral_5m_input_tokens", "cache_creation.ephemeral_1h_input_tokens"} {
			if u.Get(field).Int() != 0 {
				t.Fatalf("alias %s not cleared: %s", field, out)
			}
		}
		return
	}
	t.Fatal("missing final usage")
}
