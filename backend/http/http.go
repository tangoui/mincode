// HTTP 域：代理前端 HTTP 请求（绕过 WebView CORS）+ 简易 Mock Server。
// 对齐 Tauri src-tauri/src/http_client.rs。
//
// 命令契约：
//
//	http_send_request {payload:{method,url,headers:[[k,v]],body?,timeout_ms|timeoutMs?}}
//	    -> {status,status_text,headers:[[k,v]],body,elapsed_ms,size_bytes}  (snake_case)
//	mock_server_start {port, rules:[MockRule]} -> string(id)
//	mock_server_stop  {server_id|serverId}     -> void
//	mock_server_list                            -> [MockServerInfo]
package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

func init() {
	core.RegisterCtx("http_send_request", httpSendRequestHandler)
	core.Register("mock_server_start", mockServerStartHandler)
	core.Register("mock_server_stop", mockServerStopHandler)
	core.Register("mock_server_list", mockServerListHandler)
}

// ---- HTTP 请求代理 ----

type httpRequestEnvelope struct {
	Payload httpRequestPayload `json:"payload"`
}

type httpRequestPayload struct {
	Method         string          `json:"method"`
	URL            string          `json:"url"`
	Headers        [][2]string     `json:"headers"`
	Body           *string         `json:"body"`
	BodyType       string          `json:"bodyType"`
	FormFields     []httpFormField `json:"formFields"`
	BinaryFilePath string          `json:"binaryFilePath"`
	// 同时兼容 timeout_ms / timeoutMs
	TimeoutMsSnake *uint64 `json:"timeout_ms"`
	TimeoutMsCamel *uint64 `json:"timeoutMs"`
}

func (p *httpRequestPayload) timeout() time.Duration {
	var ms uint64 = 30000
	if p.TimeoutMsSnake != nil {
		ms = *p.TimeoutMsSnake
	} else if p.TimeoutMsCamel != nil {
		ms = *p.TimeoutMsCamel
	}
	if ms == 0 {
		ms = 30000
	}
	const maxTimeoutMs = uint64((5 * time.Minute) / time.Millisecond)
	if ms > maxTimeoutMs {
		ms = maxTimeoutMs
	}
	return time.Duration(ms) * time.Millisecond
}

type httpFormField struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	FieldType   string `json:"fieldType"`
	FileName    string `json:"fileName"`
	ContentType string `json:"contentType"`
}

type httpResponsePayload struct {
	Status     int         `json:"status"`
	StatusText string      `json:"status_text"`
	Headers    [][2]string `json:"headers"`
	Body       string      `json:"body"`
	ElapsedMs  uint64      `json:"elapsed_ms"`
	SizeBytes  int         `json:"size_bytes"`
}

const httpMaxRedirects = 5
const httpMaxBody = 10 * 1024 * 1024
const httpMaxUploadBody = 25 * 1024 * 1024

func httpSendRequestHandler(ctx context.Context, _, argsJson string) (string, error) {
	var env httpRequestEnvelope
	if err := core.DecodeArgs(argsJson, &env); err != nil {
		return "", err
	}
	p := env.Payload
	if err := core.ValidateRequestURL(p.URL); err != nil {
		return "", err
	}

	start := time.Now()

	client := &http.Client{
		Timeout:       p.timeout(),
		CheckRedirect: checkSameOriginRedirect,
	}

	method := strings.ToUpper(strings.TrimSpace(p.Method))
	if method == "" {
		method = "GET"
	}
	bodyReader, generatedContentType, err := buildHTTPRequestBody(p)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, method, p.URL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("无效的 HTTP 方法或请求: %w", err)
	}
	for _, kv := range p.Headers {
		if strings.TrimSpace(kv[0]) != "" {
			req.Header.Set(kv[0], kv[1])
		}
	}
	if generatedContentType != "" &&
		(strings.EqualFold(strings.TrimSpace(p.BodyType), "form-data") || req.Header.Get("Content-Type") == "") {
		req.Header.Set("Content-Type", generatedContentType)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	elapsedMs := uint64(time.Since(start).Milliseconds())

	respHeaders := make([][2]string, 0, len(resp.Header))
	for k, vs := range resp.Header {
		for _, v := range vs {
			respHeaders = append(respHeaders, [2]string{k, v})
		}
	}

	limited := io.LimitReader(resp.Body, httpMaxBody+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("读取响应体失败: %w", err)
	}
	sizeBytes := len(raw)
	var body string
	if sizeBytes > httpMaxBody {
		body = string(raw[:httpMaxBody]) + "\n... (response truncated)"
	} else {
		body = string(raw)
	}

	statusText := http.StatusText(resp.StatusCode)

	out := httpResponsePayload{
		Status:     resp.StatusCode,
		StatusText: statusText,
		Headers:    respHeaders,
		Body:       body,
		ElapsedMs:  elapsedMs,
		SizeBytes:  sizeBytes,
	}
	return core.EncodeResult(out)
}

