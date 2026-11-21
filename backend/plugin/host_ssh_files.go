package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"

	"mincode/app/backend/backup"
	backendsftp "mincode/app/backend/sftp"
	"mincode/app/plugins/api"
)

// Restricted SSH file access for backups (capability backup; design 4.7).
// A plugin picks one of the user's saved SSH connections by ID and asks the
// user, in a host prompt that shows the connection name, host, user and
// directory, to let it use ONE directory there. Afterwards it can only put,
// list, get and delete plain files directly inside that directory by name;
// it never passes a path and never sees a host, username or credential.
// The grant is bound to the connection's host, port and username as shown
// in the prompt: editing any of them in the saved connection revokes it.
// File names must start with "mincode-backup-", and the root or the home
// directory itself cannot be authorized.
//
// Methods (all behind backupMethod, so refused while the plugin's restore runs):
//   - host.ssh.connections {} → [{id, name, eligible, reason?, directory?}]
//   - host.ssh.files.authorize {connectionId, directory, timeoutMs?} → {directory}
//   - host.ssh.files.list {connectionId, name?} → [{name, size, modTime}]
//   - host.ssh.files.put {connectionId, name, token} → {name, size}
//     (the staged file is streamed to <dir>/<name>.part, then renamed)
//   - host.ssh.files.get {connectionId, name} → {token, size, sha256}
//   - host.ssh.files.delete {connectionId, name} → true
//   - host.ssh.files.putText {connectionId, name, text} / getText → {text}
//
// Errors carry data.code: notAuthorized, notEligible (+reason), notFound,
// hostKey, auth, network, tooLarge, invalidName, declined, permission,
// remote, notText. Messages never include the host or credentials.
//
// "Allow" without "always" lasts for the plugin's process generation;
// "always" is stored as sshfiles/<pluginId> →
// {connectionId: {directory, host, port, username}}; grants of the older
// {connectionId: directory} shape are ignored (the user is asked again).
// Uninstall and Clear Plugin Data remove both.

const (
	sshFilesPromptKind   = "sshFiles"
	sshFilesTextMaxBytes = 64 << 10
	sshFilesCallTimeout  = 10 * time.Minute
	sshFilesDirMaxBytes  = 1024
	sshFilesIDMaxBytes   = 256
	sshFilesPartSuffix   = ".part"
	sshFilesNamePrefix   = "mincode-backup-"
)

var sshFilesNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Seams for tests.
var (
	sshFilesConnections = backendsftp.ListSavedConnections
	sshFilesConnection  = backendsftp.SavedConnectionByID
	sshFilesOpen        = backendsftp.OpenSavedSFTP
)

func init() {
	registerHostMethod("host.ssh.connections", nil, api.CapabilityBackup, backupMethod(hostSSHConnections))
	registerHostMethod("host.ssh.files.authorize", nil, api.CapabilityBackup, backupMethod(hostSSHFilesAuthorize))
	registerHostMethod("host.ssh.files.list", nil, api.CapabilityBackup, backupMethod(hostSSHFilesList))
	registerHostMethod("host.ssh.files.put", nil, api.CapabilityBackup, backupMethod(hostSSHFilesPut))
	registerHostMethod("host.ssh.files.get", nil, api.CapabilityBackup, backupMethod(hostSSHFilesGet))
	registerHostMethod("host.ssh.files.delete", nil, api.CapabilityBackup, backupMethod(hostSSHFilesDelete))
	registerHostMethod("host.ssh.files.putText", nil, api.CapabilityBackup, backupMethod(hostSSHFilesPutText))
	registerHostMethod("host.ssh.files.getText", nil, api.CapabilityBackup, backupMethod(hostSSHFilesGetText))
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, _ removalReason) {
		clearSSHFilesGrants(pluginID)
	})
}

// sshFilesDetails describes the request in the approval prompt.
type sshFilesDetails struct {
	ConnectionID   string `json:"connectionId"`
	ConnectionName string `json:"connectionName"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	Directory      string `json:"directory"`
}

// ---------------------------------------------------------------------------
// Grants.

// sshFilesGrantsKVKey contains "/", which plugin IDs never do.
func sshFilesGrantsKVKey(pluginID string) string { return "sshfiles/" + pluginID }

// sshFilesGrant is one authorized directory and the connection endpoint the
// user saw when allowing it.
type sshFilesGrant struct {
	Directory string `json:"directory"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
}

