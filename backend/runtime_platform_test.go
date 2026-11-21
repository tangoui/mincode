package backend

import (
	"encoding/json"
	"runtime"
	"testing"
)

func TestGetRuntimePlatform(t *testing.T) {
	raw, err := Dispatch("get_runtime_platform", "{}")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		OS   string `json:"os"`
		Arch string `json:"arch"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Fatalf("platform = %+v, want %s/%s", got, runtime.GOOS, runtime.GOARCH)
	}
}