func readHTTPUploadFile(path string) ([]byte, string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, "", errors.New("上传文件路径为空")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return nil, "", fmt.Errorf("解析上传文件路径: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, "", fmt.Errorf("解析上传文件: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, "", fmt.Errorf("读取上传文件信息: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", errors.New("上传路径不是普通文件")
	}
	if info.Size() > httpMaxUploadBody {
		return nil, "", fmt.Errorf("上传文件超过 %d MB 限制", httpMaxUploadBody/(1024*1024))
	}
	data, err := os.ReadFile(canonical)
	if err != nil {
		return nil, "", fmt.Errorf("读取上传文件: %w", err)
	}
	return data, filepath.Base(canonical), nil
}

func buildHTTPRequestBody(p httpRequestPayload) (io.Reader, string, error) {
	switch strings.ToLower(strings.TrimSpace(p.BodyType)) {
	case "form-data":
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		for _, field := range p.FormFields {
			name := strings.TrimSpace(field.Name)
			if name == "" {
				continue
			}
			if strings.EqualFold(field.FieldType, "file") {
				data, fallbackName, err := readHTTPUploadFile(field.Value)
				if err != nil {
					return nil, "", fmt.Errorf("读取 multipart 字段 %q: %w", name, err)
				}
				filename := strings.TrimSpace(field.FileName)
				if filename == "" {
					filename = fallbackName
				}
				var part io.Writer
				if contentType := strings.TrimSpace(field.ContentType); contentType != "" {
					header := make(textproto.MIMEHeader)
					header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, filename))
					header.Set("Content-Type", contentType)
					part, err = writer.CreatePart(header)
				} else {
					part, err = writer.CreateFormFile(name, filename)
				}
				if err != nil {
					return nil, "", fmt.Errorf("创建 multipart 字段 %q: %w", name, err)
				}
				if _, err := part.Write(data); err != nil {
					return nil, "", fmt.Errorf("写入 multipart 字段 %q: %w", name, err)
				}
			} else if err := writer.WriteField(name, field.Value); err != nil {
				return nil, "", fmt.Errorf("写入 multipart 字段 %q: %w", name, err)
			}
			if buffer.Len() > httpMaxUploadBody {
				return nil, "", fmt.Errorf("multipart 请求体超过 %d MB 限制", httpMaxUploadBody/(1024*1024))
			}
		}
		if err := writer.Close(); err != nil {
			return nil, "", fmt.Errorf("结束 multipart 请求体: %w", err)
		}
		return bytes.NewReader(buffer.Bytes()), writer.FormDataContentType(), nil
	case "binary":
		data, _, err := readHTTPUploadFile(p.BinaryFilePath)
		if err != nil {
			return nil, "", err
		}
		return bytes.NewReader(data), "application/octet-stream", nil
	default:
		if p.Body == nil || *p.Body == "" {
			return nil, "", nil
		}
		return strings.NewReader(*p.Body), "", nil
	}
}

// 仅放行 http/https，拒绝云元数据端点。
// ---- Mock Server ----

