package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mincode/app/backend/core"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	controlInvalidArgs = 2
	controlUnavailable = 3
	controlNotFound    = 4
	controlDenied      = 5
	controlTimeout     = 9
	controlWaiting     = 10
)

const agentControlUsage = `mincode tool list [--json]
mincode tool call <name> [--args JSON | --args-file FILE]
mincode pane split [--pane ID] --direction right|down [--cwd DIR] [--title TITLE] [--command COMMAND]
mincode pane tab [--cwd DIR] [--title TITLE] [--command COMMAND]
mincode pane send --pane ID [--submit] TEXT
mincode pane read --pane ID [--lines N]
mincode pane wait --pane ID [--timeout-secs N]
mincode pane list
mincode pane show --pane ID
mincode pane close --pane ID
Use --json for JSON output from pane commands.`

type cliExitError struct {
	code    int
	message string
}

func (e *cliExitError) Error() string { return e.message }

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *cliExitError
	if errors.As(err, &exit) {
		return exit.code
	}
	return 1
}

func controlArgumentError(format string, args ...any) error {
	return &cliExitError{controlInvalidArgs, fmt.Sprintf(format, args...)}
}

type controlCommand struct {
	route  string
	body   any
	asJSON bool
}

func runAgentControl(ctx context.Context, args []string, output io.Writer) error {
	if len(args) >= 2 && (args[1] == "--help" || args[1] == "-h" || args[1] == "help") {
		_, err := fmt.Fprintln(output, agentControlUsage)
		return err
	}
	cmd, err := parseControlCommand(args)
	if err != nil {
		return err
	}
	return cmd.run(ctx, output)
}

var controlFlags = map[string]string{
	"tools/list":  "",
	"tools/call":  "args args-file",
	"panes/list":  "",
	"panes/show":  "pane",
	"panes/split": "pane direction cwd title command",
	"panes/tab":   "cwd title command",
	"panes/send":  "pane submit",
	"panes/read":  "pane lines",
	"panes/wait":  "pane timeout-secs",
	"panes/close": "pane",
}

func parseControlCommand(args []string) (controlCommand, error) {
	if len(args) < 2 {
		return controlCommand{}, controlArgumentError("a subcommand is required (see --help)")
	}
	key := args[0] + "s/" + args[1]
	supported, ok := controlFlags[key]
	if !ok {
		return controlCommand{}, controlArgumentError("unknown subcommand %q (see --help)", args[1])
	}
	flags := map[string]bool{"json": true}
	for _, name := range strings.Fields(supported) {
		flags[name] = true
	}
	options, positional, err := parseControlFlags(args[2:], flags)
	if err != nil {
		return controlCommand{}, err
	}
	cmd := controlCommand{route: "/v1/" + key, asJSON: options["json"] == "true"}
	if key == "tools/call" {
		if len(positional) != 1 || positional[0] == "" {
			return cmd, controlArgumentError("tool call requires a tool name")
		}
		arguments, err := controlToolArguments(options)
		if err != nil {
			return cmd, err
		}
		cmd.body = map[string]any{"name": positional[0], "arguments": arguments}
		cmd.asJSON = true
		return cmd, nil
	}
	body := make(map[string]any)
	switch key {
	case "panes/show", "panes/send", "panes/read", "panes/wait", "panes/close":
		if options["pane"] == "" {
			return cmd, controlArgumentError("--pane is required")
		}
	case "panes/split":
		if options["direction"] != "right" && options["direction"] != "down" {
			return cmd, controlArgumentError("--direction must be right or down")
		}
	}
	if key == "panes/send" {
		submit := options["submit"] == "true"
		if len(positional) > 1 || len(positional) == 0 && !submit {
			return cmd, controlArgumentError("pane send requires one text argument, or --submit to press Enter")
		}
		text := ""
		if len(positional) == 1 {
			text = positional[0]
		}
		body["text"], body["submit"] = text, submit
	} else if len(positional) != 0 {
		return cmd, controlArgumentError("unexpected argument %q", positional[0])
	}
	for _, name := range []string{"pane", "cwd", "title", "command", "direction"} {
		if value, set := options[name]; set {
			body[name] = value
		}
	}
	for _, item := range []struct{ flag, field string }{{"lines", "lines"}, {"timeout-secs", "timeoutSecs"}} {
		if raw, set := options[item.flag]; set {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 {
				return cmd, controlArgumentError("--%s must be a nonnegative integer", item.flag)
			}
			body[item.field] = value
		}
	}
	cmd.body = body
	return cmd, nil
}

