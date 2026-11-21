package ai

import "strings"

const (
	anthropicAPIVersion  = "2023-06-01"
	anthropicAuthXAPIKey = "x-api-key"
	anthropicAuthBearer  = "bearer"
)

// anthropicAuthHeaders 返回 Anthropic 格式请求的版本头与凭据头：AnthropicAuth 为 "bearer" 时
// 以 Authorization: Bearer 发送密钥（对应 Claude Code 的 ANTHROPIC_AUTH_TOKEN），其余取值
// （含缺省）一律使用 x-api-key。两个凭据头至多出现一个；密钥为空时都不发送。
func anthropicAuthHeaders(config *aiStreamConfig) map[string]string {
	headers := map[string]string{
		"anthropic-version": anthropicAPIVersion,
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return headers
	}
	if config.AnthropicAuth == anthropicAuthBearer {
		headers["Authorization"] = "Bearer " + config.APIKey
	} else {
		headers[anthropicAuthXAPIKey] = config.APIKey
	}
	return headers
}
