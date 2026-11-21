//go:build windows

package terminal

import (
	"fmt"
	"net"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestClassifyPortForwardErrorWindows(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "原始 Winsock 端口占用", err: windows.WSAEADDRINUSE, want: portForwardErrorAddressInUse},
		{name: "包装的 Winsock 端口占用", err: fmt.Errorf("监听失败: %w", windows.WSAEADDRINUSE), want: portForwardErrorAddressInUse},
		{name: "网络系统调用包装", err: fmt.Errorf("启动失败: %w", &net.OpError{
			Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", windows.WSAEADDRINUSE),
		}), want: portForwardErrorAddressInUse},
		{name: "Winsock 连接被拒绝", err: windows.WSAECONNREFUSED, want: portForwardErrorStartFailed},
		{name: "包装的 Winsock 连接被拒绝", err: fmt.Errorf("启动失败: %w", &net.OpError{
			Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", windows.WSAECONNREFUSED),
		}), want: portForwardErrorStartFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPortForwardError(tc.err); got != tc.want {
				t.Fatalf("错误分类 = %q, 期望 %q: %v", got, tc.want, tc.err)
			}
		})
	}
}
