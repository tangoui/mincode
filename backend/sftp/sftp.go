// SFTP 文件管理器 —— 工程管理「部署到远程」功能的 Go 后端实现。
//
// 命令集与 Tauri 侧（src-tauri/src/terminal/sftp.rs）保持完全一致，
// 前端无需任何改动即可通过 platform 抽象层调用：
//
//	sftp_connect          建立独立 SSH 连接并开启 SFTP 子系统（幂等）
//	sftp_disconnect       关闭会话
//	sftp_list_dir         列目录（目录在前、名称字母序，rwx 权限字符串）
//	sftp_mkdir            新建单级远程目录
//	sftp_mkdirs           递归创建目录（跳过已存在层级）
//	sftp_upload_file      本地文件流式上传到远程
//	sftp_upload_bytes     字节数组上传到远程（适合小文件，前端 base64 传入）
//	sftp_download         远程文件或目录递归下载到本地（含唯一命名、敏感路径防护）
//	sftp_delete           删除远程文件或目录
//	sftp_rename           重命名/移动远程条目
//	sftp_read_text_file   读取远程文本文件（UTF-8，最大 4 MB）
//	sftp_read_image_preview 读取远程图片预览（PNG/JPEG/GIF/WebP，最大 4 MiB）
//	sftp_write_text_file  文本内容覆盖写入远程文件
//	sftp_get_home         获取远程 HOME（canonicalize "."）
//
// 会话状态用 sync.Mutex + map 管理，等价 Rust 侧 Arc<Mutex<HashMap>>。
package sftp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"mincode/app/backend/core"
	appcrypto "mincode/app/backend/crypto"
)

// sftpState 持有一个远程客户端及其底层隧道连接（后者需保活）。
type sftpState struct {
	mu        sync.RWMutex
	client    remoteClient
	closer    io.Closer
	exclusive bool
}

func (s *sftpState) close() {
	if s.exclusive {
		if aborter, ok := s.client.(interface{ abort() }); ok {
			aborter.abort()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		_ = s.client.Close()
		s.client = nil
	}
	if s.closer != nil {
		_ = s.closer.Close()
		s.closer = nil
	}
}

var (
	sessionsMu        sync.Mutex
	sessions          = map[string]*sftpState{}
	sessionGeneration = map[string]uint64{}
)

func init() {
	registerSftpTransferCommands()
	core.Register("sftp_connect", handleSftpConnect)
	core.Register("sftp_disconnect", handleSftpDisconnect)
	core.Register("sftp_list_dir", handleSftpListDir)
	core.Register("sftp_mkdir", handleSftpMkdir)
	core.Register("sftp_mkdirs", handleSftpMkdirs)
	core.Register("sftp_upload_file", handleSftpUploadFile)
	core.Register("sftp_upload_bytes", handleSftpUploadBytes)
	core.Register("sftp_download", handleSftpDownload)
	core.Register("sftp_delete", handleSftpDelete)
	core.Register("sftp_rename", handleSftpRename)
	core.Register("sftp_read_text_file", handleSftpReadTextFile)
	core.Register("sftp_read_image_preview", handleSftpReadImagePreview)
	core.Register("sftp_write_text_file", handleSftpWriteTextFile)
	core.Register("sftp_get_home", handleSftpGetHome)
}

// fileEntry 为返回给前端的目录条目（camelCase JSON，对齐 Rust FileEntry）。
type fileEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	IsDir       bool   `json:"isDir"`
	Size        uint64 `json:"size"`
	Modified    *int64 `json:"modified"`
	Permissions string `json:"permissions"`
}

func getSession(sessionID string) (*sftpState, error) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	st, ok := sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("远程文件会话不存在，请重新打开文件面板连接")
	}
	return st, nil
}

func acquireSession(sessionID string) (*sftpState, func(), error) {
	st, err := getSession(sessionID)
	if err != nil {
		return nil, nil, err
	}
	if st.exclusive {
		st.mu.Lock()
		if st.client == nil {
			st.mu.Unlock()
			return nil, nil, fmt.Errorf("远程会话已断开，请重新连接")
		}
		return st, st.mu.Unlock, nil
	}
	st.mu.RLock()
	if st.client == nil {
		st.mu.RUnlock()
		return nil, nil, fmt.Errorf("远程会话已断开，请重新连接")
	}
	return st, st.mu.RUnlock, nil
}