func newSSHFilesGrant(connection backendsftp.SavedConnection, directory string) sshFilesGrant {
	return sshFilesGrant{Directory: directory, Host: connection.Host, Port: connection.Port, Username: connection.Username}
}

// matches reports whether the grant still describes connection.
func (grant sshFilesGrant) matches(connection backendsftp.SavedConnection) bool {
	return grant.Directory != "" && grant.Host == connection.Host && grant.Port == connection.Port && grant.Username == connection.Username
}

type sshFilesRunGrant struct {
	generation uint64
	grant      sshFilesGrant
}

var (
	sshFilesGrantsMu sync.Mutex
	sshFilesRun      = map[string]map[string]sshFilesRunGrant{}
)

// readSSHFilesGrants returns the remembered grants; entries of any other
// shape (such as the older connectionId → directory string) are dropped.
func readSSHFilesGrants(pluginID string) map[string]sshFilesGrant {
	grants := map[string]sshFilesGrant{}
	raw, found, err := pluginKVGetRaw(hostPluginID, sshFilesGrantsKVKey(pluginID))
	var stored map[string]json.RawMessage
	if err != nil || !found || json.Unmarshal(raw, &stored) != nil {
		return grants
	}
	for connectionID, value := range stored {
		var grant sshFilesGrant
		if json.Unmarshal(value, &grant) == nil && grant.Directory != "" && grant.Host != "" {
			grants[connectionID] = grant
		}
	}
	return grants
}

func writeSSHFilesGrants(pluginID string, grants map[string]sshFilesGrant) error {
	if len(grants) == 0 {
		return pluginKVDeleteRaw(hostPluginID, sshFilesGrantsKVKey(pluginID))
	}
	raw, _ := json.Marshal(grants)
	return pluginKVSetRaw(hostPluginID, sshFilesGrantsKVKey(pluginID), raw)
}

func clearSSHFilesGrants(pluginID string) {
	sshFilesGrantsMu.Lock()
	defer sshFilesGrantsMu.Unlock()
	delete(sshFilesRun, pluginID)
	_ = pluginKVDeleteRaw(hostPluginID, sshFilesGrantsKVKey(pluginID))
}

// sshFilesDirectory is the directory the plugin may use on the connection:
// this run's grant first (the latest decision), then the remembered one. A
// grant made for another host, port or username does not count.
func sshFilesDirectory(runtimeHost *processRuntime, connection backendsftp.SavedConnection) (string, bool) {
	generation := runtimeGeneration(runtimeHost)
	sshFilesGrantsMu.Lock()
	defer sshFilesGrantsMu.Unlock()
	if run, ok := sshFilesRun[runtimeHost.pluginID][connection.ID]; ok && run.generation == generation {
		if run.grant.matches(connection) {
			return run.grant.Directory, true
		}
		return "", false
	}
	grant, ok := readSSHFilesGrants(runtimeHost.pluginID)[connection.ID]
	if !ok || !grant.matches(connection) {
		return "", false
	}
	return grant.Directory, true
}

func grantSSHFiles(runtimeHost *processRuntime, connection backendsftp.SavedConnection, directory string, always bool) error {
	connectionID := connection.ID
	grant := newSSHFilesGrant(connection, directory)
	generation := runtimeGeneration(runtimeHost)
	sshFilesGrantsMu.Lock()
	defer sshFilesGrantsMu.Unlock()
	if !always {
		grants := sshFilesRun[runtimeHost.pluginID]
		if grants == nil {
			grants = map[string]sshFilesRunGrant{}
			sshFilesRun[runtimeHost.pluginID] = grants
		}
		grants[connectionID] = sshFilesRunGrant{generation: generation, grant: grant}
		return nil
	}
	delete(sshFilesRun[runtimeHost.pluginID], connectionID)
	grants := readSSHFilesGrants(runtimeHost.pluginID)
	grants[connectionID] = grant
	return writeSSHFilesGrants(runtimeHost.pluginID, grants)
}

// ---------------------------------------------------------------------------
// Errors and validation.

func sshFilesError(rpcCode int, code, message string) *rpcError {
	return &rpcError{Code: rpcCode, Message: message, Data: map[string]string{"code": code}}
}

func sshFilesNotAuthorized() *rpcError {
	return sshFilesError(-32003, "notAuthorized", "the user has not authorized a directory on this connection")
}

