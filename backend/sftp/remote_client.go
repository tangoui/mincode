package sftp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	ftp "github.com/jlaffaye/ftp"
	pkgsftp "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"mincode/app/backend/sshauth"
)

const (
	ftpDialTimeout = 20 * time.Second
	ftpIdleTimeout = 60 * time.Second
)

// remoteFile 是 SFTP 与 FTP 流式文件句柄的共同接口。
type remoteFile interface {
	io.Reader
	io.Writer
	io.Seeker
	io.Closer
}

// remoteClient 保持既有 SFTP 文件操作语义，并允许 FTP 适配器复用全部 IPC。
type remoteClient interface {
	ReadDir(string) ([]os.FileInfo, error)
	Stat(string) (os.FileInfo, error)
	Lstat(string) (os.FileInfo, error)
	Open(string) (remoteFile, error)
	Create(string) (remoteFile, error)
	OpenFile(string, int) (remoteFile, error)
	Mkdir(string) error
	Remove(string) error
	RemoveDirectory(string) error
	Rename(string, string) error
	RealPath(string) (string, error)
	Chmod(string, os.FileMode) error
	PosixRename(string, string) error
	CanChmod() bool
	Close() error
}

type sftpClientAdapter struct{ client *pkgsftp.Client }

func (c *sftpClientAdapter) ReadDir(p string) ([]os.FileInfo, error) { return c.client.ReadDir(p) }
func (c *sftpClientAdapter) Stat(p string) (os.FileInfo, error)      { return c.client.Stat(p) }
func (c *sftpClientAdapter) Lstat(p string) (os.FileInfo, error)     { return c.client.Lstat(p) }
func (c *sftpClientAdapter) Open(p string) (remoteFile, error)       { return c.client.Open(p) }
func (c *sftpClientAdapter) Create(p string) (remoteFile, error)     { return c.client.Create(p) }
func (c *sftpClientAdapter) OpenFile(p string, f int) (remoteFile, error) {
	return c.client.OpenFile(p, f)
}
func (c *sftpClientAdapter) Mkdir(p string) error                { return c.client.Mkdir(p) }
func (c *sftpClientAdapter) Remove(p string) error               { return c.client.Remove(p) }
func (c *sftpClientAdapter) RemoveDirectory(p string) error      { return c.client.RemoveDirectory(p) }
func (c *sftpClientAdapter) Rename(a, b string) error            { return c.client.Rename(a, b) }
func (c *sftpClientAdapter) RealPath(p string) (string, error)   { return c.client.RealPath(p) }
func (c *sftpClientAdapter) Chmod(p string, m os.FileMode) error { return c.client.Chmod(p, m) }
func (c *sftpClientAdapter) PosixRename(a, b string) error       { return c.client.PosixRename(a, b) }
func (c *sftpClientAdapter) CanChmod() bool                      { return true }
func (c *sftpClientAdapter) Close() error                        { return c.client.Close() }

type ftpClientAdapter struct {
	client *ftp.ServerConn
	conns  *ftpConnSet
}

func (c *ftpClientAdapter) ReadDir(p string) ([]os.FileInfo, error) {
	entries, err := c.client.List(p)
	if err != nil {
		return nil, ftpPathError("readdir", p, err)
	}
	infos := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		infos = append(infos, ftpFileInfo{entry: entry})
	}
	return infos, nil
}

func (c *ftpClientAdapter) Stat(p string) (os.FileInfo, error) {
	entry, err := c.client.GetEntry(p)
	if err == nil {
		return ftpFileInfo{entry: entry}, nil
	}
	clean := path.Clean(p)
	parent, name := path.Dir(clean), path.Base(clean)
	entries, listErr := c.client.List(parent)
	if listErr == nil {
		for _, candidate := range entries {
			if candidate.Name == name {
				return ftpFileInfo{entry: candidate}, nil
			}
		}
		return nil, &os.PathError{Op: "stat", Path: p, Err: os.ErrNotExist}
	}
	if size, sizeErr := c.client.FileSize(p); sizeErr == nil {
		return ftpFileInfo{entry: &ftp.Entry{Name: name, Type: ftp.EntryTypeFile, Size: uint64(size)}}, nil
	}
	return nil, ftpPathError("stat", p, err)
}