func disconnectAll() {
	cancelAllSftpTransfers()
	abortSessionFiles("")
	sessionsMu.Lock()
	for id := range sessionGeneration {
		sessionGeneration[id]++
	}
	states := make([]*sftpState, 0, len(sessions))
	for id, st := range sessions {
		states = append(states, st)
		delete(sessions, id)
	}
	sessionsMu.Unlock()
	for _, st := range states {
		st.close()
	}
}

// Shutdown 关闭全部 SFTP 传输和会话；可重复调用。
func Shutdown() { disconnectAll() }

// ---------- sftp_connect ----------

type sftpConnectPayload struct {
	SessionID       string                 `json:"sessionId"`
	Protocol        string                 `json:"protocol,omitempty"`
	Host            string                 `json:"host"`
	Port            int                    `json:"port"`
	Username        string                 `json:"username"`
	AuthType        string                 `json:"authType"`
	Password        *string                `json:"password"`
	FTPPassword     *string                `json:"ftpPassword,omitempty"`
	TLSServerName   string                 `json:"tlsServerName,omitempty"`
	KeyFile         *string                `json:"keyFile"`
	Passphrase      *string                `json:"passphrase"`
	AlgorithmPolicy string                 `json:"algorithmPolicy"`
	SocksProxy      *sftpSocksProxyPayload `json:"socksProxy,omitempty"`
	Bastion         *sftpConnectPayload    `json:"bastion,omitempty"`
}

// sftpSocksProxyPayload 为可选 SOCKS5 出站代理配置，字段与终端 SSH 一致。
type sftpSocksProxyPayload struct {
	Host     string  `json:"host"`
	Port     uint16  `json:"port"`
	Username *string `json:"username"`
	Password *string `json:"password"`
}

func handleSftpConnect(_ string, argsJson string) (string, error) {
	// 前端以 { payload: {...} } 包裹传入，兼容直接传入的情况。
	var wrap struct {
		Payload *sftpConnectPayload `json:"payload"`
	}
	if err := core.DecodeArgs(argsJson, &wrap); err != nil {
		return "", err
	}
	p := wrap.Payload
	if p == nil {
		// 回退：尝试将顶层直接解析为 payload。
		var direct sftpConnectPayload
		if err := json.Unmarshal([]byte(argsJson), &direct); err != nil {
			return "", err
		}
		p = &direct
	}

	sessionID := strings.TrimSpace(p.SessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session_id 不能为空")
	}

	// 幂等：已存在则跳过。
	sessionsMu.Lock()
	_, exists := sessions[sessionID]
	generation := sessionGeneration[sessionID]
	sessionsMu.Unlock()
	if exists {
		return "", nil
	}

	remote, closer, exclusive, err := connectRemoteClient(p)
	if err != nil {
		return "", err
	}

	sessionsMu.Lock()
	// 二次检查，防止并发连接时重复插入。
	if _, dup := sessions[sessionID]; dup || sessionGeneration[sessionID] != generation {
		sessionsMu.Unlock()
		_ = remote.Close()
		if closer != nil {
			_ = closer.Close()
		}
		return "", nil
	}
	sessions[sessionID] = &sftpState{client: remote, closer: closer, exclusive: exclusive}
	sessionsMu.Unlock()

	return "", nil
}

