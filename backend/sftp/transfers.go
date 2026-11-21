package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

const (
	sftpTransferProgressEvent = "sftp-transfer-progress"
	sftpTransferEndedEvent    = "sftp-transfer-ended"
	maxSftpTransferJobs       = 512
	transferProgressInterval  = 100 * time.Millisecond
	transferProgressBytes     = 256 * 1024
)

var sftpTransferIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type sftpTransferStartPayload struct {
	JobID            string `json:"jobId"`
	SessionID        string `json:"sessionId"`
	Direction        string `json:"direction"`
	LocalPath        string `json:"localPath"`
	RemotePath       string `json:"remotePath"`
	Resume           bool   `json:"resume"`
	ConflictStrategy string `json:"conflictStrategy"` // "overwrite" (default) | "skip" | "auto-rename"
}

type sftpWorkspaceTransferStartPayload struct {
	JobID            string `json:"jobId"`
	SessionID        string `json:"sessionId"`
	ProjectRoot      string `json:"projectRoot"`
	Direction        string `json:"direction"`
	WorkspacePath    string `json:"workspacePath"`
	RemotePath       string `json:"remotePath"`
	Resume           bool   `json:"resume"`
	ConflictStrategy string `json:"conflictStrategy"`
}

type sftpTransferSnapshot struct {
	JobID            string  `json:"jobId"`
	SessionID        string  `json:"sessionId"`
	Direction        string  `json:"direction"`
	Status           string  `json:"status"`
	BytesTransferred int64   `json:"bytesTransferred"`
	TotalBytes       int64   `json:"totalBytes"`
	SpeedBytes       float64 `json:"speedBytesPerSecond"`
	Error            string  `json:"error,omitempty"`
	StartedAt        *int64  `json:"startedAt,omitempty"`
	FinishedAt       *int64  `json:"finishedAt,omitempty"`
	ResumedFrom      int64   `json:"resumedFrom"`
}

type sftpTransferJob struct {
	mu sync.RWMutex

	payload          sftpTransferStartPayload
	status           string
	bytesTransferred int64
	totalBytes       int64
	resumedFrom      int64
	errorMessage     string
	startedAt        *int64
	finishedAt       *int64
	lastEmittedAt    time.Time
	lastEmittedBytes int64
	cancel           context.CancelFunc
}

func (j *sftpTransferJob) snapshot() sftpTransferSnapshot {
	j.mu.RLock()
	defer j.mu.RUnlock()
	var speed float64
	if j.startedAt != nil {
		elapsed := time.Since(time.UnixMilli(*j.startedAt)).Seconds()
		if elapsed > 0 {
			speed = float64(j.bytesTransferred-j.resumedFrom) / elapsed
		}
	}
	return sftpTransferSnapshot{
		JobID:            j.payload.JobID,
		SessionID:        j.payload.SessionID,
		Direction:        j.payload.Direction,
		Status:           j.status,
		BytesTransferred: j.bytesTransferred,
		TotalBytes:       j.totalBytes,
		SpeedBytes:       speed,
		Error:            j.errorMessage,
		StartedAt:        j.startedAt,
		FinishedAt:       j.finishedAt,
		ResumedFrom:      j.resumedFrom,
	}
}

func (j *sftpTransferJob) start(totalBytes, resumedFrom int64) {
	now := time.Now()
	millis := now.UnixMilli()
	j.mu.Lock()
	j.status = "running"
	j.totalBytes = totalBytes
	j.bytesTransferred = resumedFrom
	j.resumedFrom = resumedFrom
	j.startedAt = &millis
	j.lastEmittedAt = time.Time{}
	j.lastEmittedBytes = resumedFrom
	j.mu.Unlock()
	j.emitProgress(true)
}

func (j *sftpTransferJob) setProgress(bytes int64, force bool) {
	j.mu.Lock()
	j.bytesTransferred = bytes
	now := time.Now()
	shouldEmit := force || j.lastEmittedAt.IsZero() || now.Sub(j.lastEmittedAt) >= transferProgressInterval || bytes-j.lastEmittedBytes >= transferProgressBytes
	if shouldEmit {
		j.lastEmittedAt = now
		j.lastEmittedBytes = bytes
	}
	j.mu.Unlock()
	if shouldEmit {
		core.Emit(sftpTransferProgressEvent, j.snapshot())
	}
}

func (j *sftpTransferJob) emitProgress(force bool) {
	j.mu.RLock()
	bytes := j.bytesTransferred
	j.mu.RUnlock()
	j.setProgress(bytes, force)
}