func (c *ftpClientAdapter) Lstat(p string) (os.FileInfo, error) { return c.Stat(p) }
func (c *ftpClientAdapter) Open(p string) (remoteFile, error) {
	if strings.TrimSpace(p) == "" || strings.ContainsAny(p, "\r\n\x00") {
		return nil, fmt.Errorf("FTP 文件路径无效")
	}
	return &ftpRemoteFile{client: c.client, path: p, readable: true}, nil
}
func (c *ftpClientAdapter) Create(p string) (remoteFile, error) {
	return c.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
}
func (c *ftpClientAdapter) OpenFile(p string, flags int) (remoteFile, error) {
	if strings.TrimSpace(p) == "" || strings.ContainsAny(p, "\r\n\x00") {
		return nil, fmt.Errorf("FTP 文件路径无效")
	}
	if flags&os.O_EXCL != 0 {
		if _, err := c.Stat(p); err == nil {
			return nil, &os.PathError{Op: "open", Path: p, Err: os.ErrExist}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return &ftpRemoteFile{client: c.client, path: p, writable: true}, nil
}
func (c *ftpClientAdapter) Mkdir(p string) error {
	return ftpPathError("mkdir", p, c.client.MakeDir(p))
}
func (c *ftpClientAdapter) Remove(p string) error {
	return ftpPathError("remove", p, c.client.Delete(p))
}
func (c *ftpClientAdapter) RemoveDirectory(p string) error {
	return ftpPathError("rmdir", p, c.client.RemoveDir(p))
}
func (c *ftpClientAdapter) Rename(a, b string) error {
	if err := c.client.Rename(a, b); err != nil {
		return ftpPathError("rename", a, err)
	}
	return nil
}
func (c *ftpClientAdapter) RealPath(p string) (string, error) {
	if p == "." || strings.TrimSpace(p) == "" {
		return c.client.CurrentDir()
	}
	if strings.HasPrefix(p, "/") {
		return path.Clean(p), nil
	}
	cwd, err := c.client.CurrentDir()
	if err != nil {
		return "", err
	}
	return path.Join(cwd, p), nil
}
func (c *ftpClientAdapter) Chmod(string, os.FileMode) error {
	return errors.ErrUnsupported
}
func (c *ftpClientAdapter) PosixRename(a, b string) error {
	if err := c.Rename(a, b); err == nil {
		return nil
	}
	if _, err := c.Stat(b); err != nil {
		return c.Rename(a, b)
	}
	if err := c.Remove(b); err != nil {
		return err
	}
	return c.Rename(a, b)
}
func (c *ftpClientAdapter) CanChmod() bool { return false }
func (c *ftpClientAdapter) abort()         { c.conns.closeAll() }
func (c *ftpClientAdapter) Close() error {
	c.conns.setDeadline(time.Now())
	done := make(chan error, 1)
	go func() { done <- c.client.Quit() }()
	select {
	case err := <-done:
		c.conns.closeAll()
		return err
	case <-time.After(2 * time.Second):
		c.conns.closeAll()
		return fmt.Errorf("关闭 FTP 会话超时")
	}
}

type ftpFileInfo struct{ entry *ftp.Entry }

func (i ftpFileInfo) Name() string       { return i.entry.Name }
func (i ftpFileInfo) Size() int64        { return int64(i.entry.Size) }
func (i ftpFileInfo) ModTime() time.Time { return i.entry.Time }
func (i ftpFileInfo) IsDir() bool        { return i.entry.Type == ftp.EntryTypeFolder }
func (i ftpFileInfo) Sys() any           { return nil }
func (i ftpFileInfo) Mode() os.FileMode {
	switch i.entry.Type {
	case ftp.EntryTypeFolder:
		return os.ModeDir
	case ftp.EntryTypeLink:
		return os.ModeSymlink
	default:
		return 0
	}
}

type ftpRemoteFile struct {
	client   *ftp.ServerConn
	path     string
	readable bool
	writable bool
	offset   int64
	response *ftp.Response
	pipeW    *io.PipeWriter
	done     chan error
	closed   bool
}

func (f *ftpRemoteFile) startRead() error {
	if f.response != nil {
		return nil
	}
	response, err := f.client.RetrFrom(f.path, uint64(f.offset))
	if err != nil {
		return ftpPathError("read", f.path, err)
	}
	_ = response.SetDeadline(time.Now().Add(ftpIdleTimeout))
	f.response = response
	return nil
}
func (f *ftpRemoteFile) startWrite() {
	if f.done != nil {
		return
	}
	reader, writer := io.Pipe()
	f.pipeW = writer
	f.done = make(chan error, 1)
	go func() {
		err := f.client.StorFrom(f.path, reader, uint64(f.offset))
		_ = reader.CloseWithError(err)
		f.done <- err
		close(f.done)
	}()
}
func (f *ftpRemoteFile) Read(p []byte) (int, error) {
	if !f.readable || f.closed {
		return 0, os.ErrInvalid
	}
	if err := f.startRead(); err != nil {
		return 0, err
	}
	n, err := f.response.Read(p)
	f.offset += int64(n)
	return n, err
}
func (f *ftpRemoteFile) Write(p []byte) (int, error) {
	if !f.writable || f.closed {
		return 0, os.ErrInvalid
	}
	f.startWrite()
	n, err := f.pipeW.Write(p)
	f.offset += int64(n)
	return n, err
}
func (f *ftpRemoteFile) Seek(offset int64, whence int) (int64, error) {
	if f.response != nil || f.done != nil {
		return f.offset, fmt.Errorf("FTP 流开始后不支持重新定位")
	}
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.offset + offset
	default:
		return f.offset, errors.ErrUnsupported
	}
	if next < 0 {
		return f.offset, os.ErrInvalid
	}
	f.offset = next
	return next, nil
}
func (f *ftpRemoteFile) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	if f.response != nil {
		return f.response.Close()
	}
	if f.writable {
		f.startWrite()
		closeErr := f.pipeW.Close()
		select {
		case serverErr := <-f.done:
			return errors.Join(closeErr, serverErr)
		case <-time.After(ftpIdleTimeout):
			_ = f.pipeW.CloseWithError(context.DeadlineExceeded)
			return errors.Join(closeErr, fmt.Errorf("等待 FTP 上传完成超时"))
		}
	}
	return nil
}