func connectRemoteClient(p *sftpConnectPayload) (remoteClient, io.Closer, bool, error) {
	protocol := strings.ToLower(strings.TrimSpace(p.Protocol))
	if protocol == "" {
		protocol = "sftp"
	}
	decPassword, err := appcrypto.DecryptOptional(p.Password)
	if err != nil {
		return nil, nil, false, fmt.Errorf("解密密码失败: %w", err)
	}
	valOr := func(value *string) string {
		if value == nil {
			return ""
		}
		return *value
	}
	socksProxy, err := buildSftpSocksConfig(p.SocksProxy)
	if err != nil {
		return nil, nil, false, err
	}
	if protocol == "sftp" {
		if p.Bastion != nil {
			return nil, nil, false, fmt.Errorf("SFTP 协议不支持 bastion 字段")
		}
		sshClient, dialErr := dialSSH(sshConnectParams{
			host: p.Host, port: p.Port, username: p.Username, authType: p.AuthType,
			password: valOr(decPassword), keyFile: valOr(p.KeyFile), passphrase: valOr(p.Passphrase),
			algorithmPolicy: p.AlgorithmPolicy, socksProxy: socksProxy,
		})
		if dialErr != nil {
			return nil, nil, false, dialErr
		}
		sftpClient, sftpErr := sftp.NewClient(sshClient)
		if sftpErr != nil {
			_ = sshClient.Close()
			return nil, nil, false, fmt.Errorf("初始化 SFTP 会话失败: %w", sftpErr)
		}
		return &sftpClientAdapter{client: sftpClient}, sshClient, false, nil
	}
	if protocol != "ftp" && protocol != "ftps-explicit" {
		return nil, nil, false, fmt.Errorf("不支持的远程协议: %s", protocol)
	}
	ftpPassword, err := appcrypto.DecryptOptional(p.FTPPassword)
	if err != nil {
		return nil, nil, false, fmt.Errorf("解密 FTP 密码失败: %w", err)
	}
	if p.FTPPassword == nil {
		return nil, nil, false, fmt.Errorf("FTP 密码不能为空")
	}
	password := valOr(ftpPassword)
	var bastionClient io.Closer
	var bastionSSH *ssh.Client
	if p.Bastion != nil {
		b := p.Bastion
		bProtocol := strings.ToLower(strings.TrimSpace(b.Protocol))
		if bProtocol != "" && bProtocol != "sftp" {
			return nil, nil, false, fmt.Errorf("bastion 必须使用 SSH 配置")
		}
		if b.Bastion != nil {
			return nil, nil, false, fmt.Errorf("不支持嵌套 bastion")
		}
		bPassword, decryptErr := appcrypto.DecryptOptional(b.Password)
		if decryptErr != nil {
			return nil, nil, false, fmt.Errorf("解密堡垒机密码失败: %w", decryptErr)
		}
		bSocks, socksErr := buildSftpSocksConfig(b.SocksProxy)
		if socksErr != nil {
			return nil, nil, false, socksErr
		}
		bastionSSH, err = dialSSH(sshConnectParams{
			host: b.Host, port: b.Port, username: b.Username, authType: b.AuthType,
			password: valOr(bPassword), keyFile: valOr(b.KeyFile), passphrase: valOr(b.Passphrase),
			algorithmPolicy: b.AlgorithmPolicy, socksProxy: bSocks,
		})
		if err != nil {
			return nil, nil, false, fmt.Errorf("连接堡垒机失败: %w", err)
		}
		bastionClient = bastionSSH
	}
	ftpClient, err := dialFTP(ftpDialParams{
		protocol: protocol, host: p.Host, port: p.Port, username: p.Username,
		password: password, tlsServerName: p.TLSServerName, socksProxy: socksProxy, bastion: bastionSSH,
	})
	if err != nil {
		if bastionClient != nil {
			_ = bastionClient.Close()
		}
		return nil, nil, false, err
	}
	return ftpClient, bastionClient, true, nil
}

// ---------- sftp_disconnect ----------