func (j *sftpTransferJob) finish(status string, err error) {
	now := time.Now().UnixMilli()
	j.mu.Lock()
	j.status = status
	j.finishedAt = &now
	if err != nil {
		j.errorMessage = err.Error()
	}
	j.mu.Unlock()
	j.emitProgress(true)
	core.Emit(sftpTransferEndedEvent, j.snapshot())
}

var transferManager = struct {
	sync.Mutex
	jobs      map[string]*sftpTransferJob
	semaphore chan struct{}
}{
	jobs:      make(map[string]*sftpTransferJob),
	semaphore: make(chan struct{}, 3),
}

func registerSftpTransferCommands() {
	core.Register("sftp_transfer_start", handleSftpTransferStart)
	core.Register("sftp_workspace_transfer_start", handleSftpWorkspaceTransferStart)
	core.Register("sftp_transfer_cancel", handleSftpTransferCancel)
	core.Register("sftp_transfer_list", handleSftpTransferList)
}

func handleSftpWorkspaceTransferStart(_ string, argsJSON string) (string, error) {
	var payload sftpWorkspaceTransferStartPayload
	if err := core.DecodeArgs(argsJSON, &payload); err != nil {
		return "", err
	}
	localPath, err := resolveSftpWorkspaceTransferPath(payload)
	if err != nil {
		return "", err
	}
	return startSftpTransfer(sftpTransferStartPayload{
		JobID:            payload.JobID,
		SessionID:        payload.SessionID,
		Direction:        payload.Direction,
		LocalPath:        localPath,
		RemotePath:       payload.RemotePath,
		Resume:           payload.Resume,
		ConflictStrategy: payload.ConflictStrategy,
	})
}

func handleSftpTransferStart(_ string, argsJSON string) (string, error) {
	var payload sftpTransferStartPayload
	if err := core.DecodeArgs(argsJSON, &payload); err != nil {
		return "", err
	}
	return startSftpTransfer(payload)
}

func startSftpTransfer(payload sftpTransferStartPayload) (string, error) {
	if err := validateSftpTransferPayload(payload); err != nil {
		return "", err
	}
	if _, err := getSession(payload.SessionID); err != nil {
		return "", err
	}

	ctx, cancel := context.WithCancel(context.Background())
	job := &sftpTransferJob{payload: payload, status: "queued", cancel: cancel}
	transferManager.Lock()
	if _, exists := transferManager.jobs[payload.JobID]; exists {
		transferManager.Unlock()
		cancel()
		return "", fmt.Errorf("SFTP 传输任务已存在: %s", payload.JobID)
	}
	pruneSettledSftpTransfersLocked()
	if len(transferManager.jobs) >= maxSftpTransferJobs {
		transferManager.Unlock()
		cancel()
		return "", fmt.Errorf("SFTP 传输任务过多，请先清理或稍后重试")
	}
	transferManager.jobs[payload.JobID] = job
	transferManager.Unlock()

	go runSftpTransfer(ctx, job)
	return core.EncodeResult(job.snapshot())
}

func handleSftpTransferCancel(_ string, argsJSON string) (string, error) {
	var args struct {
		JobID     string `json:"jobId"`
		SessionID string `json:"sessionId"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	transferManager.Lock()
	job := transferManager.jobs[args.JobID]
	transferManager.Unlock()
	if job == nil || job.payload.SessionID != args.SessionID {
		return "", fmt.Errorf("SFTP 传输任务不存在")
	}
	job.cancel()
	return "", nil
}

func handleSftpTransferList(_ string, argsJSON string) (string, error) {
	var args struct {
		SessionID string `json:"sessionId"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	transferManager.Lock()
	jobs := make([]*sftpTransferJob, 0, len(transferManager.jobs))
	for _, job := range transferManager.jobs {
		if args.SessionID == "" || job.payload.SessionID == args.SessionID {
			jobs = append(jobs, job)
		}
	}
	transferManager.Unlock()
	snapshots := make([]sftpTransferSnapshot, 0, len(jobs))
	for _, job := range jobs {
		snapshots = append(snapshots, job.snapshot())
	}
	return core.EncodeResult(snapshots)
}

func resolveSftpWorkspaceTransferPath(payload sftpWorkspaceTransferStartPayload) (string, error) {
	root := strings.TrimSpace(payload.ProjectRoot)
	if root == "" {
		return "", fmt.Errorf("工作区根目录无效")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("工作区根目录无效: %w", err)
	}
	rootCanon, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("工作区根目录无效: %w", err)
	}
	rootInfo, err := os.Stat(rootCanon)
	if err != nil || !rootInfo.IsDir() {
		return "", fmt.Errorf("工作区根目录无效")
	}

	relative := strings.TrimSpace(payload.WorkspacePath)
	if relative == "" || strings.ContainsRune(relative, 0) || filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" {
		return "", fmt.Errorf("工作区文件路径无效")
	}
	cleanRelative := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(relative, "\\", "/")))
	if cleanRelative == "." || cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("工作区文件路径越界")
	}
	joined := filepath.Join(rootCanon, cleanRelative)
	if !sftpPathWithinRoot(rootCanon, joined) {
		return "", fmt.Errorf("工作区文件路径越界")
	}

	if payload.Direction != "upload" {
		return "", fmt.Errorf("工作区 SFTP 传输当前仅支持上传")
	}
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil || !sftpPathWithinRoot(rootCanon, resolved) {
		return "", fmt.Errorf("工作区文件路径越界")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("工作区上传源必须是普通文件")
	}
	return resolved, nil
}

func sftpPathWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validateSftpTransferPayload(payload sftpTransferStartPayload) error {
	if !sftpTransferIDPattern.MatchString(payload.JobID) {
		return fmt.Errorf("SFTP 传输任务 ID 无效")
	}
	if strings.TrimSpace(payload.SessionID) == "" || len(payload.SessionID) > 256 {
		return fmt.Errorf("SFTP 会话 ID 无效")
	}
	if payload.Direction != "upload" && payload.Direction != "download" {
		return fmt.Errorf("SFTP 传输方向无效")
	}
	if strings.TrimSpace(payload.LocalPath) == "" || strings.ContainsRune(payload.LocalPath, 0) {
		return fmt.Errorf("本地文件路径无效")
	}
	if strings.TrimSpace(payload.RemotePath) == "" || strings.ContainsRune(payload.RemotePath, 0) {
		return fmt.Errorf("远程文件路径无效")
	}
	return nil
}

func runSftpTransfer(ctx context.Context, job *sftpTransferJob) {
	select {
	case transferManager.semaphore <- struct{}{}:
		defer func() { <-transferManager.semaphore }()
	case <-ctx.Done():
		job.finish("cancelled", nil)
		return
	}

	var err error
	if job.payload.Direction == "upload" {
		err = runSftpUploadTransfer(ctx, job)
	} else {
		err = runSftpDownloadTransfer(ctx, job)
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		job.finish("cancelled", nil)
		return
	}
	if err != nil {
		job.finish("failed", err)
		return
	}
	job.finish("completed", nil)
}

func runSftpUploadTransfer(ctx context.Context, job *sftpTransferJob) error {
	localPath := filepath.Clean(job.payload.LocalPath)
	if err := rejectSensitiveLocalReadPath(localPath); err != nil {
		return err
	}
	info, err := os.Lstat(localPath)
	if err != nil {
		return fmt.Errorf("读取本地文件失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("仅支持上传普通文件")
	}
	localFile, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer localFile.Close()

	state, release, err := acquireSession(job.payload.SessionID)
	if err != nil {
		return err
	}
	defer release()

	remotePath := job.payload.RemotePath
	var offset int64
	if remoteInfo, statErr := state.client.Lstat(remotePath); statErr == nil {
		if remoteInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("拒绝写入远程符号链接")
		}
		if !remoteInfo.Mode().IsRegular() {
			return fmt.Errorf("远程目标不是普通文件")
		}
		if job.payload.Resume && remoteInfo.Size() <= info.Size() {
			offset = remoteInfo.Size()
		} else {
			switch job.payload.ConflictStrategy {
			case "skip":
				return nil
			case "auto-rename":
				remotePath = findUniqueRemotePath(state.client, remotePath)
			}
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("检查远程文件失败: %w", statErr)
	}

	flags := os.O_WRONLY | os.O_CREATE
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	remoteFile, err := state.client.OpenFile(remotePath, flags)
	if err != nil {
		return fmt.Errorf("打开远程文件失败: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = remoteFile.Close()
		}
	}()

	if offset > 0 {
		if _, err := localFile.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("定位本地文件失败: %w", err)
		}
		if _, err := remoteFile.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("定位远程文件失败: %w", err)
		}
	}
	job.start(info.Size(), offset)
	if offset < info.Size() {
		if err := copySftpTransfer(ctx, remoteFile, localFile, offset, job); err != nil {
			return err
		}
	}
	if err := remoteFile.Close(); err != nil {
		return fmt.Errorf("完成远程文件上传失败: %w", err)
	}
	closed = true
	return nil
}

