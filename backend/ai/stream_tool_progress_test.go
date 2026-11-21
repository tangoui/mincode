package ai

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func progressSummary(events []map[string]any) []string {
	out := []string{}
	for _, event := range eventsOfType(events, "tool_call_progress") {
		out = append(out, fmt.Sprintf("%v:%v:%v", event["index"], event["name"], event["bytes"]))
	}
	return out
}

func TestToolCallProgressReportsNameThenEachKilobyte(t *testing.T) {
	rec := &recordedEvents{}
	em := &streamEmitter{emit: rec.emit}
	var progress toolCallProgress
	progress.observe(em, 0, "", 10) // 名字未到，不报告
	progress.observe(em, 0, "write", 0)
	progress.observe(em, 0, "write", 500)
	progress.observe(em, 0, "write_file", 600) // 名字分片补全后再报一次
	progress.observe(em, 0, "write_file", 1500)
	progress.observe(em, 0, "write_file", 1700)
	progress.observe(em, 1, "read_file", 0)
	got := strings.Join(progressSummary(rec.snapshot()), " ")
	want := "0:write:0 0:write_file:600 0:write_file:1700 1:read_file:0"
	if got != want {
		t.Fatalf("progress = %q, want %q", got, want)
	}
}

func TestToolCallProgressFlushesBufferedTextFirst(t *testing.T) {
	rec := &recordedEvents{}
	em := &streamEmitter{emit: rec.emit}
	em.chunk("先说明一下")
	var progress toolCallProgress
	progress.observe(em, 0, "write_file", 0)
	got := eventSummary(rec.snapshot())
	if len(got) != 2 || got[0] != "chunk:先说明一下" || got[1] != "tool_call_progress:" {
		t.Fatalf("events = %v", got)
	}
}

func TestStreamOpenAIReportsToolCallProgressBeforeTheCall(t *testing.T) {
	args := strings.Repeat("x", 1100)
	server := sseServer(t,
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"write_file\",\"arguments\":\"\"}}]}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"a\\\":\\\""+args+"\\\"}\"}}]}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	rec := &recordedEvents{}
	if err := streamOpenAI(context.Background(), server.Client(), &aiStreamConfig{BaseURL: server.URL, Model: "gpt-4o"},
		[]chatMessage{{Role: "user", Content: "hi"}}, &streamEmitter{emit: rec.emit}); err != nil {
		t.Fatal(err)
	}
	events := rec.snapshot()
	if got := progressSummary(events); len(got) != 2 || got[0] != "0:write_file:0" {
		t.Fatalf("progress = %v", got)
	}
	calls := eventsOfType(events, "tool_call")
	if len(calls) != 1 || calls[0]["id"] != "c1" {
		t.Fatalf("final call unchanged, got %v", calls)
	}
	if lastProgress, call := indexOfType(events, "tool_call_progress", true), indexOfType(events, "tool_call", false); lastProgress > call {
		t.Fatalf("progress must come before the final call: %v", eventSummary(events))
	}
}

func TestStreamOpenAIResponsesReportsToolCallProgress(t *testing.T) {
	server := sseServer(t,
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"i1\",\"call_id\":\"c1\",\"name\":\"read_file\",\"arguments\":\"\"}}\n\n"+
			"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"i1\",\"delta\":\"{}\"}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"id\":\"i1\",\"call_id\":\"c1\",\"name\":\"read_file\",\"arguments\":\"{}\"}]}}\n\n")
	rec := &recordedEvents{}
	if err := streamOpenAIResponses(context.Background(), server.Client(), &aiStreamConfig{BaseURL: server.URL, Model: "gpt-5"},
		[]chatMessage{{Role: "user", Content: "hi"}}, &streamEmitter{emit: rec.emit}); err != nil {
		t.Fatal(err)
	}
	if got := progressSummary(rec.snapshot()); len(got) != 1 || got[0] != "0:read_file:0" {
		t.Fatalf("progress = %v", got)
	}
}

func TestStreamAnthropicReportsToolCallProgress(t *testing.T) {
	server := sseServer(t, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"write_file\"}}\n\n"+
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"a\\\":\\\""+strings.Repeat("y", 1200)+"\\\"}\"}}\n\n"+
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n"+
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n")
	rec := &recordedEvents{}
	if err := streamAnthropic(context.Background(), server.Client(), &aiStreamConfig{BaseURL: server.URL, Model: "claude"},
		[]chatMessage{{Role: "user", Content: "hi"}}, &streamEmitter{emit: rec.emit}); err != nil {
		t.Fatal(err)
	}
	got := progressSummary(rec.snapshot())
	if len(got) != 2 || got[0] != "1:write_file:0" || !strings.HasPrefix(got[1], "1:write_file:12") {
		t.Fatalf("progress = %v", got)
	}
}

func indexOfType(events []map[string]any, kind string, last bool) int {
	found := -1
	for i, event := range events {
		if event["type"] == kind {
			found = i
			if !last {
				return i
			}
		}
	}
	return found
}