func handleSftpDisconnect(_ string, argsJson string) (string, error) {
	var a struct {
		SessionID string `json:"sessionId"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	cancelSftpTransfersForSession(a.SessionID)
	abortSessionFiles(a.SessionID)
	sessionsMu.Lock()
	sessionGeneration[a.SessionID]++
	st, ok := sessions[a.SessionID]
	if ok {
		delete(sessions, a.SessionID)
	}
	sessionsMu.Unlock()
	if ok && st != nil {
		st.close()
	}
	return "", nil
}

// ---------- sftp_list_dir ----------

func handleSftpListDir(_ string, argsJson string) (string, error) {
	var a struct {
		SessionID string `json:"sessionId"`
		Path      string `json:"path"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	st, release, err := acquireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	defer release()

	infos, err := st.client.ReadDir(a.Path)
	if err != nil {
		return "", fmt.Errorf("读取目录失败: %w", err)
	}

	entries := make([]fileEntry, 0, len(infos))
	for _, info := range infos {
		name := info.Name()
		if name == "." || name == ".." {
			continue
		}
		var fullPath string
		if strings.HasSuffix(a.Path, "/") {
			fullPath = a.Path + name
		} else {
			fullPath = a.Path + "/" + name
		}
		isDir := info.IsDir()
		var modified *int64
		if mt := info.ModTime(); !mt.IsZero() {
			sec := mt.Unix()
			modified = &sec
		}
		entries = append(entries, fileEntry{
			Name:        name,
			Path:        fullPath,
			IsDir:       isDir,
			Size:        uint64(info.Size()),
			Modified:    modified,
			Permissions: formatPermissions(info.Mode()),
		})
	}

	// 目录在前、文件在后；同类按名称（小写）字母序。
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	return core.EncodeResult(entries)
}

// formatPermissions 将列表项的模式转为 ls 风格字符串（对齐 Rust format_permissions）：
// 首字符 l 为符号链接、d 为目录、- 为其他，后接 rwx 权限位。列表来自 ReadDir（SFTP 不跟随
// 链接；FTP 由 LIST 行类型给出），所以符号链接保留 ModeSymlink，前端据此识别链接。
func formatPermissions(fileMode os.FileMode) string {
	mode := uint32(fileMode.Perm())
	var b strings.Builder
	b.Grow(10)
	switch {
	case fileMode&os.ModeSymlink != 0:
		b.WriteByte('l')
	case fileMode.IsDir():
		b.WriteByte('d')
	default:
		b.WriteByte('-')
	}
	bits := []struct {
		bit uint32
		ch  byte
	}{
		{0o400, 'r'}, {0o200, 'w'}, {0o100, 'x'},
		{0o040, 'r'}, {0o020, 'w'}, {0o010, 'x'},
		{0o004, 'r'}, {0o002, 'w'}, {0o001, 'x'},
	}
	for _, e := range bits {
		if mode&e.bit != 0 {
			b.WriteByte(e.ch)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// ---------- sftp_mkdirs ----------

func handleSftpMkdirs(_ string, argsJson string) (string, error) {
	var a struct {
		SessionID string `json:"sessionId"`
		Path      string `json:"path"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(a.Path)
	if trimmed == "" || trimmed == "/" {
		return "", nil
	}
	st, release, err := acquireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	defer release()

	absolute := strings.HasPrefix(trimmed, "/")
	current := ""
	if absolute {
		current = "/"
	}
	for _, part := range strings.Split(trimmed, "/") {
		if part == "" {
			continue
		}
		if current == "" || current == "/" {
			if absolute {
				current = "/" + part
			} else {
				current = part
			}
		} else {
			current = current + "/" + part
		}
		if _, statErr := st.client.Stat(current); statErr == nil {
			continue
		}
		if mkErr := st.client.Mkdir(current); mkErr != nil {
			// 并发或已存在时 Stat 可能漏判，再次确认存在则忽略。
			if _, s2 := st.client.Stat(current); s2 == nil {
				continue
			}
			return "", fmt.Errorf("创建目录失败 %s: %w", current, mkErr)
		}
	}
	return "", nil
}

// ---------- sftp_upload_file ----------

func handleSftpUploadFile(_ string, argsJson string) (string, error) {
	var a struct {
		SessionID  string `json:"sessionId"`
		LocalPath  string `json:"localPath"`
		RemotePath string `json:"remotePath"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	localPath := strings.TrimSpace(a.LocalPath)
	if localPath == "" {
		return "", fmt.Errorf("本地文件路径为空")
	}
	if err := rejectSensitiveLocalReadPath(localPath); err != nil {
		return "", err
	}
	st, release, err := acquireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	defer release()

	localFile, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer localFile.Close()

	remoteFile, err := st.client.Create(a.RemotePath)
	if err != nil {
		return "", fmt.Errorf("创建远程文件失败: %w", err)
	}
	defer remoteFile.Close()

	buf := make([]byte, 65536)
	for {
		n, readErr := localFile.Read(buf)
		if n > 0 {
			if _, wErr := remoteFile.Write(buf[:n]); wErr != nil {
				return "", fmt.Errorf("写入远程文件失败: %w", wErr)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("读取本地文件失败: %w", readErr)
		}
	}
	if err := remoteFile.Close(); err != nil {
		return "", fmt.Errorf("完成远程文件上传失败: %w", err)
	}
	return "", nil
}

// ---------- sftp_get_home ----------

func handleSftpGetHome(_ string, argsJson string) (string, error) {
	var a struct {
		SessionID string `json:"sessionId"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	st, release, err := acquireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	defer release()
	home, err := st.client.RealPath(".")
	if err != nil || strings.TrimSpace(home) == "" {
		home = "/"
	}
	return core.EncodeResult(home)
}

// ---------- sftp_mkdir ----------

func handleSftpMkdir(_ string, argsJson string) (string, error) {
	var a struct {
		SessionID string `json:"sessionId"`
		Path      string `json:"path"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	st, release, err := acquireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	defer release()
	if err := st.client.Mkdir(a.Path); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}
	return "", nil
}

// ---------- sftp_upload_bytes ----------

func handleSftpUploadBytes(_ string, argsJson string) (string, error) {
	var a struct {
		SessionID  string          `json:"sessionId"`
		RemotePath string          `json:"remotePath"`
		Bytes      json.RawMessage `json:"bytes"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	data, err := decodeBytesField(a.Bytes)
	if err != nil {
		return "", err
	}
	st, release, err := acquireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	defer release()

	remoteFile, err := st.client.Create(a.RemotePath)
	if err != nil {
		return "", fmt.Errorf("创建远程文件失败: %w", err)
	}
	defer remoteFile.Close()
	if _, err := remoteFile.Write(data); err != nil {
		return "", fmt.Errorf("写入远程文件失败: %w", err)
	}
	if err := remoteFile.Close(); err != nil {
		return "", fmt.Errorf("完成远程文件上传失败: %w", err)
	}
	return "", nil
}

// decodeBytesField 兼容两种 IPC 编码：base64 字符串（Go json.Marshal([]byte) 默认）
// 或 number 数组（Tauri IPC 中 Vec<u8> 序列化为 [1,2,3]）。
func decodeBytesField(raw json.RawMessage) ([]byte, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	// base64 字符串形式
	if strings.HasPrefix(trimmed, "\"") {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("解析文件字节失败: %w", err)
		}
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("解析文件字节失败: %w", err)
		}
		return decoded, nil
	}
	// number 数组形式
	var nums []int
	if err := json.Unmarshal(raw, &nums); err != nil {
		return nil, fmt.Errorf("解析文件字节失败: %w", err)
	}
	out := make([]byte, len(nums))
	for i, n := range nums {
		out[i] = byte(n)
	}
	return out, nil
}

// ---------- sftp_download ----------

func handleSftpDownload(_ string, argsJson string) (string, error) {
	var a struct {
		SessionID  string  `json:"sessionId"`
		RemotePath string  `json:"remotePath"`
		LocalPath  *string `json:"localPath"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	remotePath := strings.TrimSpace(a.RemotePath)
	if remotePath == "" {
		return "", fmt.Errorf("远程路径为空")
	}

	st, release, err := acquireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	defer release()

	remoteInfo, err := st.client.Stat(remotePath)
	if err != nil {
		return "", fmt.Errorf("读取远程条目失败: %w", err)
	}

	entryName := path.Base(path.Clean(remotePath))
	if entryName == "" || entryName == "." || entryName == "/" {
		entryName = "download"
	}

	localPath, err := resolveDownloadTarget(a.LocalPath, entryName)
	if err != nil {
		return "", err
	}
	if remoteInfo.IsDir() {
		if _, err := os.Lstat(localPath); err == nil {
			return "", fmt.Errorf("目标目录已存在: %s", localPath)
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("检查目标目录失败: %w", err)
		}
		if err := os.Mkdir(localPath, 0o755); err != nil {
			return "", fmt.Errorf("创建本地目录失败: %w", err)
		}
		completed := false
		defer func() {
			if !completed {
				_ = os.RemoveAll(localPath)
			}
		}()
		if err := downloadRemoteDirectory(st.client, remotePath, localPath); err != nil {
			return "", err
		}
		completed = true
	} else if err := downloadRemoteFile(st.client, remotePath, localPath); err != nil {
		return "", err
	}
	return core.EncodeResult(localPath)
}

func resolveDownloadTarget(requested *string, entryName string) (string, error) {
	if requested != nil {
		localPath := strings.TrimSpace(*requested)
		if localPath == "" {
			return "", fmt.Errorf("保存路径为空")
		}
		if err := rejectSensitiveLocalPath(localPath); err != nil {
			return "", err
		}
		if parent := filepath.Dir(localPath); parent != "" && parent != "." {
			if err := os.MkdirAll(parent, 0o755); err != nil {
				return "", fmt.Errorf("创建目标目录失败: %w", err)
			}
		}
		return localPath, nil
	}

	if !isSafeLocalEntryName(entryName) {
		return "", fmt.Errorf("远程条目名称不能作为本地文件名: %q", entryName)
	}
	localDir, err := downloadDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return "", fmt.Errorf("创建下载目录失败: %w", err)
	}
	return uniqueDownloadPath(localDir, entryName), nil
}

func downloadRemoteDirectory(client remoteClient, remoteDir, localDir string) error {
	entries, err := client.ReadDir(remoteDir)
	if err != nil {
		return fmt.Errorf("读取远程目录失败 %s: %w", remoteDir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !isSafeLocalEntryName(name) {
			return fmt.Errorf("远程目录包含非法条目名称: %q", name)
		}
		remoteChild := path.Join(remoteDir, name)
		localChild := filepath.Join(localDir, name)
		if entry.IsDir() {
			if err := os.Mkdir(localChild, 0o755); err != nil {
				return fmt.Errorf("创建本地目录失败 %s: %w", localChild, err)
			}
			if err := downloadRemoteDirectory(client, remoteChild, localChild); err != nil {
				return err
			}
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("不支持下载远程符号链接: %s", remoteChild)
		}
		if err := downloadRemoteFile(client, remoteChild, localChild); err != nil {
			return err
		}
	}
	return nil
}

func downloadRemoteFile(client remoteClient, remotePath, localPath string) error {
	remoteFile, err := client.Open(remotePath)
	if err != nil {
		return fmt.Errorf("打开远程文件失败 %s: %w", remotePath, err)
	}
	defer remoteFile.Close()

	localFile, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("创建本地文件失败 %s: %w", localPath, err)
	}
	if _, err := io.Copy(localFile, remoteFile); err != nil {
		_ = localFile.Close()
		_ = os.Remove(localPath)
		return fmt.Errorf("下载文件失败 %s: %w", remotePath, err)
	}
	if err := remoteFile.Close(); err != nil {
		_ = localFile.Close()
		_ = os.Remove(localPath)
		return fmt.Errorf("完成远程下载失败 %s: %w", remotePath, err)
	}
	if err := localFile.Close(); err != nil {
		_ = os.Remove(localPath)
		return fmt.Errorf("保存本地文件失败 %s: %w", localPath, err)
	}
	return nil
}

// removeRemoteDirAll recursively removes all children of dirPath then the
// directory itself, equivalent to `rm -rf` on the remote side.
func removeRemoteDirAll(client remoteClient, dirPath string) error {
	entries, err := client.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("读取目录失败 %s: %w", dirPath, err)
	}
	for _, entry := range entries {
		child := path.Join(dirPath, entry.Name())
		if entry.IsDir() {
			if err := removeRemoteDirAll(client, child); err != nil {
				return err
			}
		} else {
			if err := client.Remove(child); err != nil {
				return fmt.Errorf("删除文件失败 %s: %w", child, err)
			}
		}
	}
	if err := client.RemoveDirectory(dirPath); err != nil {
		return fmt.Errorf("删除目录失败 %s: %w", dirPath, err)
	}
	return nil
}

// ---------- sftp_delete ----------

func handleSftpDelete(_ string, argsJson string) (string, error) {
	var a struct {
		SessionID string `json:"sessionId"`
		Path      string `json:"path"`
		IsDir     bool   `json:"isDir"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	st, release, err := acquireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	defer release()
	if a.IsDir {
		if err := removeRemoteDirAll(st.client, a.Path); err != nil {
			return "", err
		}
	} else {
		if err := st.client.Remove(a.Path); err != nil {
			return "", fmt.Errorf("删除文件失败: %w", err)
		}
	}
	return "", nil
}

// ---------- sftp_rename ----------

func handleSftpRename(_ string, argsJson string) (string, error) {
	var a struct {
		SessionID string `json:"sessionId"`
		OldPath   string `json:"oldPath"`
		NewPath   string `json:"newPath"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	st, release, err := acquireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	defer release()
	if err := st.client.Rename(a.OldPath, a.NewPath); err != nil {
		return "", fmt.Errorf("重命名失败: %w", err)
	}
	return "", nil
}

// ---------- sftp_read_text_file ----------

func handleSftpReadTextFile(_ string, argsJson string) (string, error) {
	var a struct {
		SessionID string `json:"sessionId"`
		Path      string `json:"path"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	st, release, err := acquireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	defer release()

	remoteFile, err := st.client.Open(a.Path)
	if err != nil {
		return "", fmt.Errorf("打开远程文件失败: %w", err)
	}
	defer remoteFile.Close()

	const maxSize = 4 * 1024 * 1024
	limited := io.LimitReader(remoteFile, maxSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("读取远程文件失败: %w", err)
	}
	if len(data) > maxSize {
		return "", fmt.Errorf("文件超过 4 MB，请直接下载")
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("文件不是有效的 UTF-8 文本")
	}
	return core.EncodeResult(string(data))
}

// ---------- sftp_write_text_file ----------

func handleSftpWriteTextFile(_ string, argsJson string) (string, error) {
	var a struct {
		SessionID string `json:"sessionId"`
		Path      string `json:"path"`
		Content   string `json:"content"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	st, release, err := acquireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	defer release()

	if len(a.Content) > 4*1024*1024 {
		return "", fmt.Errorf("文件超过 4 MB，请使用上传功能")
	}
	remotePath := strings.TrimSpace(a.Path)
	if remotePath == "" || strings.ContainsRune(remotePath, 0) {
		return "", fmt.Errorf("远程文件路径无效")
	}
	var existingMode os.FileMode
	if existing, statErr := st.client.Lstat(remotePath); statErr == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return "", fmt.Errorf("拒绝覆盖远程非普通文件")
		}
		existingMode = existing.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("检查远程文件失败: %w", statErr)
	}
	tempPath := path.Join(path.Dir(remotePath), fmt.Sprintf(".%s.mincode-%d", path.Base(remotePath), time.Now().UnixNano()))
	remoteFile, err := st.client.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return "", fmt.Errorf("创建远程临时文件失败: %w", err)
	}
	completed := false
	defer func() {
		_ = remoteFile.Close()
		if !completed {
			_ = st.client.Remove(tempPath)
		}
	}()
	if _, err := remoteFile.Write([]byte(a.Content)); err != nil {
		return "", fmt.Errorf("写入远程临时文件失败: %w", err)
	}
	if err := remoteFile.Close(); err != nil {
		return "", fmt.Errorf("保存远程临时文件失败: %w", err)
	}
	if existingMode != 0 && st.client.CanChmod() {
		if err := st.client.Chmod(tempPath, existingMode); err != nil {
			return "", fmt.Errorf("保留远程文件权限失败: %w", err)
		}
	}
	if err := st.client.PosixRename(tempPath, remotePath); err != nil {
		if renameErr := st.client.Rename(tempPath, remotePath); renameErr != nil {
			return "", fmt.Errorf("原子替换远程文件失败: %w", err)
		}
	}
	completed = true
	return "", nil
}

// ---------- 本地路径安全辅助 ----------

// downloadDir 返回本地默认下载目录（优先 ~/Downloads）。
func downloadDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("无法确定本地下载目录")
	}
	return filepath.Join(home, "Downloads"), nil
}

// isSafeLocalEntryName 判断远程条目名称能否作为本地目录下的单层文件名。
// path.Base 只识别 '/'；filepath.Base / filepath.IsLocal 按本机语义额外拒绝
// Windows 上的 '\\' 分隔、盘符（如 "C:x"）、':' 及 NUL 等保留设备名。
func isSafeLocalEntryName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, 0) {
		return false
	}
	if path.Base(name) != name || filepath.Base(name) != name {
		return false
	}
	return filepath.IsLocal(name)
}