func parseControlFlags(args []string, allowed map[string]bool) (map[string]string, []string, error) {
	options := make(map[string]string)
	var positional []string
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			positional = append(positional, args...)
			break
		}
		name, isFlag := strings.CutPrefix(arg, "--")
		if !isFlag {
			positional = append(positional, arg)
			continue
		}
		name, value, explicit := strings.Cut(name, "=")
		if !allowed[name] {
			return nil, nil, controlArgumentError("unknown option --%s", name)
		}
		if _, exists := options[name]; exists {
			return nil, nil, controlArgumentError("duplicate option --%s", name)
		}
		switch name {
		case "json", "submit":
			if !explicit {
				value = "true"
			}
			if value != "true" && value != "false" {
				return nil, nil, controlArgumentError("--%s expects true or false", name)
			}
		default:
			if !explicit {
				if len(args) == 0 || strings.HasPrefix(args[0], "--") {
					return nil, nil, controlArgumentError("--%s requires a value", name)
				}
				value, args = args[0], args[1:]
			}
		}
		options[name] = value
	}
	return options, positional, nil
}

func controlToolArguments(options map[string]string) (json.RawMessage, error) {
	const maxArgs = 1 << 20
	args, fromInline := options["args"]
	path, fromFile := options["args-file"]
	if fromInline && fromFile {
		return nil, controlArgumentError("--args and --args-file are mutually exclusive")
	}
	if !fromInline && !fromFile {
		return json.RawMessage("{}"), nil
	}
	if fromFile {
		file, err := os.Open(path)
		if err != nil {
			return nil, controlArgumentError("read --args-file: %v", err)
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, maxArgs+1))
		if err != nil {
			return nil, controlArgumentError("read --args-file: %v", err)
		}
		args = string(data)
	}
	data := bytes.TrimSpace([]byte(args))
	if len(data) > maxArgs {
		return nil, controlArgumentError("tool arguments exceed 1 MiB")
	}
	if len(data) == 0 || data[0] != '{' || !json.Valid(data) {
		return nil, controlArgumentError("tool arguments must be a JSON object")
	}
	return data, nil
}

type controlEndpoint struct {
	url, token string
}

func findControlEndpoint(ctx context.Context) (controlEndpoint, error) {
	env := controlEndpoint{os.Getenv("MINCODE_CONTROL_URL"), os.Getenv("MINCODE_CONTROL_TOKEN")}
	if env.url != "" || env.token != "" {
		return validateControlEndpoint(env)
	}
	base := os.Getenv("MINCODE_DATA_DIR")
	if !filepath.IsAbs(base) {
		base = core.UserDataDir()
	}
	if base == "" {
		return controlEndpoint{}, &cliExitError{controlUnavailable, "MinCode is not running"}
	}
	dir := filepath.Join(base, core.AppDataNamespace, "external-mcp")
	var record struct {
		URL   string `json:"controlUrl"`
		Token string `json:"controlToken"`
	}
	err := readControlRecord(dir, "current.json", &record)
	if err != nil && !os.IsNotExist(err) {
		return controlEndpoint{}, &cliExitError{controlDenied, "MinCode control record is unavailable or unsafe"}
	}
	if record.URL != "" || record.Token != "" {
		return validateControlEndpoint(controlEndpoint{record.URL, record.Token})
	}
	var running struct {
		URL string `json:"url"`
	}
	if readControlRecord(dir, "running.json", &running) == nil && validControlURL(running.URL) {
		// Presence is token-free and may outlive a crashed desktop, so confirm
		// that the live control endpoint still rejects unauthenticated requests.
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		probe := controlEndpoint{url: strings.TrimSuffix(running.URL, "/")}
		if _, err := probe.post(probeCtx, "/v1/tools/list", []byte("{}")); commandExitCode(err) == controlDenied {
			return controlEndpoint{}, &cliExitError{controlDenied, "external agent control is disabled"}
		}
	}
	return controlEndpoint{}, &cliExitError{controlUnavailable, "MinCode is not running"}
}

