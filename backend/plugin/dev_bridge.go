package plugin

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

const (
	pluginDevProtocolVersion = 1
	pluginDevPairTTL         = 2 * time.Minute
	pluginDevLeaseTTL        = 30 * time.Minute
	pluginDevHeartbeatTTL    = 45 * time.Second
	pluginDevRequestMaxBytes = 256 * 1024
	pluginDevBridgePrefix    = "/plugin-dev/v1"
	pluginDevPairingEvent    = "plugin:development-pairing"
	pluginDevChangedEvent    = "plugin:development-changed"
	pluginDevMaxLogs         = 200
	pluginDevLogRateWindow   = 10 * time.Second
	pluginDevLogRateLimit    = 100
)

type pluginDevPairRequest struct {
	ProtocolVersion int    `json:"protocolVersion"`
	CLIInstanceID   string `json:"cliInstanceId"`
	ProjectRoot     string `json:"projectRoot"`
	PackageRoot     string `json:"packageRoot"`
	UIURL           string `json:"uiUrl,omitempty"`
}

type pluginDevManifestSummary struct {
	APIVersion   int      `json:"apiVersion"`
	RuntimeType  string   `json:"runtimeType"`
	RuntimeEntry string   `json:"runtimeEntry"`
	Activation   string   `json:"activation"`
	Platforms    []string `json:"platforms"`
	CommandCount int      `json:"commandCount"`
	ViewCount    int      `json:"viewCount"`
	ActionCount  int      `json:"actionCount"`
	// Declarative (data-only) plugins: valid keymap presets and the reasons
	// skipped presets were dropped, so the developer sees them in the session.
	KeymapCount  int      `json:"keymapCount"`
	KeymapIssues []string `json:"keymapIssues,omitempty"`
	// Every contribution point: valid entry count and issue texts per point.
	Contributions map[string]pluginDevContributionSummary `json:"contributions,omitempty"`
}

type pluginDevContributionSummary struct {
	Count  int      `json:"count"`
	Issues []string `json:"issues,omitempty"`
}

type pluginDevPairing struct {
	ID              string                   `json:"id"`
	CLIInstanceID   string                   `json:"cliInstanceId"`
	ProjectRoot     string                   `json:"projectRoot"`
	PackageRoot     string                   `json:"packageRoot"`
	UIURL           string                   `json:"uiUrl,omitempty"`
	PluginID        string                   `json:"pluginId"`
	PluginName      string                   `json:"pluginName"`
	Version         string                   `json:"version"`
	Capabilities    []string                 `json:"capabilities"`
	Manifest        pluginDevManifestSummary `json:"manifest"`
	ProtocolVersion int                      `json:"protocolVersion"`
	CreatedAt       time.Time                `json:"createdAt"`
	ExpiresAt       time.Time                `json:"expiresAt"`
	Status          string                   `json:"status"`
	leaseToken      string
}