// uniqueDownloadPath 生成不冲突的本地下载路径（同名则加 _2, _3 ...）。
func uniqueDownloadPath(dir, fileName string) string {
	path := filepath.Join(dir, fileName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(fileName)
	stem := strings.TrimSuffix(fileName, ext)
	for i := 2; i < 10000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s_%d%s", stem, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return path
}

// rejectSensitiveLocalPath 拒绝将 SFTP 下载目标写入敏感目录或符号链接，
// 防止借 sftp_download 改写 ~/.ssh/authorized_keys、~/.bashrc 等。对齐 Rust reject_sensitive_local_path。
func rejectSensitiveLocalPath(target string) error {
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("拒绝写入符号链接")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	sensitiveRoots := []string{
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".config", "gh"),
	}
	sensitiveFiles := []string{
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".profile"),
		filepath.Join(home, ".npmrc"),
		filepath.Join(home, ".netrc"),
		filepath.Join(home, ".gitconfig"),
	}
	parent := filepath.Dir(target)
	parentCanon := canonicalOr(parent)
	for _, root := range sensitiveRoots {
		rootCanon := canonicalOr(root)
		if pathStartsWith(parentCanon, rootCanon) {
			return fmt.Errorf("拒绝写入敏感目录: %s", root)
		}
	}
	for _, f := range sensitiveFiles {
		if target == f {
			return fmt.Errorf("拒绝覆盖敏感文件: %s", f)
		}
	}
	return nil
}