func sshFilesInvalidName() *rpcError {
	return sshFilesError(-32602, "invalidName", "name must start with \"mincode-backup-\", be at most 128 of A-Z a-z 0-9 . _ - and not end with \".part\"")
}

// sshFilesConnectionError maps saved-connection errors to fixed codes.
func sshFilesConnectionError(err error) *rpcError {
	var saved *backendsftp.SavedConnectionError
	if !errors.As(err, &saved) {
		return sshFilesError(-32000, "network", "could not connect to the SSH server")
	}
	switch saved.Code {
	case backendsftp.SavedNotFound:
		return sshFilesError(-32002, "notFound", "the saved SSH connection does not exist")
	case backendsftp.SavedNotEligible:
		return &rpcError{Code: -32000, Message: "this connection cannot be used unattended",
			Data: map[string]string{"code": "notEligible", "reason": saved.Reason}}
	case backendsftp.SavedHostKey:
		return sshFilesError(-32000, "hostKey", "the server's host key is not the one recorded in known_hosts")
	case backendsftp.SavedAuth:
		return sshFilesError(-32000, "auth", "the SSH server rejected the saved credentials")
	default:
		return sshFilesError(-32000, "network", "could not connect to the SSH server")
	}
}

// sshFilesRemoteError maps a failed SFTP operation.
func sshFilesRemoteError(ctx context.Context, err error) *rpcError {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return sshFilesError(-32002, "notFound", "the file does not exist")
	case errors.Is(err, os.ErrPermission):
		return sshFilesError(-32000, "permission", "the SSH server denied access")
	case errors.Is(err, backup.ErrBackupTooLarge):
		return sshFilesError(-32000, "tooLarge", "the file exceeds the 256 MiB limit")
	case ctx.Err() != nil:
		return sshFilesError(-32000, "network", "the SSH operation timed out or was cancelled")
	case errors.Is(err, sftp.ErrSSHFxConnectionLost), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return sshFilesError(-32000, "network", "the SSH connection was lost")
	default:
		return sshFilesError(-32000, "remote", "the SSH server reported an error")
	}
}

// validSSHFilesName accepts only the backup plugin's own file names, so a
// grant never reaches other files in the directory.
func validSSHFilesName(name string) bool {
	return sshFilesNamePattern.MatchString(name) && strings.HasPrefix(name, sshFilesNamePrefix) && len(name) > len(sshFilesNamePrefix) &&
		!strings.HasSuffix(name, sshFilesPartSuffix)
}

func validSSHFilesConnectionID(id string) bool {
	return id != "" && len(id) <= sshFilesIDMaxBytes && !strings.ContainsFunc(id, unsafeTextRune)
}

// normalizeSSHFilesDirectory accepts an absolute POSIX path or a ~-relative
// path ("~/..."); ".." segments, NUL and control characters are refused, and
// so are the root and the home directory itself ("/", "~", "~/"): a grant
// covers one dedicated directory. The result is path.Clean'ed.
func normalizeSSHFilesDirectory(raw string) (string, bool) {
	if raw == "" || len(raw) > sshFilesDirMaxBytes || !utf8.ValidString(raw) || strings.ContainsFunc(raw, unsafeTextRune) {
		return "", false
	}
	if !strings.HasPrefix(raw, "/") && raw != "~" && !strings.HasPrefix(raw, "~/") {
		return "", false
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return "", false
		}
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		rest := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(raw, "~")), "/")
		if rest == "" {
			return "", false
		}
		return "~/" + rest, true
	}
	if cleaned := path.Clean(raw); cleaned != "/" {
		return cleaned, true
	}
	return "", false
}

// resolveSSHFilesDirectory turns a stored directory into an absolute remote
// path; "~" is the SFTP session's working directory (the user's home).
func resolveSSHFilesDirectory(client *sftp.Client, directory string) (string, error) {
	if !strings.HasPrefix(directory, "~") {
		return directory, nil
	}
	home, err := client.Getwd()
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(home, "/") {
		return "", fmt.Errorf("remote working directory is not absolute")
	}
	return path.Join(home, strings.TrimPrefix(directory, "~")), nil
}

