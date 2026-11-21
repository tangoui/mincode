package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

// legacyPluginInvokeResultJSON is the previous InvokeGeneration unwrapping
// (a second full json.Unmarshal); the fast path must match it exactly.
func legacyPluginInvokeResultJSON(result json.RawMessage) string {
	if len(result) == 0 || string(result) == "null" {
		return "null"
	}
	var response struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(result, &response) == nil && len(response.Result) > 0 {
		return string(response.Result)
	}
	return string(result)
}

func TestPluginInvokeResultJSONMatchesFullDecode(t *testing.T) {
	cases := []string{
		``, `null`, `true`, `false`, `0`, `-1.5e3`, `"text"`, `"{\"result\":1}"`, `[]`, `[{"result":1}]`,
		`{}`, ` { } `, `{"result":null}`, `{"result":1}`, `{"result":"x"}`, `{"result":{"ok":true}}`,
		`{"result":[1,{"a":"}"}],"other":2}`, `{"other":{"result":3},"result":4}`, `{"other":{"result":3}}`,
		`{"a":"\"result\":5","b":[]}`, `{"result":1,"result":2}`, `{"result":{"nested":"]}"}, "tail":null}`,
		"{\n  \"result\" : { \"x\" : [ 1 , 2 ] } ,\n  \"z\" : \"q\"\n}",
		`{"Result":1}`, `{"RESULT":{"a":1}}`, `{"result":1,"Result":2}`, `{"Result":1,"result":2}`,
		`{"result":7}`, `{"result":7,"result":8}`, `{"reſult":9}`, `{"résult":1,"result":2}`,
		`{"résumé":"ok","result":3}`, `{"a\\b":1,"result":2}`, `{"res":1}`, `{"results":1}`, `{"resul":1}`,
		`{"result":"\\"}`, `{"result":"a\"b"}`, `{"x":"\\\\","result":[]}`, `{"result":-0.0}`,
		// Malformed input never reaches InvokeGeneration, but must still be safe.
		`{"result":1,`, `{"result"`, `{"result":}`, `{result:1}`, `{"a":1 "result":2}`, `{"result":1}}`,
	}
	for _, input := range cases {
		got := pluginInvokeResultJSON(json.RawMessage(input))
		want := legacyPluginInvokeResultJSON(json.RawMessage(input))
		if got != want {
			t.Errorf("pluginInvokeResultJSON(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPluginInvokeResultJSONLargeReplyIsSliceOfValidJSON(t *testing.T) {
	payload := benchmarkInvokeResultPayload(256 * 1024)
	reply := `{"result":` + payload + `}`
	got := pluginInvokeResultJSON(json.RawMessage(reply))
	if got != payload {
		t.Fatalf("unexpected unwrap: %d bytes, want %d", len(got), len(payload))
	}
	if !json.Valid([]byte(got)) {
		t.Fatal("unwrapped result must stay valid JSON")
	}
}

func benchmarkInvokeResultPayload(target int) string {
	var builder strings.Builder
	builder.WriteString(`{"rows":[`)
	for index := 0; builder.Len() < target-64; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"id":1,"name":"item \"quoted\" ]}","tag":"alpha","v":1.25}`)
	}
	builder.WriteString(`]}`)
	return builder.String()
}

func BenchmarkPluginInvokeResultJSON(b *testing.B) {
	reply := json.RawMessage(`{"result":` + benchmarkInvokeResultPayload(256*1024) + `}`)
	b.Run("fast", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = pluginInvokeResultJSON(reply)
		}
	})
	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = legacyPluginInvokeResultJSON(reply)
		}
	})
}
