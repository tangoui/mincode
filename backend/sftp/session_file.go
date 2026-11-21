package sftp

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"sync"
)

// OpenSessionFile 让宿主的其它模块（插件服务）借用用户已经打开的远程文件会话读取
// 一个普通文件，而不必自己建立连接或接触凭据。错误是稳定的哨兵值，英文消息只描述
// 原因，不含主机、端口、本地或远程路径；调用方按哨兵映射为自己的错误码，界面文案由
// 前端本地化。

var (
	// ErrSessionUnavailable：会话不存在、已断开或正在断开。
	ErrSessionUnavailable = errors.New("remote session unavailable")
	// ErrRemotePathInvalid：路径不是远程绝对路径。
	ErrRemotePathInvalid = errors.New("invalid remote path")
	// ErrRemoteFileNotFound：远程条目不存在或无法读取属性。
	ErrRemoteFileNotFound = errors.New("remote file not found")
	// ErrRemoteNotRegular：目标是目录、符号链接或其它特殊文件。
	ErrRemoteNotRegular = errors.New("remote entry is not a regular file")
	// ErrRemoteReadFailed：打开或读取远程文件失败。
	ErrRemoteReadFailed = errors.New("remote read failed")
)

// sessionFile 是 OpenSessionFile 返回的读取句柄。Read 与 Close 互斥：底层 SFTP/FTP
// 文件句柄都不支持并发读与关闭。会话断开时 abort 让之后的 Read 立即失败，
// 读取方因此尽快归还会话锁，断开流程不会一直等待。
type sessionFile struct {
	sessionID string
	mu        sync.Mutex
	file      remoteFile
	aborted   bool // 受 openSessionFiles.mu 保护
	closed    bool // 受 mu 保护
	release   sync.Once
	unlock    func()
}

var openSessionFiles = struct {
	mu    sync.Mutex
	files map[*sessionFile]struct{}
}{files: map[*sessionFile]struct{}{}}

type openedSessionFile struct {
	reader  io.ReadCloser
	size    int64
	release func()
	err     error
}

// OpenSessionFileContext 与 OpenSessionFile 相同，但 ctx 结束时立即返回 ctx.Err()：
// 等待会话锁（FTP 会话独占，传输期间一直被占用）以及 Lstat / Open 的网络往返都在
// 单独的 goroutine 里进行；被放弃的打开在完成后立刻 release，不会留下被占用的会话。
func OpenSessionFileContext(ctx context.Context, sessionID, remotePath string) (io.ReadCloser, int64, func(), error) {
	if !validSessionFilePath(remotePath) {
		return nil, 0, nil, ErrRemotePathInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, nil, err
	}
	done := make(chan openedSessionFile, 1)
	go func() {
		reader, size, release, err := OpenSessionFile(sessionID, remotePath)
		done <- openedSessionFile{reader, size, release, err}
	}()
	select {
	case opened := <-done:
		return opened.reader, opened.size, opened.release, opened.err
	case <-ctx.Done():
		go func() {
			if opened := <-done; opened.err == nil {
				opened.release()
			}
		}()
		return nil, 0, nil, ctx.Err()
	}
}

// OpenSessionFile 用 sessionID 对应的已打开会话 Lstat remotePath：只接受普通文件
// （拒绝目录与符号链接，与下载一致），然后打开它。返回读取句柄、Lstat 得到的大小和
// release。大小是 Lstat 时的值：读取期间文件继续增长时，调用方按这个大小截断。
// release 关闭文件并归还会话（幂等）；在它之前会话保持占用（FTP 会话是独占的，期间
// 浏览会等待）。release 会等待正在进行的 Read 返回，所以放弃一个可能卡住的读取时应
// 在单独的 goroutine 里调用它。会话忙时本函数会一直等待，需要可取消时用
// OpenSessionFileContext。
func OpenSessionFile(sessionID, remotePath string) (io.ReadCloser, int64, func(), error) {
	if !validSessionFilePath(remotePath) {
		return nil, 0, nil, ErrRemotePathInvalid
	}
	state, unlock, err := acquireSession(sessionID)
	if err != nil {
		return nil, 0, nil, ErrSessionUnavailable
	}
	info, err := state.client.Lstat(remotePath)
	if err != nil {
		unlock()
		return nil, 0, nil, ErrRemoteFileNotFound
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		unlock()
		return nil, 0, nil, ErrRemoteNotRegular
	}
	file, err := state.client.Open(remotePath)
	if err != nil {
		unlock()
		return nil, 0, nil, ErrRemoteReadFailed
	}
	handle := &sessionFile{sessionID: sessionID, file: file, unlock: unlock}
	openSessionFiles.mu.Lock()
	openSessionFiles.files[handle] = struct{}{}
	openSessionFiles.mu.Unlock()
	return handle, info.Size(), handle.finish, nil
}

func validSessionFilePath(remotePath string) bool {
	if remotePath == "" || strings.ContainsAny(remotePath, "\x00\r\n") || !strings.HasPrefix(remotePath, "/") {
		return false
	}
	base := path.Base(path.Clean(remotePath))
	return base != "/" && base != "." && base != ".."
}

func (f *sessionFile) isAborted() bool {
	openSessionFiles.mu.Lock()
	defer openSessionFiles.mu.Unlock()
	return f.aborted
}

func (f *sessionFile) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	if f.isAborted() {
		return 0, ErrSessionUnavailable
	}
	n, err := f.file.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		if f.isAborted() {
			return n, ErrSessionUnavailable
		}
		return n, ErrRemoteReadFailed
	}
	return n, err
}

// Close 只关闭文件；会话由 release 归还。
func (f *sessionFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if err := f.file.Close(); err != nil {
		return ErrRemoteReadFailed
	}
	return nil
}

func (f *sessionFile) finish() {
	f.release.Do(func() {
		_ = f.Close()
		openSessionFiles.mu.Lock()
		delete(openSessionFiles.files, f)
		openSessionFiles.mu.Unlock()
		f.unlock()
	})
}

// abortSessionFiles 让会话（sessionID 为空时为全部会话）上打开的文件之后的读取
// 立即失败，读取方随即 release，断开流程得以拿到会话锁。
func abortSessionFiles(sessionID string) {
	openSessionFiles.mu.Lock()
	defer openSessionFiles.mu.Unlock()
	for file := range openSessionFiles.files {
		if sessionID == "" || file.sessionID == sessionID {
			file.aborted = true
		}
	}
}
