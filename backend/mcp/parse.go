// MCP JSON-RPC 结果解析辅助（stdio / http 共享）。对齐 mcp/stdio.rs、mcp/http.rs 的 parse_*。
package mcp

import (
	"encoding/json"
	"fmt"
)

// parseList 从 result[key] 提取数组；若无该键则尝试把 result 整体当数组反序列化。
func parseList[T any](result json.RawMessage, key string) ([]T, error) {
	value := extractKey(result, key)
	var out []T
	if err := json.Unmarshal(value, &out); err != nil {
		return nil, fmt.Errorf("Failed to parse %s/list: %w", key, err)
	}
	return out, nil
}

// parseResourceContent 解析 resources/read：优先取 contents 数组第一个，否则整体反序列化为单对象。
func parseResourceContent(result json.RawMessage) (*McpResourceContent, error) {
	contents := extractKey(result, "contents")
	var list []McpResourceContent
	if err := json.Unmarshal(contents, &list); err == nil {
		if len(list) == 0 {
			return nil, fmt.Errorf("Empty resource contents")
		}
		return &list[0], nil
	}
	var single McpResourceContent
	if err := json.Unmarshal(contents, &single); err != nil {
		return nil, fmt.Errorf("Failed to parse resources/read: %w", err)
	}
	return &single, nil
}

// extractKey 返回 obj[key]（若 obj 是对象且含该键），否则原样返回 obj。
func extractKey(raw json.RawMessage, key string) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err == nil {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return raw
}

// rawOrNull 把可能为空的 RawMessage 规整为合法 JSON 值（空时为 null）。
func rawOrNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}