// withSSHFiles dials the connection for one call (no pooling), bounded by
// sshFilesCallTimeout and the plugin's run context, and closes it on return.
func withSSHFiles(runtimeHost *processRuntime, connectionID, directory string,
	run func(ctx context.Context, client *sftp.Client, dir string) (any, *rpcError)) (any, *rpcError) {
	ctx, cancel := context.WithTimeout(runtimeHost.promptContext(), sshFilesCallTimeout)
	defer cancel()
	client, closeClient, err := sshFilesOpen(ctx, connectionID)
	if err != nil {
		return nil, sshFilesConnectionError(err)
	}
	defer closeClient()
	dir, err := resolveSSHFilesDirectory(client, directory)
	if err != nil {
		return nil, sshFilesRemoteError(ctx, err)
	}
	return run(ctx, client, dir)
}

// sshFilesCall checks the connection ID, the name (unless nameOptional and
// empty) and the grant, then runs the operation on the authorized directory.
func sshFilesCall(runtimeHost *processRuntime, connectionID, name string, nameOptional bool,
	run func(ctx context.Context, client *sftp.Client, dir string) (any, *rpcError)) (any, *rpcError) {
	if !validSSHFilesConnectionID(connectionID) {
		return nil, hostUIParamsError("connectionId is required")
	}
	if !(nameOptional && name == "") && !validSSHFilesName(name) {
		return nil, sshFilesInvalidName()
	}
	connection, err := sshFilesConnection(connectionID)
	if err != nil {
		var saved *backendsftp.SavedConnectionError
		if errors.As(err, &saved) && saved.Code == backendsftp.SavedNotFound {
			return nil, sshFilesNotAuthorized()
		}
		return nil, sshFilesConnectionError(err)
	}
	directory, ok := sshFilesDirectory(runtimeHost, connection)
	if !ok {
		return nil, sshFilesNotAuthorized()
	}
	return withSSHFiles(runtimeHost, connectionID, directory, run)
}

// sshFilesTarget is the request of the name-only methods.
type sshFilesTarget struct {
	ConnectionID string `json:"connectionId"`
	Name         string `json:"name"`
}

// regularSSHFile stats name without following a symlink and requires a
// regular file.
func regularSSHFile(ctx context.Context, client *sftp.Client, remote string) (os.FileInfo, *rpcError) {
	info, err := client.Lstat(remote)
	if err != nil {
		return nil, sshFilesRemoteError(ctx, err)
	}
	if !info.Mode().IsRegular() {
		return nil, sshFilesError(-32002, "notFound", "the file does not exist")
	}
	return info, nil
}

// ---------------------------------------------------------------------------
// Methods.

func hostSSHConnections(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct{}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	connections, err := sshFilesConnections()
	if err != nil {
		return nil, sshFilesError(-32000, "unavailable", "the saved SSH connections could not be read")
	}
	out := make([]map[string]any, 0, len(connections))
	for _, connection := range connections {
		item := map[string]any{"id": connection.ID, "name": cleanHostText(connection.Name, hostUITitleMaxRunes, false), "eligible": connection.Eligible}
		if connection.Reason != "" {
			item["reason"] = connection.Reason
		}
		if directory, ok := sshFilesDirectory(runtimeHost, connection); ok {
			item["directory"] = directory
		}
		out = append(out, item)
	}
	return out, nil
}