func runSftpDownloadTransfer(ctx context.Context, job *sftpTransferJob) error {
	localPath := filepath.Clean(job.payload.LocalPath)
	if err := rejectSensitiveLocalPath(localPath); err != nil {
		return err
	}

	state, release, err := acquireSession(job.payload.SessionID)
	if err != nil {
		return err
	}
	defer release()

	remoteInfo, err := state.client.Lstat(job.payload.RemotePath)
	if err != nil {
		return fmt.Errorf("读取远程文件失败: %w", err)
	}
	if remoteInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("不支持下载远程符号链接")
	}
	if remoteInfo.IsDir() {
		return runSftpDownloadDirTransfer(ctx, state.client, job, job.payload.RemotePath, localPath)
	}
	if !remoteInfo.Mode().IsRegular() {
		return fmt.Errorf("仅支持下载普通文件或目录")
	}

	// File download: uses a .part file for resumable transfers.
	partPath := localPath + ".part"
	if err := rejectSensitiveLocalPath(partPath); err != nil {
		return err
	}
	if parent := filepath.Dir(localPath); parent != "" && parent != "." {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("创建目标目录失败: %w", err)
		}
	}
	if info, statErr := os.Lstat(partPath); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("拒绝写入符号链接")
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("检查断点文件失败: %w", statErr)
	}

	remoteFile, err := state.client.Open(job.payload.RemotePath)
	if err != nil {
		return fmt.Errorf("打开远程文件失败: %w", err)
	}
	defer remoteFile.Close()

	var offset int64
	if job.payload.Resume {
		if partInfo, statErr := os.Stat(partPath); statErr == nil && partInfo.Mode().IsRegular() && partInfo.Size() <= remoteInfo.Size() {
			offset = partInfo.Size()
		}
	}
	flags := os.O_WRONLY | os.O_CREATE
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	localFile, err := os.OpenFile(partPath, flags, 0o600)
	if err != nil {
		return fmt.Errorf("打开本地断点文件失败: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = localFile.Close()
		}
	}()
	if offset > 0 {
		if _, err := remoteFile.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("定位远程文件失败: %w", err)
		}
		if _, err := localFile.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("定位本地断点文件失败: %w", err)
		}
	}
	job.start(remoteInfo.Size(), offset)
	if offset < remoteInfo.Size() {
		if err := copySftpTransfer(ctx, localFile, remoteFile, offset, job); err != nil {
			return err
		}
	}
	if err := localFile.Sync(); err != nil {
		return fmt.Errorf("同步本地文件失败: %w", err)
	}
	if err := localFile.Close(); err != nil {
		return fmt.Errorf("保存本地文件失败: %w", err)
	}
	closed = true
	return replaceDownloadedFile(partPath, localPath)
}

// runSftpDownloadDirTransfer downloads an entire remote directory tree into localDir.
// It walks the tree first to compute the total byte count, then downloads each file
// sequentially while updating cumulative progress on the job.
func runSftpDownloadDirTransfer(ctx context.Context, client remoteClient, job *sftpTransferJob, remoteDir, localDir string) error {
	type fileSpec struct {
		remote string
		local  string
		size   int64
	}
	type dirPair struct {
		remote string
		local  string
	}

	var files []fileSpec
	var totalBytes int64
	dirQueue := []dirPair{{remote: remoteDir, local: localDir}}
	for len(dirQueue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		cur := dirQueue[0]
		dirQueue = dirQueue[1:]
		entries, err := client.ReadDir(cur.remote)
		if err != nil {
			return fmt.Errorf("读取远程目录失败 %s: %w", cur.remote, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !isSafeLocalEntryName(name) {
				return fmt.Errorf("远程目录包含非法条目名称: %q", name)
			}
			if entry.Mode()&os.ModeSymlink != 0 {
				continue // Skip symlinks — cannot be recreated on the local filesystem
			}
			remoteChild := path.Join(cur.remote, name)
			localChild := filepath.Join(cur.local, name)
			if entry.IsDir() {
				dirQueue = append(dirQueue, dirPair{remote: remoteChild, local: localChild})
			} else if entry.Mode().IsRegular() {
				files = append(files, fileSpec{remote: remoteChild, local: localChild, size: entry.Size()})
				totalBytes += entry.Size()
			}
		}
	}

	if _, err := os.Lstat(localDir); err == nil {
		return fmt.Errorf("目标目录已存在: %s", localDir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查目标目录失败: %w", err)
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return fmt.Errorf("创建本地目录失败: %w", err)
	}
	done := false
	defer func() {
		if !done {
			_ = os.RemoveAll(localDir)
		}
	}()

	job.start(totalBytes, 0)

	var transferred int64
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(f.local), 0o755); err != nil {
			return fmt.Errorf("创建本地目录失败: %w", err)
		}
		n, err := downloadSftpDirFile(ctx, client, f.remote, f.local, transferred, job)
		transferred += n
		if err != nil {
			return fmt.Errorf("下载失败 %s: %w", f.remote, err)
		}
	}
	job.setProgress(transferred, true)
	done = true
	return nil
}

