package desktop

import (
	"errors"
	"reflect"
	"testing"
)

func TestSetPluginTrayWithoutAdapter(t *testing.T) {
	previous := currentAdapter()
	t.Cleanup(func() { SetAdapter(previous) })
	SetAdapter(Adapter{})
	if err := SetPluginTray([]PluginTrayGroup{{PluginID: "p", Name: "P", Items: []PluginTrayItem{{CommandID: "c", Title: "C"}}}}); !errors.Is(err, errPluginTrayUnavailable) {
		t.Fatalf("err = %v", err)
	}
	var got []PluginTrayGroup
	calls := 0
	SetAdapter(Adapter{SetPluginTray: func(groups []PluginTrayGroup) { calls++; got = groups }})
	want := []PluginTrayGroup{{PluginID: "p", Name: "P", Items: []PluginTrayItem{{CommandID: "c", Title: "C"}}}}
	if err := SetPluginTray(want); err != nil || calls != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("err %v calls %d got %+v", err, calls, got)
	}
}