func hostSSHFilesAuthorize(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		ConnectionID string `json:"connectionId"`
		Directory    string `json:"directory"`
		TimeoutMs    int    `json:"timeoutMs"`
	}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	if !validSSHFilesConnectionID(request.ConnectionID) {
		return nil, hostUIParamsError("connectionId is required")
	}
	directory, ok := normalizeSSHFilesDirectory(request.Directory)
	if !ok {
		return nil, hostUIParamsError("directory must be an absolute path or start with ~/, without .., and not be / or ~ itself")
	}
	approvalTimeout, rpcErr := promptTimeout(request.TimeoutMs)
	if rpcErr != nil {
		return nil, rpcErr
	}
	connection, err := sshFilesConnection(request.ConnectionID)
	if err != nil {
		return nil, sshFilesConnectionError(err)
	}
	if !connection.Eligible {
		return nil, sshFilesConnectionError(&backendsftp.SavedConnectionError{Code: backendsftp.SavedNotEligible, Reason: connection.Reason})
	}
	if granted, ok := sshFilesDirectory(runtimeHost, connection); ok && granted == directory {
		return map[string]any{"directory": directory}, nil
	}
	name := cleanHostText(connection.Name, hostUITitleMaxRunes, false)
	items := []quickPickItem{{ID: "once", Label: "once"}, {ID: "always", Label: "always"}}
	event := promptEvent{Kind: sshFilesPromptKind, PluginID: runtimeHost.pluginID, PluginName: runtimeHost.displayName(),
		Title: name, Items: items,
		SSHFiles: &sshFilesDetails{ConnectionID: connection.ID, ConnectionName: name,
			Host: cleanHostText(connection.Host, hostUITitleMaxRunes, false), Port: connection.Port,
			Username: cleanHostText(connection.Username, hostUITitleMaxRunes, false), Directory: directory}}
	reply, rpcErr := hostPrompts.ask(runtimeHost.promptContext(), event,
		&pendingPrompt{pluginID: runtimeHost.pluginID, kind: sshFilesPromptKind, items: map[string]bool{"once": true, "always": true}}, approvalTimeout)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if reply.Cancelled {
		return nil, sshFilesError(-32003, "declined", "the user declined access to the directory")
	}
	// Check the connection and create the directory before saving the grant.
	if _, rpcErr := withSSHFiles(runtimeHost, connection.ID, directory, func(ctx context.Context, client *sftp.Client, dir string) (any, *rpcError) {
		if err := client.MkdirAll(dir); err != nil {
			return nil, sshFilesRemoteError(ctx, err)
		}
		return nil, nil
	}); rpcErr != nil {
		return nil, rpcErr
	}
	always := len(reply.IDs) == 1 && reply.IDs[0] == "always"
	if err := grantSSHFiles(runtimeHost, connection, directory, always); err != nil {
		return nil, sshFilesError(-32000, "unavailable", "could not save the authorization")
	}
	return map[string]any{"directory": directory}, nil
}

func hostSSHFilesList(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request sshFilesTarget
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	return sshFilesCall(runtimeHost, request.ConnectionID, request.Name, true, func(ctx context.Context, client *sftp.Client, dir string) (any, *rpcError) {
		entries, err := client.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			return []map[string]any{}, nil
		}
		if err != nil {
			return nil, sshFilesRemoteError(ctx, err)
		}
		out := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			name := entry.Name()
			if !entry.Mode().IsRegular() || !validSSHFilesName(name) || (request.Name != "" && name != request.Name) {
				continue
			}
			out = append(out, map[string]any{"name": name, "size": entry.Size(), "modTime": entry.ModTime().UnixMilli()})
		}
		return out, nil
	})
}

// writeSSHFilesAtomic writes <dir>/<name>.part from source, then renames it
// over <dir>/<name> (posix-rename; plain rename after removing the target
// when the server lacks the extension).
func writeSSHFilesAtomic(ctx context.Context, client *sftp.Client, dir, name string, source io.Reader) (int64, *rpcError) {
	final := path.Join(dir, name)
	part := final + sshFilesPartSuffix
	// A leftover .part (or a symlink planted there) is removed, never followed.
	if err := client.Remove(part); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, sshFilesRemoteError(ctx, err)
	}
	file, err := client.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return 0, sshFilesRemoteError(ctx, err)
	}
	written, err := io.Copy(file, source)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = client.Remove(part)
		return 0, sshFilesRemoteError(ctx, err)
	}
	if err := client.PosixRename(part, final); err != nil {
		var status *sftp.StatusError
		if !errors.As(err, &status) || status.FxCode() != sftp.ErrSSHFxOpUnsupported {
			_ = client.Remove(part)
			return 0, sshFilesRemoteError(ctx, err)
		}
		if removeErr := client.Remove(final); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			_ = client.Remove(part)
			return 0, sshFilesRemoteError(ctx, removeErr)
		}
		if err := client.Rename(part, final); err != nil {
			_ = client.Remove(part)
			return 0, sshFilesRemoteError(ctx, err)
		}
	}
	return written, nil
}

