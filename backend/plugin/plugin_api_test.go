package plugin

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestPluginKVRawValidationQuotaAndOrdering(t *testing.T) {
	useTemporaryPluginData(t)
	if err := pluginKVSetRaw("example.plugin", "z-last", json.RawMessage(`42`)); err != nil {
		t.Fatal(err)
	}
	if err := pluginKVSetRaw("example.plugin", "a-first", json.RawMessage(`true`)); err != nil {
		t.Fatal(err)
	}
	value, found, err := pluginKVGetRaw("example.plugin", "z-last")
	if err != nil || !found || string(value) != "42" {
		t.Fatalf("unexpected get result value=%s found=%v err=%v", value, found, err)
	}
	keys, err := pluginKVListRaw("example.plugin")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, []string{"a-first", "z-last"}) {
		t.Fatalf("keys = %#v", keys)
	}
	if err := pluginKVSetRaw("example.plugin", "bad:key", json.RawMessage(`true`)); err == nil {
		t.Fatal("expected invalid key error")
	}
	if err := pluginKVSetRaw("example.plugin", "invalid", json.RawMessage(`{`)); err == nil {
		t.Fatal("expected invalid JSON error")
	}
	large := json.RawMessage(`"` + strings.Repeat("x", pluginKVMaxValueBytes) + `"`)
	if err := pluginKVSetRaw("example.plugin", "large", large); err == nil {
		t.Fatal("expected value size error")
	}
	for index := 0; index < pluginKVMaxKeys-2; index++ {
		if err := pluginKVSetRaw("example.plugin", fmt.Sprintf("key-%d", index), json.RawMessage(`null`)); err != nil {
			t.Fatalf("fill quota: %v", err)
		}
	}
	if err := pluginKVSetRaw("example.plugin", "over-quota", json.RawMessage(`null`)); err == nil {
		t.Fatal("expected key quota error")
	}
	if err := pluginKVSetRaw("example.plugin", "z-last", json.RawMessage(`43`)); err != nil {
		t.Fatalf("updating at quota failed: %v", err)
	}
	if err := pluginKVDeleteNamespace("example.plugin"); err != nil {
		t.Fatal(err)
	}
	keys, err = pluginKVListRaw("example.plugin")
	if err != nil || len(keys) != 0 {
		t.Fatalf("namespace delete left keys=%#v err=%v", keys, err)
	}
}

func TestPluginKVRawMessagePreservedExactly(t *testing.T) {
	useTemporaryPluginData(t)
	want := json.RawMessage("{\n  \"number\": 1.2300e+04, \"escaped\": \"\\u0061\", \"items\": [true, null]\n}")
	if err := pluginKVSetRaw("raw.plugin", "payload", want); err != nil {
		t.Fatal(err)
	}
	got, found, err := pluginKVGetRaw("raw.plugin", "payload")
	if err != nil || !found {
		t.Fatalf("get found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual([]byte(got), []byte(want)) {
		t.Fatalf("raw value changed:\n got %q\nwant %q", got, want)
	}
}

func TestPluginKVConcurrentQuotaNeverExceedsLimit(t *testing.T) {
	useTemporaryPluginData(t)
	const attempts = pluginKVMaxKeys + 64
	start := make(chan struct{})
	var wait sync.WaitGroup
	var succeeded int
	var succeededMu sync.Mutex
	var unexpected []error
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			err := pluginKVSetRaw("concurrent.plugin", fmt.Sprintf("key-%03d", index), json.RawMessage(`true`))
			if err == nil {
				succeededMu.Lock()
				succeeded++
				succeededMu.Unlock()
				return
			}
			if !strings.Contains(err.Error(), "最多保存") {
				succeededMu.Lock()
				unexpected = append(unexpected, err)
				succeededMu.Unlock()
			}
		}(index)
	}
	close(start)
	wait.Wait()
	if len(unexpected) != 0 {
		t.Fatalf("unexpected concurrent errors: %v", unexpected)
	}
	keys, err := pluginKVListRaw("concurrent.plugin")
	if err != nil {
		t.Fatal(err)
	}
	if succeeded != pluginKVMaxKeys || len(keys) != pluginKVMaxKeys {
		t.Fatalf("succeeded=%d keys=%d, want %d", succeeded, len(keys), pluginKVMaxKeys)
	}
}
