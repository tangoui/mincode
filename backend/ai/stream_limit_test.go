package ai

import (
	"strings"
	"testing"
)

func roles(messages []chatMessage) string {
	parts := make([]string, len(messages))
	for i, message := range messages {
		parts[i] = message.Role + ":" + message.Content[:min(len(message.Content), 8)]
	}
	return strings.Join(parts, " ")
}

func TestLimitChatMessagesKeepsEveryLeadingSystemMessage(t *testing.T) {
	long := strings.Repeat("x", 400) // ~108 tokens each
	messages := []chatMessage{
		{Role: "system", Content: "context"},
		{Role: "system", Content: "instructions"},
		{Role: "system", Content: "summary"},
		{Role: "user", Content: "old-1 " + long},
		{Role: "assistant", Content: "old-2 " + long},
		{Role: "user", Content: "recent " + long},
	}
	kept, err := limitChatMessages(messages, &aiStreamConfig{MaxInputTokens: 260})
	if err != nil {
		t.Fatal(err)
	}
	got := roles(kept)
	want := "system:context system:instruct system:summary assistant:old-2 xx user:recent x"
	if got != want {
		t.Fatalf("kept = %q\nwant   %q", got, want)
	}
}

func TestLimitChatMessagesAlwaysSendsTheLastMessage(t *testing.T) {
	huge := strings.Repeat("y", 4000)
	messages := []chatMessage{
		{Role: "system", Content: "instructions"},
		{Role: "user", Content: huge},
	}
	kept, err := limitChatMessages(messages, &aiStreamConfig{MaxInputTokens: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].Content != huge {
		t.Fatalf("kept = %q, want only the oversized last message", roles(kept))
	}

	onlySystem := []chatMessage{{Role: "system", Content: "a"}, {Role: "system", Content: "b"}}
	kept, _ = limitChatMessages(onlySystem, &aiStreamConfig{MaxInputTokens: 1000})
	if roles(kept) != "system:a system:b" {
		t.Fatalf("kept = %q", roles(kept))
	}
	if kept, _ := limitChatMessages(nil, &aiStreamConfig{MaxInputTokens: 10}); len(kept) != 0 {
		t.Fatalf("empty input kept %d messages", len(kept))
	}
}

// A trailing system reminder (plan mode) must not displace the real last message.
func TestLimitChatMessagesKeepsLastMessageBeforeTrailingSystemReminders(t *testing.T) {
	long := strings.Repeat("z", 400) // ~108 tokens
	messages := []chatMessage{
		{Role: "system", Content: "instructions"},
		{Role: "user", Content: "old " + long},
		{Role: "assistant", Content: "older " + long},
		{Role: "user", Content: "recent " + long},
		{Role: "system", Content: "reminder-1"},
		{Role: "system", Content: "reminder-2"},
	}
	kept, err := limitChatMessages(messages, &aiStreamConfig{MaxInputTokens: 60})
	if err != nil {
		t.Fatal(err)
	}
	want := "user:recent z system:reminder system:reminder"
	if got := roles(kept); got != want {
		t.Fatalf("kept = %q\nwant   %q", got, want)
	}
	// With room to spare, earlier messages still fill the budget in order.
	kept, _ = limitChatMessages(messages, &aiStreamConfig{MaxInputTokens: 260})
	want = "system:instruct assistant:older zz user:recent z system:reminder system:reminder"
	if got := roles(kept); got != want {
		t.Fatalf("kept = %q\nwant   %q", got, want)
	}
}
