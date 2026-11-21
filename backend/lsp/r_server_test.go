package lsp

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func stubRServerProbe(t *testing.T, run func(context.Context, string, ...string) ([]byte, error)) {
	t.Helper()
	rServerProbeMu.Lock()
	previous := rServerProbeRun
	rServerProbeRun = run
	clear(rServerProbeCache)
	rServerProbeMu.Unlock()
	t.Cleanup(func() {
		rServerProbeMu.Lock()
		rServerProbeRun = previous
		clear(rServerProbeCache)
		rServerProbeMu.Unlock()
	})
}

func TestRServerProbeOutput(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		want   bool
	}{
		{"已安装", rServerProbeMarker + "\n", true},
		{"带诊断信息", "diagnostic\r\n" + rServerProbeMarker + "\r\n", true},
		{"未安装", "", false},
		{"仅有其他输出", "TRUE\n", false},
		{"表达式回显", "cat('" + rServerProbeMarker + "')", false},
		{"非完整标记", "prefix" + rServerProbeMarker, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := rServerProbeSucceeded([]byte(test.output)); got != test.want {
				t.Fatalf("探测输出结果 = %v，期望 %v", got, test.want)
			}
		})
	}
}

func TestRServerProbeUsesFixedSafeCommand(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "R.exe")
	stubRServerProbe(t, func(ctx context.Context, path string, args ...string) ([]byte, error) {
		if path != executable || !filepath.IsAbs(path) {
			t.Fatalf("未使用实际 R 绝对路径：%q", path)
		}
		want := []string{"--vanilla", "--slave", "-e", "if (requireNamespace('languageserver', quietly = TRUE)) cat('MINCODE_R_LANGUAGESERVER_READY\\n')"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("包探测参数 = %q，期望 %q", args, want)
		}
		deadline, ok := ctx.Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining <= 0 || remaining > 3*time.Second {
			t.Fatalf("未设置 3 秒探测超时：%v，剩余 %v", ok, remaining)
		}
		return []byte(rServerProbeMarker), nil
	})
	if !rServerInstalled(executable) {
		t.Fatal("有成功标记时应判定已安装")
	}
}

func TestRServerProbeFailure(t *testing.T) {
	for _, probeErr := range []error{errors.New("R 启动失败"), context.DeadlineExceeded} {
		t.Run(probeErr.Error(), func(t *testing.T) {
			stubRServerProbe(t, func(context.Context, string, ...string) ([]byte, error) {
				return []byte(rServerProbeMarker), probeErr
			})
			if rServerInstalled(filepath.Join(t.TempDir(), "R.exe")) {
				t.Fatal("命令失败或超时不能判定包已安装")
			}
		})
	}
}

func TestRServerProbeCache(t *testing.T) {
	calls := 0
	stubRServerProbe(t, func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, nil
	})
	executable := filepath.Join(t.TempDir(), "R.exe")
	if rServerInstalled(executable) || rServerInstalled(executable) || calls != 1 {
		t.Fatalf("应缓存未安装结果，调用次数 = %d", calls)
	}
	otherExecutable := filepath.Join(t.TempDir(), "R.exe")
	_ = rServerInstalled(otherExecutable)
	if calls != 2 {
		t.Fatalf("不同 R 路径必须独立探测，调用次数 = %d", calls)
	}
	rServerProbeMu.Lock()
	rServerProbeCache[executable] = rServerProbeEntry{at: time.Now().Add(-rServerProbeTTL)}
	rServerProbeMu.Unlock()
	_ = rServerInstalled(executable)
	if calls != 3 {
		t.Fatalf("缓存过期后必须重探，调用次数 = %d", calls)
	}
	invalidateRServerProbe()
	_ = rServerInstalled(executable)
	if calls != 4 {
		t.Fatalf("安装后缓存失效必须立即重探，调用次数 = %d", calls)
	}
	if rServerInstalled("") || calls != 4 {
		t.Fatal("空路径不能运行探测")
	}
}

func TestRExecutableStatusRequiresLanguageServerPackage(t *testing.T) {
	installed := false
	stubRServerProbe(t, func(context.Context, string, ...string) ([]byte, error) {
		if installed {
			return []byte(rServerProbeMarker), nil
		}
		return nil, nil
	})
	executable := filepath.Join(t.TempDir(), "R.exe")
	primeLookPath(t, "R", executable)
	definition, ok := definitionByID("r-languageserver")
	if !ok {
		t.Fatal("缺少 R 语言服务器定义")
	}
	status := executableStatus(definition)
	if status.Installed || status.Executable != executable {
		t.Fatalf("只有 R 本体时必须显示未安装且保留可执行路径：%+v", status)
	}
	installed = true
	invalidateRServerProbe()
	status = executableStatus(definition)
	if !status.Installed || status.Executable != executable {
		t.Fatalf("安装包并清除缓存后应显示已安装：%+v", status)
	}
}