type pluginDevLog struct {
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

type pluginDevSession struct {
	ID              string                   `json:"id"`
	PluginID        string                   `json:"pluginId"`
	PluginName      string                   `json:"pluginName"`
	ProjectRoot     string                   `json:"projectRoot"`
	PackageRoot     string                   `json:"packageRoot"`
	UIURL           string                   `json:"uiUrl,omitempty"`
	CLIInstanceID   string                   `json:"cliInstanceId"`
	ConnectedAt     time.Time                `json:"connectedAt"`
	LastSeenAt      time.Time                `json:"lastSeenAt"`
	ExpiresAt       time.Time                `json:"expiresAt"`
	Status          string                   `json:"status"`
	Generation      uint64                   `json:"generation"`
	ProtocolVersion int                      `json:"protocolVersion"`
	Capabilities    []string                 `json:"capabilities"`
	Manifest        pluginDevManifestSummary `json:"manifest"`
	BuildError      string                   `json:"buildError,omitempty"`
	Logs            []pluginDevLog           `json:"logs,omitempty"`
	leaseToken      string
	logWindowStart  time.Time
	logWindowCount  int
}

type pluginDevDiscovery struct {
	ProtocolVersion int       `json:"protocolVersion"`
	Endpoint        string    `json:"endpoint"`
	InstanceID      string    `json:"instanceId"`
	InstanceToken   string    `json:"instanceToken"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

type pluginDevGenerationManifest struct {
	ProtocolVersion int               `json:"protocolVersion"`
	Generation      uint64            `json:"generation"`
	Files           map[string]string `json:"files"`
}

type pluginDevBridge struct {
	mu                sync.RWMutex
	listener          net.Listener
	server            *http.Server
	endpoint          string
	instanceID        string
	instanceToken     string
	discoveryPath     string
	pairings          map[string]*pluginDevPairing
	sessions          map[string]*pluginDevSession
	byLease           map[string]*pluginDevSession
	onApproved        func(pluginDevSession) error
	onDisconnected    func(pluginDevSession)
	onGenerationReady func(pluginDevSession, string, uint64) error
	publishes         map[string]*pluginDevPublishOperation
	publishWorkers    sync.WaitGroup
	publishReserved   int64
	publishStages     int
	closed            bool
}

func newPluginDevBridge() *pluginDevBridge {
	return &pluginDevBridge{
		pairings:  map[string]*pluginDevPairing{},
		publishes: map[string]*pluginDevPublishOperation{},
		sessions:  map[string]*pluginDevSession{},
		byLease:   map[string]*pluginDevSession{},
	}
}

func pluginDevDiscoveryPath() string {
	return filepath.Join(userDataDir(), pluginDataDirectory, "plugin-development.json")
}

func (bridge *pluginDevBridge) Start() error {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if bridge.closed {
		return errors.New("plugin development bridge is closed")
	}
	if bridge.listener != nil {
		return bridge.writeDiscoveryLocked()
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start plugin development bridge: %w", err)
	}
	instanceID, err := randomHex(16)
	if err != nil {
		_ = listener.Close()
		return err
	}
	instanceToken, err := randomHex(32)
	if err != nil {
		_ = listener.Close()
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc(pluginDevBridgePrefix+"/publish", bridge.handlePublish)
	mux.HandleFunc(pluginDevBridgePrefix+"/publish/", bridge.handlePublishStatus)
	mux.HandleFunc(pluginDevBridgePrefix+"/pair", bridge.handlePair)
	mux.HandleFunc(pluginDevBridgePrefix+"/pair/", bridge.handlePairStatus)
	mux.HandleFunc(pluginDevBridgePrefix+"/heartbeat", bridge.handleHeartbeat)
	mux.HandleFunc(pluginDevBridgePrefix+"/manifest", bridge.handleManifest)
	mux.HandleFunc(pluginDevBridgePrefix+"/generation/ready", bridge.handleGenerationReady)
	mux.HandleFunc(pluginDevBridgePrefix+"/build/error", bridge.handleBuildError)
	mux.HandleFunc(pluginDevBridgePrefix+"/logs", bridge.handleLogs)
	mux.HandleFunc(pluginDevBridgePrefix+"/disconnect", bridge.handleDisconnect)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	bridge.listener = listener
	bridge.server = server
	bridge.endpoint = "http://" + listener.Addr().String() + pluginDevBridgePrefix
	bridge.instanceID = instanceID
	bridge.instanceToken = instanceToken
	bridge.discoveryPath = pluginDevDiscoveryPath()
	if err := bridge.writeDiscoveryLocked(); err != nil {
		bridge.listener = nil
		bridge.server = nil
		_ = listener.Close()
		return err
	}
	go func() {
		serveErr := server.Serve(listener)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			core.Notify(core.AppNotificationPayload{Source: "plugin", Level: "error", Code: "plugin-development-bridge-stopped"})
		}
	}()
	return nil
}

func (bridge *pluginDevBridge) writeDiscoveryLocked() error {
	if err := os.MkdirAll(filepath.Dir(bridge.discoveryPath), 0o700); err != nil {
		return fmt.Errorf("create plugin development data directory: %w", err)
	}
	payload, err := json.Marshal(pluginDevDiscovery{
		ProtocolVersion: pluginDevProtocolVersion,
		Endpoint:        bridge.endpoint,
		InstanceID:      bridge.instanceID,
		InstanceToken:   bridge.instanceToken,
		ExpiresAt:       time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(bridge.discoveryPath), ".plugin-development-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, bridge.discoveryPath); err != nil {
		return fmt.Errorf("publish plugin development discovery: %w", err)
	}
	return nil
}

func (bridge *pluginDevBridge) Close() {
	bridge.mu.Lock()
	if bridge.closed {
		bridge.mu.Unlock()
		return
	}
	bridge.closed = true
	bridge.closePublishesLocked()
	server := bridge.server
	discoveryPath := bridge.discoveryPath
	bridge.server = nil
	bridge.listener = nil
	bridge.endpoint = ""
	bridge.instanceToken = ""
	bridge.pairings = map[string]*pluginDevPairing{}
	bridge.sessions = map[string]*pluginDevSession{}
	bridge.byLease = map[string]*pluginDevSession{}
	bridge.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
	bridge.cleanupPublishes()
	if discoveryPath != "" {
		_ = os.Remove(discoveryPath)
	}
}

func (bridge *pluginDevBridge) authenticateInstance(request *http.Request) bool {
	candidate := bearerToken(request)
	bridge.mu.RLock()
	expected := bridge.instanceToken
	bridge.mu.RUnlock()
	return expected != "" && subtle.ConstantTimeCompare([]byte(candidate), []byte(expected)) == 1
}

func (bridge *pluginDevBridge) authenticateLease(request *http.Request) *pluginDevSession {
	candidate := bearerToken(request)
	if candidate == "" {
		return nil
	}
	bridge.mu.RLock()
	var session *pluginDevSession
	for token, current := range bridge.byLease {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			copy := *current
			session = &copy
			break
		}
	}
	bridge.mu.RUnlock()
	if session == nil || time.Now().After(session.ExpiresAt) || time.Since(session.LastSeenAt) > pluginDevHeartbeatTTL {
		return nil
	}
	return session
}

func bearerToken(request *http.Request) string {
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
}

func requireLoopback(request *http.Request) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	return err == nil && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func decodePluginDevRequest(request *http.Request, target any) error {
	body, err := io.ReadAll(io.LimitReader(request.Body, pluginDevRequestMaxBytes+1))
	if err != nil || len(body) > pluginDevRequestMaxBytes {
		return errors.New("invalid request")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid request")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("invalid request")
	}
	return nil
}

func writePluginDevJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func validatePluginDevUIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("development UI URL must be a plain loopback HTTP URL")
	}
	host := parsed.Hostname()
	port := parsed.Port()
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || port == "" {
		return "", errors.New("development UI URL must use an explicit loopback IP and port")
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) != 1 || len(segments[0]) != 48 {
		return "", errors.New("development UI URL must include one random path key")
	}
	if _, err := hex.DecodeString(segments[0]); err != nil {
		return "", errors.New("development UI URL path key is invalid")
	}
	parsed.Path = "/" + segments[0] + "/"
	return parsed.String(), nil
}

func pathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validatePluginDevGeneration(projectRoot, generationRoot string, generation uint64) error {
	expected := filepath.Join(projectRoot, ".mincode", "dev", "generations", fmt.Sprintf("%020d", generation))
	if generationRoot != expected {
		return errors.New("development generation path does not match its sequence")
	}
	metadata, err := os.ReadFile(filepath.Join(generationRoot, ".mincode-generation.json"))
	if err != nil || len(metadata) > pluginDevRequestMaxBytes {
		return errors.New("development generation manifest is unavailable")
	}
	var manifest pluginDevGenerationManifest
	decoder := json.NewDecoder(bytes.NewReader(metadata))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || manifest.ProtocolVersion != pluginDevProtocolVersion || manifest.Generation != generation {
		return errors.New("development generation manifest is invalid")
	}
	ready, err := os.ReadFile(filepath.Join(generationRoot, ".mincode-ready"))
	if err != nil {
		return errors.New("development generation is not ready")
	}
	digest := sha256.Sum256(bytes.TrimSpace(metadata))
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(string(ready))), []byte(hex.EncodeToString(digest[:]))) != 1 {
		return errors.New("development generation ready marker is invalid")
	}
	actual := map[string]string{}
	err = filepath.WalkDir(generationRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("development generation contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(generationRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == ".mincode-generation.json" || relative == ".mincode-ready" {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, io.LimitReader(file, pluginInstallMaxBytes+1))
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		actual[relative] = hex.EncodeToString(hash.Sum(nil))
		return nil
	})
	if err != nil || len(actual) != len(manifest.Files) {
		return errors.New("development generation file set changed")
	}
	for path, expectedHash := range manifest.Files {
		if actualHash, ok := actual[path]; !ok || subtle.ConstantTimeCompare([]byte(actualHash), []byte(expectedHash)) != 1 {
			return fmt.Errorf("development generation file changed: %s", path)
		}
	}
	return nil
}

func pluginDevCapabilities(manifest Manifest) []string {
	capabilities := make([]string, len(manifest.Capabilities))
	for index, capability := range manifest.Capabilities {
		capabilities[index] = string(capability)
	}
	return capabilities
}

func summarizePluginDevManifest(manifest Manifest) pluginDevManifestSummary {
	activation := manifest.Runtime.Activation
	if activation == "" && !manifest.isDeclarative() {
		activation = pluginActivationOnDemand
	}
	var issues []string
	for _, issue := range manifest.keymapIssues() {
		if len(issues) == keymapMaxPresets*2 {
			break
		}
		issues = append(issues, redactPluginDevLog(keymapIssueText(issue)))
	}
	var contributions map[string]pluginDevContributionSummary
	for _, key := range manifest.contributionKeys() {
		resolved := manifest.contribution(key)
		summary := pluginDevContributionSummary{Count: len(resolved.Entries)}
		for _, issue := range resolved.Issues {
			if len(summary.Issues) == 16 {
				break
			}
			summary.Issues = append(summary.Issues, redactPluginDevLog(contributionIssueText(issue)))
		}
		if contributions == nil {
			contributions = map[string]pluginDevContributionSummary{}
		}
		contributions[key] = summary
	}
	return pluginDevManifestSummary{
		APIVersion: manifest.APIVersion, RuntimeType: manifest.Runtime.Type,
		RuntimeEntry: manifest.Runtime.Entry, Activation: activation,
		Platforms:    append([]string(nil), manifest.Platforms...),
		CommandCount: len(manifest.Contributes.Commands), ViewCount: len(manifest.Contributes.Views),
		ActionCount: len(manifest.Contributes.Actions),
		KeymapCount: len(manifest.KeymapPresets()), KeymapIssues: issues,
		Contributions: contributions,
	}
}

func canonicalPluginDevRoot(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("path must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func (bridge *pluginDevBridge) handlePair(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !requireLoopback(request) || !bridge.authenticateInstance(request) {
		writePluginDevJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var input pluginDevPairRequest
	if err := decodePluginDevRequest(request, &input); err != nil || input.ProtocolVersion != pluginDevProtocolVersion {
		writePluginDevJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid request or protocol version"})
		return
	}
	input.CLIInstanceID = strings.TrimSpace(input.CLIInstanceID)
	if input.CLIInstanceID == "" || len(input.CLIInstanceID) > 128 {
		writePluginDevJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid cli instance"})
		return
	}
	projectRoot, err := canonicalPluginDevRoot(input.ProjectRoot)
	if err != nil {
		writePluginDevJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid project root"})
		return
	}
	packageRoot, err := canonicalPluginDevRoot(input.PackageRoot)
	if err != nil {
		writePluginDevJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid package root"})
		return
	}
	relative, err := filepath.Rel(projectRoot, packageRoot)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		writePluginDevJSON(response, http.StatusBadRequest, map[string]string{"error": "package root must be inside project root"})
		return
	}
	if _, err := inspectPackageDirectory(packageRoot); err != nil {
		writePluginDevJSON(response, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	manifest, err := readManifest(packageRoot)
	if err != nil {
		writePluginDevJSON(response, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	uiURL, err := validatePluginDevUIURL(input.UIURL)
	if err != nil {
		writePluginDevJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	id, err := randomHex(16)
	if err != nil {
		writePluginDevJSON(response, http.StatusInternalServerError, map[string]string{"error": "pairing unavailable"})
		return
	}
	now := time.Now()
	pairing := &pluginDevPairing{
		ID: id, CLIInstanceID: input.CLIInstanceID, ProjectRoot: projectRoot, PackageRoot: packageRoot,
		UIURL: uiURL, PluginID: manifest.ID, PluginName: manifest.Name, Version: manifest.Version,
		Capabilities: pluginDevCapabilities(manifest), Manifest: summarizePluginDevManifest(manifest),
		ProtocolVersion: pluginDevProtocolVersion, CreatedAt: now,
		ExpiresAt: now.Add(pluginDevPairTTL), Status: "pending",
	}
	bridge.mu.Lock()
	bridge.pairings[id] = pairing
	expired := bridge.pruneLocked(now)
	bridge.mu.Unlock()
	bridge.notifyExpired(expired)
	core.Emit(pluginDevPairingEvent, pairing.publicCopy())
	writePluginDevJSON(response, http.StatusAccepted, map[string]any{"pairingId": id, "expiresAt": pairing.ExpiresAt})
}

func (bridge *pluginDevBridge) handlePairStatus(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || !requireLoopback(request) || !bridge.authenticateInstance(request) {
		writePluginDevJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id := strings.TrimSpace(strings.TrimPrefix(request.URL.Path, pluginDevBridgePrefix+"/pair/"))
	bridge.mu.RLock()
	pairing := bridge.pairings[id]
	if pairing != nil {
		copy := *pairing
		pairing = &copy
	}
	bridge.mu.RUnlock()
	if pairing == nil || time.Now().After(pairing.ExpiresAt) {
		writePluginDevJSON(response, http.StatusNotFound, map[string]string{"error": "pairing unavailable"})
		return
	}
	result := map[string]any{"status": pairing.Status, "expiresAt": pairing.ExpiresAt}
	if pairing.Status == "approved" {
		result["leaseToken"] = pairing.leaseToken
		result["sessionId"] = pairing.ID
	}
	writePluginDevJSON(response, http.StatusOK, result)
}

func (bridge *pluginDevBridge) handleHeartbeat(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !requireLoopback(request) {
		writePluginDevJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	session := bridge.authenticateLease(request)
	if session == nil {
		writePluginDevJSON(response, http.StatusUnauthorized, map[string]string{"error": "lease unavailable"})
		return
	}
	now := time.Now()
	bridge.mu.Lock()
	if current := bridge.sessions[session.ID]; current != nil {
		current.LastSeenAt = now
		current.ExpiresAt = now.Add(pluginDevLeaseTTL)
		session = current
	}
	bridge.mu.Unlock()
	writePluginDevJSON(response, http.StatusOK, map[string]any{"sessionId": session.ID, "expiresAt": session.ExpiresAt})
}

func (bridge *pluginDevBridge) handleManifest(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !requireLoopback(request) || bridge.authenticateLease(request) == nil {
		writePluginDevJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writePluginDevJSON(response, http.StatusNotImplemented, map[string]string{"error": "manifest updates require generation publishing"})
}

func (bridge *pluginDevBridge) handleGenerationReady(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !requireLoopback(request) {
		writePluginDevJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	session := bridge.authenticateLease(request)
	if session == nil {
		writePluginDevJSON(response, http.StatusUnauthorized, map[string]string{"error": "lease unavailable"})
		return
	}
	var input struct {
		Generation uint64 `json:"generation"`
		Root       string `json:"root"`
	}
	if err := decodePluginDevRequest(request, &input); err != nil || input.Generation == 0 {
		writePluginDevJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid generation"})
		return
	}
	generationRoot, err := canonicalPluginDevRoot(input.Root)
	if err != nil || !pathWithinRoot(session.ProjectRoot, generationRoot) || generationRoot == session.ProjectRoot {
		writePluginDevJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid generation root"})
		return
	}
	if err := validatePluginDevGeneration(session.ProjectRoot, generationRoot, input.Generation); err != nil {
		writePluginDevJSON(response, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if input.Generation <= session.Generation {
		writePluginDevJSON(response, http.StatusConflict, map[string]string{"error": "generation must increase"})
		return
	}
	manifest, err := readManifest(generationRoot)
	if err != nil {
		writePluginDevJSON(response, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if bridge.onGenerationReady != nil {
		if err := bridge.onGenerationReady(session.publicCopy(), generationRoot, input.Generation); err != nil {
			writePluginDevJSON(response, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
	}
	bridge.mu.Lock()
	if current := bridge.sessions[session.ID]; current != nil && input.Generation > current.Generation {
		current.Generation = input.Generation
		current.PackageRoot = generationRoot
		current.Capabilities = pluginDevCapabilities(manifest)
		current.Manifest = summarizePluginDevManifest(manifest)
		current.BuildError = ""
		session = current
	}
	bridge.mu.Unlock()
	core.Emit(pluginDevChangedEvent, session.publicCopy())
	writePluginDevJSON(response, http.StatusAccepted, map[string]any{"generation": session.Generation})
}

func (bridge *pluginDevBridge) handleBuildError(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !requireLoopback(request) {
		writePluginDevJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	session := bridge.authenticateLease(request)
	if session == nil {
		writePluginDevJSON(response, http.StatusUnauthorized, map[string]string{"error": "lease unavailable"})
		return
	}
	var input struct {
		Message string `json:"message"`
	}
	if err := decodePluginDevRequest(request, &input); err != nil {
		writePluginDevJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid diagnostic"})
		return
	}
	message := redactPluginDevLog(input.Message)
	bridge.mu.Lock()
	if current := bridge.sessions[session.ID]; current != nil {
		current.BuildError = message
		session = current
	}
	bridge.mu.Unlock()
	core.Emit(pluginDevChangedEvent, session.publicCopy())
	writePluginDevJSON(response, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (bridge *pluginDevBridge) handleLogs(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !requireLoopback(request) {
		writePluginDevJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	session := bridge.authenticateLease(request)
	if session == nil {
		writePluginDevJSON(response, http.StatusUnauthorized, map[string]string{"error": "lease unavailable"})
		return
	}
	var input struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}
	if err := decodePluginDevRequest(request, &input); err != nil {
		writePluginDevJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid log"})
		return
	}
	level := strings.ToLower(strings.TrimSpace(input.Level))
	if level != "debug" && level != "info" && level != "warn" && level != "error" {
		writePluginDevJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid log level"})
		return
	}
	message := redactPluginDevLog(input.Message)
	if message == "" {
		writePluginDevJSON(response, http.StatusBadRequest, map[string]string{"error": "log message is required"})
		return
	}
	entry := pluginDevLog{Level: level, Message: message, CreatedAt: time.Now()}
	bridge.mu.Lock()
	if current := bridge.sessions[session.ID]; current != nil {
		if !current.acceptLogLocked(entry.CreatedAt) {
			bridge.mu.Unlock()
			writePluginDevJSON(response, http.StatusTooManyRequests, map[string]string{"error": "log rate limit exceeded"})
			return
		}
		current.Logs = append(current.Logs, entry)
		if len(current.Logs) > pluginDevMaxLogs {
			current.Logs = append([]pluginDevLog(nil), current.Logs[len(current.Logs)-pluginDevMaxLogs:]...)
		}
		session = current
	}
	bridge.mu.Unlock()
	core.Emit(pluginDevChangedEvent, session.publicCopy())
	writePluginDevJSON(response, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (session *pluginDevSession) acceptLogLocked(now time.Time) bool {
	if session.logWindowStart.IsZero() || now.Sub(session.logWindowStart) >= pluginDevLogRateWindow {
		session.logWindowStart = now
		session.logWindowCount = 0
	}
	if session.logWindowCount >= pluginDevLogRateLimit {
		return false
	}
	session.logWindowCount++
	return true
}

var (
	pluginDevSensitiveLogPattern = regexp.MustCompile(`(?i)("?(?:authorization|token|password|secret|private[ _-]?key|connection[ _-]?string)"?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	pluginDevBearerLogPattern    = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	pluginDevPEMLogPattern       = regexp.MustCompile(`(?s)-----BEGIN [^-\r\n]+-----.*?-----END [^-\r\n]+-----`)
)

func redactPluginDevLog(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 4*1024 {
		message = message[:4*1024]
	}
	message = pluginDevPEMLogPattern.ReplaceAllString(message, "[REDACTED PEM]")
	message = pluginDevBearerLogPattern.ReplaceAllString(message, "Bearer [REDACTED]")
	return pluginDevSensitiveLogPattern.ReplaceAllString(message, "$1[REDACTED]")
}

func (bridge *pluginDevBridge) handleDisconnect(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !requireLoopback(request) {
		writePluginDevJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	session := bridge.authenticateLease(request)
	if session == nil {
		writePluginDevJSON(response, http.StatusUnauthorized, map[string]string{"error": "lease unavailable"})
		return
	}
	bridge.disconnect(session.ID)
	writePluginDevJSON(response, http.StatusOK, map[string]bool{"disconnected": true})
}

func (bridge *pluginDevBridge) Pending() []pluginDevPairing {
	now := time.Now()
	bridge.mu.Lock()
	expired := bridge.pruneLocked(now)
	result := make([]pluginDevPairing, 0, len(bridge.pairings))
	for _, pairing := range bridge.pairings {
		if pairing.Status == "pending" {
			result = append(result, pairing.publicCopy())
		}
	}
	bridge.mu.Unlock()
	bridge.notifyExpired(expired)
	return result
}

func (bridge *pluginDevBridge) Sessions() []pluginDevSession {
	now := time.Now()
	bridge.mu.Lock()
	expired := bridge.pruneLocked(now)
	result := make([]pluginDevSession, 0, len(bridge.sessions))
	for _, session := range bridge.sessions {
		result = append(result, session.publicCopy())
	}
	bridge.mu.Unlock()
	bridge.notifyExpired(expired)
	return result
}

func (bridge *pluginDevBridge) Approve(pairingID string) error {
	bridge.mu.Lock()
	pairing := bridge.pairings[strings.TrimSpace(pairingID)]
	if pairing == nil || pairing.Status != "pending" || time.Now().After(pairing.ExpiresAt) {
		bridge.mu.Unlock()
		return errors.New("plugin development pairing is unavailable")
	}
	leaseToken, err := randomHex(32)
	if err != nil {
		bridge.mu.Unlock()
		return err
	}
	now := time.Now()
	session := &pluginDevSession{
		ID: pairing.ID, PluginID: pairing.PluginID, PluginName: pairing.PluginName,
		ProjectRoot: pairing.ProjectRoot, PackageRoot: pairing.PackageRoot, UIURL: pairing.UIURL,
		CLIInstanceID: pairing.CLIInstanceID, ConnectedAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(pluginDevLeaseTTL), Status: "connected", ProtocolVersion: pairing.ProtocolVersion,
		Capabilities: append([]string(nil), pairing.Capabilities...), Manifest: pairing.Manifest, leaseToken: leaseToken,
	}
	bridge.mu.Unlock()
	if bridge.onApproved != nil {
		if err := bridge.onApproved(session.publicCopy()); err != nil {
			return err
		}
	}
	bridge.mu.Lock()
	pairing = bridge.pairings[strings.TrimSpace(pairingID)]
	if pairing == nil || pairing.Status != "pending" || time.Now().After(pairing.ExpiresAt) {
		bridge.mu.Unlock()
		if bridge.onDisconnected != nil {
			bridge.onDisconnected(session.publicCopy())
		}
		return errors.New("plugin development pairing is unavailable")
	}
	pairing.Status = "approved"
	pairing.leaseToken = leaseToken
	bridge.sessions[session.ID] = session
	bridge.byLease[leaseToken] = session
	bridge.mu.Unlock()
	core.Emit(pluginDevChangedEvent, session.publicCopy())
	return nil
}

func (bridge *pluginDevBridge) Reject(pairingID string) error {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	pairing := bridge.pairings[strings.TrimSpace(pairingID)]
	if pairing == nil || pairing.Status != "pending" {
		return errors.New("plugin development pairing is unavailable")
	}
	pairing.Status = "rejected"
	go core.Emit(pluginDevChangedEvent, map[string]any{"id": pairing.ID, "status": "rejected"})
	return nil
}

func (bridge *pluginDevBridge) disconnect(sessionID string) {
	bridge.mu.Lock()
	session := bridge.sessions[strings.TrimSpace(sessionID)]
	if session != nil {
		delete(bridge.byLease, session.leaseToken)
		delete(bridge.sessions, session.ID)
	}
	bridge.mu.Unlock()
	if session != nil && bridge.onDisconnected != nil {
		bridge.onDisconnected(session.publicCopy())
	}
	core.Emit(pluginDevChangedEvent, map[string]any{"id": sessionID, "status": "disconnected"})
}

func (bridge *pluginDevBridge) pruneLocked(now time.Time) []pluginDevSession {
	bridge.prunePublishesLocked(now)
	for id, pairing := range bridge.pairings {
		if now.After(pairing.ExpiresAt) && pairing.Status != "approved" {
			delete(bridge.pairings, id)
		}
	}
	expired := make([]pluginDevSession, 0)
	for id, session := range bridge.sessions {
		if now.After(session.ExpiresAt) || now.Sub(session.LastSeenAt) > pluginDevHeartbeatTTL {
			expired = append(expired, session.publicCopy())
			delete(bridge.sessions, id)
			delete(bridge.byLease, session.leaseToken)
		}
	}
	return expired
}

func (bridge *pluginDevBridge) notifyExpired(sessions []pluginDevSession) {
	for _, session := range sessions {
		if bridge.onDisconnected != nil {
			bridge.onDisconnected(session)
		}
		core.Emit(pluginDevChangedEvent, map[string]any{"id": session.ID, "status": "disconnected"})
	}
}

func (pairing pluginDevPairing) publicCopy() pluginDevPairing {
	pairing.leaseToken = ""
	pairing.Capabilities = append([]string(nil), pairing.Capabilities...)
	pairing.Manifest.Platforms = append([]string(nil), pairing.Manifest.Platforms...)
	pairing.Manifest.KeymapIssues = append([]string(nil), pairing.Manifest.KeymapIssues...)
	return pairing
}

func (session pluginDevSession) publicCopy() pluginDevSession {
	session.leaseToken = ""
	session.Capabilities = append([]string(nil), session.Capabilities...)
	session.Manifest.Platforms = append([]string(nil), session.Manifest.Platforms...)
	session.Manifest.KeymapIssues = append([]string(nil), session.Manifest.KeymapIssues...)
	session.Logs = append([]pluginDevLog(nil), session.Logs...)
	return session
}
