//go:build windows

package sessions

// platformLiveCwds 在 Windows 上返回 nil：读取任意进程的工作目录需要提权，
// 因此会话状态回退到转录文件推断。
func platformLiveCwds(map[string]struct{}) map[string]struct{} {
	return nil
}
