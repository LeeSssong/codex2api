package basispoints

import (
	"github.com/tidwall/gjson"
	"io"
	"strings"
	"testing"
)

func TestCacheCreationAsInputTransformsNativeStreamWithoutChangingTotals(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","input":"hello"}`)
	_, bridge, e := Prepare(body, "test", nil)
	if e != nil {
		t.Fatal(e)
	}
	bridge.CacheCreationAsInput = true
	wire := `data: {"type":"response.completed","response":{"id":"test","status":"completed","output":[],"usage":{"input_tokens":1000,"output_tokens":50,"total_tokens":1050,"cache_creation_input_tokens":200,"input_tokens_details":{"cached_tokens":100,"cache_creation_tokens":200},"extension":9007199254740993}}}` + "\n\n"
	stream := bridge.Stream(io.NopCloser(strings.NewReader(wire)))
	defer stream.Close()
	raw, e := io.ReadAll(stream)
	if e != nil {
		t.Fatal(e)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "data: ") {
			data := []byte(strings.TrimPrefix(line, "data: "))
			if gjson.GetBytes(data, "type").String() == "response.completed" {
				usage := gjson.GetBytes(data, "response.usage")
				if usage.Get("cache_creation_input_tokens").Int() != 0 || usage.Get("input_tokens_details.cache_creation_tokens").Int() != 0 || usage.Get("input_tokens").Int() != 1000 || usage.Get("input_tokens_details.cached_tokens").Int() != 100 || usage.Get("extension").Raw != "9007199254740993" {
					t.Fatalf("usage=%s", usage.Raw)
				}
				return
			}
		}
	}
	t.Fatalf("no completion frame: %s", raw)
}