// downloadSftpDirFile downloads one file within a directory transfer, reporting
// progress relative to baseBytes (bytes already transferred before this file).
// Returns the number of bytes actually written.
func downloadSftpDirFile(ctx context.Context, client remoteClient, remotePath, localPath string, baseBytes int64, job *sftpTransferJob) (int64, error) {
	remoteFile, err := client.Open(remotePath)
	if err != nil {
		return 0, fmt.Errorf("打开远程文件失败: %w", err)
	}
	defer remoteFile.Close()

	localFile, err := os.OpenFile(localPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("创建本地文件失败: %w", err)
	}
	defer localFile.Close()

	buf := make([]byte, 256*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, readErr := remoteFile.Read(buf)
		if n > 0 {
			if _, writeErr := localFile.Write(buf[:n]); writeErr != nil {
				return written, fmt.Errorf("写入本地文件失败: %w", writeErr)
			}
			written += int64(n)
			job.setProgress(baseBytes+written, false)
		}
		if readErr == io.EOF {
			if err := remoteFile.Close(); err != nil {
				return written, fmt.Errorf("完成远程下载失败: %w", err)
			}
			return written, nil
		}
		if readErr != nil {
			return written, fmt.Errorf("读取远程文件失败: %w", readErr)
		}
	}
}

func copySftpTransfer(ctx context.Context, dst io.Writer, src io.Reader, offset int64, job *sftpTransferJob) error {
	buffer := make([]byte, 256*1024)
	transferred := offset
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			written := 0
			for written < n {
				if err := ctx.Err(); err != nil {
					return err
				}
				count, writeErr := dst.Write(buffer[written:n])
				if count > 0 {
					written += count
					transferred += int64(count)
					job.setProgress(transferred, false)
				}
				if writeErr != nil {
					return fmt.Errorf("写入传输目标失败: %w", writeErr)
				}
				if count == 0 {
					return io.ErrShortWrite
				}
			}
		}
		if readErr == io.EOF {
			job.setProgress(transferred, true)
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("读取传输源失败: %w", readErr)
		}
	}
}

func replaceDownloadedFile(partPath, localPath string) error {
	if err := os.Rename(partPath, localPath); err == nil {
		return nil
	}
	if info, statErr := os.Lstat(localPath); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("拒绝覆盖非普通文件")
		}
		if removeErr := os.Remove(localPath); removeErr != nil {
			return fmt.Errorf("替换本地文件失败: %w", removeErr)
		}
	}
	if err := os.Rename(partPath, localPath); err != nil {
		return fmt.Errorf("完成下载文件替换失败: %w", err)
	}
	return nil
}

func cancelSftpTransfersForSession(sessionID string) {
	transferManager.Lock()
	jobs := make([]*sftpTransferJob, 0)
	for _, job := range transferManager.jobs {
		if job.payload.SessionID == sessionID {
			jobs = append(jobs, job)
		}
	}
	transferManager.Unlock()
	for _, job := range jobs {
		job.cancel()
	}
}

func cancelAllSftpTransfers() {
	transferManager.Lock()
	jobs := make([]*sftpTransferJob, 0, len(transferManager.jobs))
	for _, job := range transferManager.jobs {
		jobs = append(jobs, job)
	}
	transferManager.Unlock()
	for _, job := range jobs {
		job.cancel()
	}
}

func pruneSettledSftpTransfersLocked() {
	if len(transferManager.jobs) < maxSftpTransferJobs {
		return
	}
	for id, job := range transferManager.jobs {
		status := job.snapshot().Status
		if status == "completed" || status == "failed" || status == "cancelled" {
			delete(transferManager.jobs, id)
		}
	}
}

// findUniqueRemotePath returns a path that does not exist on the remote by appending
// a numeric suffix (_1, _2, …) to the base name (before the extension).
func findUniqueRemotePath(client remoteClient, remotePath string) string {
	ext := filepath.Ext(remotePath)
	base := strings.TrimSuffix(remotePath, ext)
	for i := 1; i <= 999; i++ {
		candidate := fmt.Sprintf("%s_%d%s", base, i, ext)
		if _, err := client.Lstat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return fmt.Sprintf("%s_%d%s", base, 1000, ext)
}