// rejectSensitiveLocalReadPath 拒绝从敏感目录/符号链接读取本地文件，
// 防止 sftp_upload_* 把 ~/.ssh/id_rsa 之类外泄。对齐 Rust reject_sensitive_local_read_path。
func rejectSensitiveLocalReadPath(target string) error {
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("拒绝读取符号链接")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	sensitiveRoots := []string{
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".config", "gh"),
		filepath.Join(home, ".docker"),
		filepath.Join(home, ".kube"),
	}
	sensitiveFiles := []string{
		filepath.Join(home, ".npmrc"),
		filepath.Join(home, ".netrc"),
		filepath.Join(home, ".gitconfig"),
	}
	check := canonicalOr(target)
	for _, root := range sensitiveRoots {
		rootCanon := canonicalOr(root)
		if pathStartsWith(check, rootCanon) {
			return fmt.Errorf("拒绝读取敏感目录: %s", root)
		}
	}
	for _, f := range sensitiveFiles {
		if check == canonicalOr(f) {
			return fmt.Errorf("拒绝读取敏感文件: %s", f)
		}
	}
	return nil
}

// canonicalOr 尽力解析为绝对/软链接展开后的路径，失败则回退原值。
func canonicalOr(p string) string {
	if p == "" {
		return p
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// pathStartsWith 判断 child 是否位于 root 之下（按路径分隔符边界比较）。
func pathStartsWith(child, root string) bool {
	if root == "" || child == "" {
		return false
	}
	if child == root {
		return true
	}
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
