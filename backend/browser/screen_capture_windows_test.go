//go:build windows

package browser

import (
	"bytes"
	"errors"
	"image/color"
	"image/png"
	"math"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestValidateWindowsCaptureRegion(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name       string
		x, y, w, h int
		valid      bool
	}{
		{name: "单像素", w: 1, h: 1, valid: true},
		{name: "负坐标副屏", x: -1920, y: -1080, w: 1920, h: 1080, valid: true},
		{name: "最大像素数", w: 8192, h: 4096, valid: true},
		{name: "最大单边", w: windowsCaptureMaxDimension, h: 1, valid: true},
		{name: "最小坐标", x: math.MinInt32, y: math.MinInt32, w: 1, h: 1, valid: true},
		{name: "端点边界", x: math.MaxInt32 - 1, y: math.MaxInt32 - 1, w: 1, h: 1, valid: true},
		{name: "零宽", h: 1},
		{name: "零高", w: 1},
		{name: "负宽", w: -1, h: 1},
		{name: "负高", w: 1, h: -1},
		{name: "宽度超限", w: windowsCaptureMaxDimension + 1, h: 1},
		{name: "高度超限", w: 1, h: windowsCaptureMaxDimension + 1},
		{name: "总像素超限", w: 8192, h: 4097},
		{name: "尺寸整数溢出", w: maxInt, h: maxInt},
		{name: "水平端点溢出", x: math.MaxInt32, w: 1, h: 1},
		{name: "垂直端点溢出", y: math.MaxInt32, w: 1, h: 1},
	}
	if maxInt > math.MaxInt32 {
		if err := validateWindowsCaptureRegion(maxInt, 0, 1, 1); err == nil {
			t.Fatal("巨大正坐标应失败")
		}
		if err := validateWindowsCaptureRegion(0, -maxInt, 1, 1); err == nil {
			t.Fatal("巨大负坐标应失败")
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWindowsCaptureRegion(tt.x, tt.y, tt.w, tt.h)
			if (err == nil) != tt.valid {
				t.Fatalf("validateWindowsCaptureRegion() = %v，期望有效：%v", err, tt.valid)
			}
		})
	}
}

func TestValidateWindowsCaptureDesktop(t *testing.T) {
	tests := []struct {
		name                     string
		x, y, w, h               int
		left, top, width, height int
		valid                    bool
	}{
		{name: "负原点桌面", x: -1920, y: -1080, w: 1920, h: 1080, left: -1920, top: -1080, width: 3840, height: 2160, valid: true},
		{name: "完整桌面", x: -1920, w: 3840, h: 1080, left: -1920, width: 3840, height: 1080, valid: true},
		{name: "无显示器", w: 1, h: 1},
		{name: "无显示器高度", w: 1, h: 1, width: 1920},
		{name: "左边越界", x: -1921, w: 1, h: 1, left: -1920, width: 3840, height: 1080},
		{name: "上边越界", y: -1081, w: 1, h: 1, top: -1080, width: 1920, height: 2160},
		{name: "右边越界", x: 1919, w: 2, h: 1, width: 1920, height: 1080},
		{name: "下边越界", y: 1079, w: 1, h: 2, width: 1920, height: 1080},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWindowsCaptureDesktop(tt.x, tt.y, tt.w, tt.h, tt.left, tt.top, tt.width, tt.height)
			if (err == nil) != tt.valid {
				t.Fatalf("validateWindowsCaptureDesktop() = %v，期望有效：%v", err, tt.valid)
			}
		})
	}
}

func TestWindowsCapturePixelsPNG(t *testing.T) {
	pixels := []byte{
		0x00, 0x00, 0xff, 0x00, 0x00, 0xff, 0x00, 0x01,
		0xff, 0x00, 0x00, 0x80, 0x12, 0x34, 0x56, 0xff,
	}
	encoded, err := windowsCapturePixelsPNG(pixels, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 2 {
		t.Fatalf("PNG 尺寸：%v", decoded.Bounds())
	}
	want := []color.RGBA{
		{R: 0xff, A: 0xff}, {G: 0xff, A: 0xff},
		{B: 0xff, A: 0xff}, {R: 0x56, G: 0x34, B: 0x12, A: 0xff},
	}
	for index, expected := range want {
		actual := color.RGBAModel.Convert(decoded.At(index%2, index/2)).(color.RGBA)
		if actual != expected {
			t.Errorf("像素 %d = %#v，期望 %#v", index, actual, expected)
		}
	}
}

func TestWindowsCapturePixelsPNGRejectsInvalidBuffer(t *testing.T) {
	for _, pixels := range [][]byte{nil, {1, 2, 3}, {1, 2, 3, 4, 5}} {
		if _, err := windowsCapturePixelsPNG(pixels, 1, 1); err == nil {
			t.Fatalf("缓冲区长度 %d 应失败", len(pixels))
		}
	}
	for _, size := range [][2]int{{0, 1}, {1, -1}, {int(^uint(0) >> 1), int(^uint(0) >> 1)}} {
		if _, err := windowsCapturePixelsPNG(nil, size[0], size[1]); err == nil {
			t.Fatalf("无效尺寸 %v 应在分配缓冲区前失败", size)
		}
	}
}

func TestValidateWindowsCaptureInputDesktop(t *testing.T) {
	tests := []struct {
		input, thread string
		valid         bool
	}{
		{input: "Default", thread: "Default", valid: true},
		{input: "default", thread: "DEFAULT", valid: true},
		{input: "Winlogon", thread: "Default"},
		{input: "Winlogon", thread: "Winlogon"},
		{input: "Disconnect", thread: "Default"},
		{input: "Default", thread: "OtherDesktop"},
		{input: "OtherDesktop", thread: "OtherDesktop"},
		{input: "", thread: ""},
		{input: "Default", thread: ""},
	}
	for _, tt := range tests {
		t.Run(tt.input+"/"+tt.thread, func(t *testing.T) {
			err := validateWindowsCaptureInputDesktop(tt.input, tt.thread)
			if (err == nil) != tt.valid {
				t.Fatalf("输入桌面 %q、线程桌面 %q：%v，期望有效：%v", tt.input, tt.thread, err, tt.valid)
			}
		})
	}
}

func TestWindowsCaptureBitmapInfoLayout(t *testing.T) {
	if size := unsafe.Sizeof(windowsCaptureBitmapInfoHeader{}); size != 40 {
		t.Fatalf("BITMAPINFOHEADER 大小 = %d，期望 40", size)
	}
}

func TestWindowsCaptureCallError(t *testing.T) {
	for _, cause := range []error{nil, syscall.Errno(0)} {
		if err := windowsCaptureCallError("GetDC", cause); err == nil || !strings.Contains(err.Error(), "GetDC") {
			t.Fatalf("必须返回真实的调用失败：%v", err)
		}
	}
	cause := syscall.Errno(5)
	if err := windowsCaptureCallError("BitBlt", cause); !errors.Is(err, cause) {
		t.Fatalf("应保留 Windows 错误：%v", err)
	}
}
