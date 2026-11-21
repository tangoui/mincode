package cloudcompanion

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMemberDevicesListAndRemove(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	identity, err := loadOrCreateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	removed := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/common/refreshToken":
			_, _ = w.Write([]byte(`{"code":1,"data":{"token":"access"}}`))
		case "/api/mincode/memberDevice/index":
			if r.Header.Get("ba-user-token") != "access" {
				t.Error("device list was not authenticated")
			}
			_, _ = w.Write([]byte(`{"code":1,"data":{"items":[{"device_id":"` + identity.DeviceID + `","name":"This Mac","platform":"darwin/arm64","app_version":"0.1.0","first_seen_at":1,"last_seen_at":2},{"device_id":"bad id!","name":"x"},{"device_id":"desktop-OtherOtherOtherOther","device_type":"desktop","name":"Old PC","platform":"windows/amd64","app_version":"0.1.0","first_seen_at":1,"last_seen_at":1},{"device_id":"mobile-PhonePhonePhonePhone","device_type":"mobile","name":"Phone","platform":"ios","app_version":"0.1.0","first_seen_at":1,"last_seen_at":1}]}}`))
		case "/api/mincode/memberDevice/remove":
			removed = r.PostFormValue("device_id")
			_, _ = w.Write([]byte(`{"code":1,"data":{"removed":true}}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if _, err := cloudMemberDevicesHandler("", ""); err == nil {
		t.Fatal("signed-out device list succeeded")
	}
	if err := writeSession(persistedSession{APIBaseURL: server.URL, AccountID: 42, AccessToken: "old", RefreshToken: "refresh", AuthProvider: "member"}); err != nil {
		t.Fatal(err)
	}
	raw, err := cloudMemberDevicesHandler("", "")
	if err != nil {
		t.Fatal(err)
	}
	var devices []MemberDevice
	if err := json.Unmarshal([]byte(raw), &devices); err != nil {
		t.Fatal(err)
	}
	if len(devices) != 3 || !devices[0].Current || devices[1].Current || devices[1].Name != "Old PC" {
		t.Fatalf("devices = %+v", devices)
	}
	// device_type is passed through; a server without the field falls back to the device_id prefix.
	if devices[0].DeviceType != "desktop" || devices[1].DeviceType != "desktop" || devices[2].DeviceType != "mobile" {
		t.Fatalf("device types = %q %q %q", devices[0].DeviceType, devices[1].DeviceType, devices[2].DeviceType)
	}
	if memberDeviceType("", "mobile-PhonePhonePhonePhone") != "mobile" || memberDeviceType("tablet", "desktop-x") != "desktop" {
		t.Fatal("memberDeviceType fallback")
	}
	if _, err := cloudMemberDeviceRemoveHandler("", `{"deviceId":"bad id!"}`); err == nil {
		t.Fatal("invalid device ID sent")
	}
	if _, err := cloudMemberDeviceRemoveHandler("", `{"deviceId":"desktop-OtherOtherOtherOther"}`); err != nil || removed != "desktop-OtherOtherOtherOther" {
		t.Fatalf("remove: %v, sent %q", err, removed)
	}
}