type mockRule struct {
	Method  string      `json:"method"`
	Path    string      `json:"path"`
	Status  int         `json:"status"`
	Body    string      `json:"body"`
	Headers [][2]string `json:"headers"`
	DelayMs uint64      `json:"delay_ms"`
}

type mockServerInfo struct {
	ID      string     `json:"id"`
	Port    int        `json:"port"`
	Rules   []mockRule `json:"rules"`
	Running bool       `json:"running"`
}

type mockServerEntry struct {
	info     mockServerInfo
	listener net.Listener
	server   *http.Server
}

var (
	mockServersMu sync.Mutex
	mockServers   = map[string]*mockServerEntry{}
)

type mockStartArgs struct {
	Port  int        `json:"port"`
	Rules []mockRule `json:"rules"`
}

func mockServerStartHandler(_, argsJson string) (string, error) {
	var a mockStartArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if len(a.Rules) > 200 {
		return "", errors.New("Mock 规则数量超过上限(200)")
	}
	rules := make([]mockRule, len(a.Rules))
	copy(rules, a.Rules)
	for i := range rules {
		if rules[i].DelayMs > 60000 {
			rules[i].DelayMs = 60000
		}
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", a.Port))
	if err != nil {
		return "", fmt.Errorf("绑定端口失败: %w", err)
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port
	id := fmt.Sprintf("mock-%d", actualPort)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.ToUpper(r.Method)
		path := r.URL.Path
		var matched *mockRule
		for i := range rules {
			ru := &rules[i]
			if (ru.Method == "*" || strings.ToUpper(ru.Method) == method) && ru.Path == path {
				matched = ru
				break
			}
		}
		status := 404
		body := "Not Found"
		var extra [][2]string
		var delay uint64
		if matched != nil {
			status = matched.Status
			body = matched.Body
			extra = matched.Headers
			delay = matched.DelayMs
		}
		if delay > 0 {
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
		contentType := "text/plain"
		trimmed := strings.TrimLeft(body, " \t\r\n")
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			contentType = "application/json"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		for _, kv := range extra {
			w.Header().Set(kv[0], kv[1])
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()

	info := mockServerInfo{ID: id, Port: actualPort, Rules: rules, Running: true}
	mockServersMu.Lock()
	if old := mockServers[id]; old != nil {
		mockServersMu.Unlock()
		_ = srv.Close()
		return "", fmt.Errorf("Mock 服务已存在: %s", id)
	}
	mockServers[id] = &mockServerEntry{info: info, listener: ln, server: srv}
	mockServersMu.Unlock()

	return core.EncodeResult(id)
}

type mockStopArgs struct {
	ServerIDSnake string `json:"server_id"`
	ServerIDCamel string `json:"serverId"`
}

func mockServerStopHandler(_, argsJson string) (string, error) {
	var a mockStopArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	id := a.ServerIDSnake
	if id == "" {
		id = a.ServerIDCamel
	}
	mockServersMu.Lock()
	entry, ok := mockServers[id]
	if ok {
		delete(mockServers, id)
	}
	mockServersMu.Unlock()
	if ok && entry != nil && entry.server != nil {
		_ = entry.server.Close()
	}
	return core.EncodeResult(nil)
}

func mockServerListHandler(_, _ string) (string, error) {
	mockServersMu.Lock()
	list := make([]mockServerInfo, 0, len(mockServers))
	ids := make([]string, 0, len(mockServers))
	for id := range mockServers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		list = append(list, mockServers[id].info)
	}
	mockServersMu.Unlock()
	return core.EncodeResult(list)
}

// Shutdown 停止全部流会话与 Mock Server；可重复调用。
func Shutdown(ctx context.Context) {
	shutdownStreams()
	mockServersMu.Lock()
	entries := make([]*mockServerEntry, 0, len(mockServers))
	for id, entry := range mockServers {
		entries = append(entries, entry)
		delete(mockServers, id)
	}
	mockServersMu.Unlock()
	for _, entry := range entries {
		if entry != nil && entry.server != nil {
			_ = entry.server.Shutdown(ctx)
		}
	}
}