func ftpPathError(op, p string, err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "550") || strings.Contains(message, "not found") || strings.Contains(message, "no such") {
		err = os.ErrNotExist
	}
	return &os.PathError{Op: op, Path: p, Err: err}
}

type ftpConnSet struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func (s *ftpConnSet) add(conn net.Conn) net.Conn {
	s.mu.Lock()
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
	return &ftpTrackedConn{Conn: conn, set: s}
}
func (s *ftpConnSet) remove(conn net.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}
func (s *ftpConnSet) setDeadline(deadline time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.conns {
		_ = conn.SetDeadline(deadline)
	}
}
func (s *ftpConnSet) closeAll() {
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

type ftpTrackedConn struct {
	net.Conn
	set       *ftpConnSet
	closeOnce sync.Once
}

func (c *ftpTrackedConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(ftpIdleTimeout))
	return c.Conn.Read(p)
}
func (c *ftpTrackedConn) Write(p []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(ftpIdleTimeout))
	return c.Conn.Write(p)
}
func (c *ftpTrackedConn) Handshake() error {
	if tlsConn, ok := c.Conn.(*tls.Conn); ok {
		_ = tlsConn.SetDeadline(time.Now().Add(ftpDialTimeout))
		return tlsConn.Handshake()
	}
	return nil
}
func (c *ftpTrackedConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.set.remove(c.Conn)
		err = c.Conn.Close()
	})
	return err
}

// ftpTCPAddrConn 满足 jlaffaye/ftp 对控制连接 RemoteAddr 的 TCP 类型断言。
type ftpTCPAddrConn struct {
	net.Conn
	remote *net.TCPAddr
}