func readControlRecord(dir, name string, out any) error {
	for _, parent := range []string{filepath.Dir(dir), dir} {
		info, err := os.Lstat(parent)
		if err != nil {
			return err
		}
		if !info.IsDir() || (parent == dir && runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return errors.New("unsafe MinCode control directory")
		}
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return errors.New("unsafe MinCode control file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1025))
	if err != nil {
		return err
	}
	if len(data) > 1024 {
		return errors.New("MinCode control record is too large")
	}
	return json.Unmarshal(data, out)
}

func validControlURL(address string) bool {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	return err == nil && port >= 1 && port <= 65535 && (parsed.EscapedPath() == "" || parsed.EscapedPath() == "/")
}

func validateControlEndpoint(endpoint controlEndpoint) (controlEndpoint, error) {
	_, err := hex.DecodeString(endpoint.token)
	if err != nil || len(endpoint.token) != 64 || !validControlURL(endpoint.url) {
		return controlEndpoint{}, &cliExitError{controlDenied, "invalid MinCode control URL or token"}
	}
	endpoint.url = strings.TrimSuffix(endpoint.url, "/")
	return endpoint, nil
}

func (endpoint controlEndpoint) post(ctx context.Context, route string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.url+route, bytes.NewReader(body))
	if err != nil {
		return nil, &cliExitError{controlUnavailable, "MinCode control service is unreachable"}
	}
	req.Header.Set("Content-Type", "application/json")
	if endpoint.token != "" {
		req.Header.Set("Authorization", "Bearer "+endpoint.token)
	}
	client := &http.Client{
		Timeout:       10*time.Minute + 20*time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &cliExitError{controlTimeout, "MinCode control request timed out"}
		}
		return nil, &cliExitError{controlUnavailable, "MinCode control service is unreachable"}
	}
	defer res.Body.Close()
	const maxResponse = 64 << 20
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return nil, errors.New("MinCode control response could not be read")
	}
	if res.StatusCode != http.StatusOK {
		var failed struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &failed)
		if failed.Error == "" {
			failed.Error = fmt.Sprintf("MinCode control HTTP %d", res.StatusCode)
		}
		code := 1
		switch res.StatusCode {
		case http.StatusBadRequest:
			code = controlInvalidArgs
		case http.StatusNotFound:
			code = controlNotFound
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict:
			code = controlDenied
		case http.StatusGatewayTimeout:
			code = controlTimeout
		}
		return nil, &cliExitError{code, failed.Error}
	}
	if !json.Valid(data) {
		return nil, errors.New("invalid MinCode control response")
	}
	return bytes.TrimSpace(data), nil
}

func (cmd controlCommand) run(ctx context.Context, output io.Writer) error {
	endpoint, err := findControlEndpoint(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(cmd.body)
	if err != nil {
		return err
	}
	data, err := endpoint.post(ctx, cmd.route, body)
	if err != nil {
		return err
	}
	switch cmd.route {
	case "/v1/tools/call":
		return writeControlToolResult(output, data)
	case "/v1/panes/wait":
		var result struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		if cmd.asJSON {
			_, err = fmt.Fprintln(output, string(data))
		} else {
			_, err = fmt.Fprintln(output, result.State)
		}
		if err != nil {
			return err
		}
		switch result.State {
		case "timeout":
			return &cliExitError{controlTimeout, ""}
		case "waiting_input":
			return &cliExitError{controlWaiting, ""}
		}
		return nil
	}
	if cmd.asJSON {
		_, err := fmt.Fprintln(output, string(data))
		return err
	}
	return writeControlPlainResult(cmd.route, output, data)
}

func writeControlPlainResult(route string, out io.Writer, data []byte) error {
	type pane struct{ ID, Title, State, CWD string }
	writePane := func(p pane) error {
		_, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", p.ID, p.Title, p.State, p.CWD)
		return err
	}
	switch route {
	case "/v1/tools/list":
		var result struct {
			Tools []struct{ Name, Description string }
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		for _, tool := range result.Tools {
			if _, err := fmt.Fprintf(out, "%s\t%s\n", tool.Name, tool.Description); err != nil {
				return err
			}
		}
	case "/v1/panes/read":
		var result struct{ Text string }
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		_, err := io.WriteString(out, result.Text)
		return err
	case "/v1/panes/list":
		var result struct{ Panes []pane }
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		for _, p := range result.Panes {
			if err := writePane(p); err != nil {
				return err
			}
		}
	case "/v1/panes/show", "/v1/panes/split", "/v1/panes/tab":
		var p pane
		if err := json.Unmarshal(data, &p); err != nil {
			return err
		}
		return writePane(p)
	}
	return nil
}

func writeControlToolResult(out io.Writer, data []byte) (err error) {
	var result struct {
		Content []map[string]json.RawMessage `json:"content"`
		IsError bool                         `json:"isError"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	var created []string
	defer func() {
		if err != nil {
			for _, path := range created {
				_ = os.Remove(path)
			}
		}
	}()
	for _, part := range result.Content {
		if string(part["type"]) != `"image"` {
			continue
		}
		var mime, encoded string
		if json.Unmarshal(part["mimeType"], &mime) != nil || json.Unmarshal(part["data"], &encoded) != nil {
			return errors.New("invalid MinCode image content")
		}
		path, err := saveControlImage(mime, encoded)
		if err != nil {
			return err
		}
		created = append(created, path)
		delete(part, "data")
		part["path"], _ = json.Marshal(path)
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return err
	}
	created = nil
	if result.IsError {
		return &cliExitError{1, ""}
	}
	return nil
}

func saveControlImage(mime, encoded string) (string, error) {
	const maxImage = 5 << 20
	if !strings.HasPrefix(mime, "image/") || len(encoded) > base64.StdEncoding.EncodedLen(maxImage) {
		return "", errors.New("invalid or oversized MinCode image")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) > maxImage {
		return "", errors.New("invalid or oversized MinCode image")
	}
	extension := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[mime]
	if extension == "" {
		extension = ".img"
	}
	file, err := os.CreateTemp("", "mincode-image-*"+extension)
	if err != nil {
		return "", err
	}
	_, err = file.Write(data)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}
