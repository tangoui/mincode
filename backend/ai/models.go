package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"mincode/app/backend/core"
)

const maxModelListResponseSize = 1 << 20

type aiListModelsArgs struct {
	Config aiStreamConfig `json:"config"`
}

func init() {
	core.Register("ai_list_models", aiListModelsHandler)
}

// aiListModelsHandler 从当前 AI 服务读取模型清单。请求经 Go 后端发送，避免 WebView 的 CORS 限制。
func aiListModelsHandler(_ string, argsJSON string) (string, error) {
	var args aiListModelsArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}

	endpoint, err := aiModelsEndpoint(args.Config.BaseURL)
	if err != nil {
		return "", err
	}
	if err := validateAiBaseURL(args.Config.BaseURL); err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("构造模型列表请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if args.Config.APIFormat == "anthropic" {
		for name, value := range anthropicAuthHeaders(&args.Config) {
			req.Header.Set(name, value)
		}
	} else if strings.TrimSpace(args.Config.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+args.Config.APIKey)
	}

	client := aiTimeoutHTTPClient(30 * time.Second)
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取模型列表失败: %w", err)
	}
	defer res.Body.Close()

	raw, truncated := core.ReadCapped(res.Body, maxModelListResponseSize)
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("获取模型列表失败（HTTP %d）", res.StatusCode)
	}
	if truncated {
		return "", core.ErrString("模型列表响应过大")
	}

	models, err := parseModelList(raw)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(models)
}

func aiModelsEndpoint(baseURL string) (string, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "", core.ErrString("请先填写 AI 服务地址")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", core.ErrString("AI 服务地址无效")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", core.ErrString("AI 服务地址仅支持 HTTP 或 HTTPS")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", core.ErrString("AI 服务地址不能包含查询参数或片段")
	}

	return normalizeBaseURL(trimmed) + "/models", nil
}

// parseModelList 兼容 OpenAI /models 及常见 OpenAI 兼容服务的 data、models 格式。
func parseModelList(raw []byte) ([]string, error) {
	var payload struct {
		Data   json.RawMessage `json:"data"`
		Models json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %w", err)
	}

	candidates := payload.Data
	if len(candidates) == 0 || string(candidates) == "null" {
		candidates = payload.Models
	}
	if len(candidates) == 0 || string(candidates) == "null" {
		return []string{}, nil
	}

	var entries []json.RawMessage
	if err := json.Unmarshal(candidates, &entries); err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %w", err)
	}

	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		var name string
		if err := json.Unmarshal(entry, &name); err == nil {
			if name = strings.TrimSpace(name); name != "" {
				seen[name] = struct{}{}
			}
			continue
		}

		var model struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(entry, &model); err != nil {
			continue
		}
		name = strings.TrimSpace(model.ID)
		if name == "" {
			name = strings.TrimSpace(model.Name)
		}
		if name != "" {
			seen[name] = struct{}{}
		}
	}

	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return models, nil
}