func hostSSHFilesPut(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		ConnectionID string `json:"connectionId"`
		Name         string `json:"name"`
		Token        string `json:"token"`
	}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	if !validBackupToken(request.Token) {
		return nil, hostUIParamsError("token is required")
	}
	return sshFilesCall(runtimeHost, request.ConnectionID, request.Name, false, func(ctx context.Context, client *sftp.Client, dir string) (any, *rpcError) {
		staging := backupStaging()
		staging.Sweep()
		local, err := staging.Path(runtimeHost.pluginID, request.Token)
		if err != nil {
			return nil, sshFilesError(-32002, "notFound", "the staged file does not exist or has expired")
		}
		source, err := os.Open(local)
		if err != nil {
			return nil, sshFilesError(-32002, "notFound", "the staged file does not exist or has expired")
		}
		defer source.Close()
		size, rpcErr := writeSSHFilesAtomic(ctx, client, dir, request.Name, source)
		if rpcErr != nil {
			return nil, rpcErr
		}
		return map[string]any{"name": request.Name, "size": size}, nil
	})
}

func hostSSHFilesGet(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request sshFilesTarget
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	return sshFilesCall(runtimeHost, request.ConnectionID, request.Name, false, func(ctx context.Context, client *sftp.Client, dir string) (any, *rpcError) {
		remote := path.Join(dir, request.Name)
		info, rpcErr := regularSSHFile(ctx, client, remote)
		if rpcErr != nil {
			return nil, rpcErr
		}
		if info.Size() > backup.MaxBackupBytes {
			return nil, sshFilesError(-32000, "tooLarge", "the file exceeds the 256 MiB limit")
		}
		file, err := client.Open(remote)
		if err != nil {
			return nil, sshFilesRemoteError(ctx, err)
		}
		defer file.Close()
		staging := backupStaging()
		staging.Sweep()
		// Create caps the size at MaxBackupBytes and hashes while writing.
		token, size, sha, err := staging.Create(runtimeHost.pluginID, func(w io.Writer) error {
			_, err := io.Copy(w, file)
			return err
		})
		if err != nil {
			return nil, sshFilesRemoteError(ctx, err)
		}
		return map[string]any{"token": token, "size": size, "sha256": sha}, nil
	})
}

func hostSSHFilesDelete(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request sshFilesTarget
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	return sshFilesCall(runtimeHost, request.ConnectionID, request.Name, false, func(ctx context.Context, client *sftp.Client, dir string) (any, *rpcError) {
		remote := path.Join(dir, request.Name)
		if _, rpcErr := regularSSHFile(ctx, client, remote); rpcErr != nil {
			return nil, rpcErr
		}
		if err := client.Remove(remote); err != nil {
			return nil, sshFilesRemoteError(ctx, err)
		}
		return true, nil
	})
}

func hostSSHFilesPutText(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		ConnectionID string  `json:"connectionId"`
		Name         string  `json:"name"`
		Text         *string `json:"text"`
	}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	if request.Text == nil || !utf8.ValidString(*request.Text) {
		return nil, hostUIParamsError("text is required and must be UTF-8")
	}
	if len(*request.Text) > sshFilesTextMaxBytes {
		return nil, sshFilesError(-32000, "tooLarge", "text exceeds 64 KiB")
	}
	return sshFilesCall(runtimeHost, request.ConnectionID, request.Name, false, func(ctx context.Context, client *sftp.Client, dir string) (any, *rpcError) {
		size, rpcErr := writeSSHFilesAtomic(ctx, client, dir, request.Name, strings.NewReader(*request.Text))
		if rpcErr != nil {
			return nil, rpcErr
		}
		return map[string]any{"name": request.Name, "size": size}, nil
	})
}

func hostSSHFilesGetText(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request sshFilesTarget
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	return sshFilesCall(runtimeHost, request.ConnectionID, request.Name, false, func(ctx context.Context, client *sftp.Client, dir string) (any, *rpcError) {
		remote := path.Join(dir, request.Name)
		info, rpcErr := regularSSHFile(ctx, client, remote)
		if rpcErr != nil {
			return nil, rpcErr
		}
		if info.Size() > sshFilesTextMaxBytes {
			return nil, sshFilesError(-32000, "tooLarge", "the file exceeds 64 KiB")
		}
		file, err := client.Open(remote)
		if err != nil {
			return nil, sshFilesRemoteError(ctx, err)
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, sshFilesTextMaxBytes+1))
		if err != nil {
			return nil, sshFilesRemoteError(ctx, err)
		}
		if len(data) > sshFilesTextMaxBytes {
			return nil, sshFilesError(-32000, "tooLarge", "the file exceeds 64 KiB")
		}
		if !utf8.Valid(data) {
			return nil, sshFilesError(-32000, "notText", "the file is not UTF-8 text")
		}
		return map[string]any{"text": string(data)}, nil
	})
}