func (c *ftpTCPAddrConn) RemoteAddr() net.Addr { return c.remote }

type ftpDialParams struct {
	protocol      string
	host          string
	port          int
	username      string
	password      string
	tlsServerName string
	tlsConfig     *tls.Config
	socksProxy    *sshauth.SocksProxyConfig
	bastion       *ssh.Client
}

func dialFTP(p ftpDialParams) (*ftpClientAdapter, error) {
	host := strings.TrimSpace(p.host)
	user := strings.TrimSpace(p.username)
	if host == "" || strings.ContainsAny(host, "\r\n\x00") {
		return nil, fmt.Errorf("FTP host 不能为空或无效")
	}
	if user == "" || strings.ContainsAny(user, "\r\n\x00") {
		return nil, fmt.Errorf("FTP username 不能为空或无效")
	}
	port := p.port
	if port <= 0 {
		port = 21
	}
	if port > 65535 {
		return nil, fmt.Errorf("FTP port 无效")
	}
	if p.protocol != "ftp" && p.protocol != "ftps-explicit" {
		return nil, fmt.Errorf("不支持的远程协议: %s", p.protocol)
	}

	ctx, cancel := context.WithTimeout(context.Background(), ftpDialTimeout)
	defer cancel()
	conns := &ftpConnSet{conns: make(map[net.Conn]struct{})}
	controlAddr := net.JoinHostPort(host, strconv.Itoa(port))
	var dialMu sync.Mutex
	controlPending := true
	tlsConfig := p.tlsConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		tlsConfig = tlsConfig.Clone()
		if tlsConfig.MinVersion < tls.VersionTLS12 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	if tlsConfig.ServerName = strings.TrimSpace(p.tlsServerName); tlsConfig.ServerName == "" {
		tlsConfig.ServerName = host
	}
	dialFn := func(network, address string) (net.Conn, error) {
		dialMu.Lock()
		isControl := controlPending
		if isControl {
			controlPending = false
		}
		dialMu.Unlock()
		_, requestedPort, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("FTP 数据地址无效: %w", err)
		}
		target := controlAddr
		if !isControl {
			target = net.JoinHostPort(host, requestedPort)
		}
		dialCtx, dialCancel := context.WithTimeout(context.Background(), ftpDialTimeout)
		defer dialCancel()
		var conn net.Conn
		if p.bastion != nil {
			conn, err = p.bastion.DialContext(dialCtx, "tcp", target)
		} else if p.socksProxy != nil {
			conn, err = sshauth.DialThroughSocks(*p.socksProxy, target, ftpDialTimeout)
		} else {
			conn, err = (&net.Dialer{Timeout: ftpDialTimeout}).DialContext(dialCtx, network, target)
		}
		if err != nil {
			return nil, err
		}
		if !isControl && p.protocol == "ftps-explicit" {
			// 数据连接须在 RETR/STOR 命令之后才握手，避免等待服务器 150 回复时互相阻塞。
			conn = tls.Client(conn, tlsConfig)
		}
		tracked := conns.add(conn)
		if isControl {
			return &ftpTCPAddrConn{Conn: tracked, remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}}, nil
		}
		return tracked, nil
	}

	options := []ftp.DialOption{
		ftp.DialWithContext(ctx),
		ftp.DialWithTimeout(ftpDialTimeout),
		ftp.DialWithShutTimeout(ftpDialTimeout),
		ftp.DialWithDialFunc(dialFn),
		ftp.DialWithTrustPasvIP(false),
	}
	if p.protocol == "ftps-explicit" {
		options = append(options, ftp.DialWithExplicitTLS(tlsConfig))
	}
	server, err := ftp.Dial(controlAddr, options...)
	if err != nil {
		conns.closeAll()
		return nil, fmt.Errorf("FTP 连接失败: %w", err)
	}
	if err := server.Login(user, p.password); err != nil {
		conns.setDeadline(time.Now())
		_ = server.Quit()
		conns.closeAll()
		return nil, fmt.Errorf("FTP 登录失败: %w", err)
	}
	return &ftpClientAdapter{client: server, conns: conns}, nil
}
