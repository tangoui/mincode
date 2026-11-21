package plugin

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

func TestLargeStorageFeatureOffer(t *testing.T) {
	if _, ok := offeredRuntimeFeatures(map[api.Capability]struct{}{})["largeStorage"]; ok {
		t.Fatal("largeStorage offered without the storage capability")
	}
	offer := offeredRuntimeFeatures(map[api.Capability]struct{}{api.CapabilityStorage: {}})
	if offer["largeStorage"] != 1 {
		t.Fatalf("offer: %v", offer)
	}
	if got := negotiateRuntimeFeatures(offer, json.RawMessage(`{"features":{"largeStorage":1}}`)).maxMessage(); got != pluginRPCMaxStorageMessage {
		t.Fatalf("negotiated cap %d, want %d", got, pluginRPCMaxStorageMessage)
	}
	if got := negotiateRuntimeFeatures(offer, json.RawMessage(`{"features":{"largeStorage":2}}`)).maxMessage(); got != pluginRPCMaxMessage {
		t.Fatalf("other version must not raise the cap: %d", got)
	}
	if pluginRPCMaxStorageMessage <= pluginKVMaxValueBytes || pluginRPCMaxLine < pluginRPCMaxStorageMessage || pluginRPCMaxLine < pluginRPCMaxLargeReply {
		t.Fatal("line caps must hold a full storage value and every large reply")
	}
}

func TestStoredValuesUpTo4MiB(t *testing.T) {
	useTemporaryPluginData(t)
	value := json.RawMessage(`"` + strings.Repeat("x", pluginKVMaxValueBytes-2) + `"`)
	if err := pluginKVSetRaw("large-test", "big", value); err != nil {
		t.Fatal(err)
	}
	got, found, err := pluginKVGetRaw("large-test", "big")
	if err != nil || !found || len(got) != pluginKVMaxValueBytes {
		t.Fatalf("read back %d bytes, found=%v err=%v", len(got), found, err)
	}
	if err := pluginKVSetRaw("large-test", "big", json.RawMessage(`"`+strings.Repeat("x", pluginKVMaxValueBytes-1)+`"`)); err == nil {
		t.Fatal("value above 4 MiB accepted")
	}
}

// A negotiated generation may send a host.storage.set request above 1 MiB;
// every other oversized request still stops the reader.
func TestReadStdoutLargeStorageRequests(t *testing.T) {
	const big = 2 << 20
	setLine := func(method string) string {
		return `{"jsonrpc":"2.0","id":"host-1","method":"` + method + `","params":{"key":"k","value":"` + strings.Repeat("x", big) + `"}}` + "\n"
	}
	small := `{"jsonrpc":"2.0","id":"c","result":true}` + "\n"
	run := func(negotiated bool, stdout string) bool {
		t.Helper()
		h := newProcessRuntime("", Manifest{}, nil, nil)
		h.generation = 1
		h.cmd = &exec.Cmd{}
		h.pending = map[string]chan rpcEnvelope{}
		h.largePending = map[string]struct{}{}
		reply := make(chan rpcEnvelope, 1)
		h.pending["c"] = reply
		limit := int64(pluginRPCMaxMessage)
		if negotiated {
			h.features = negotiateRuntimeFeatures(map[string]int{"largeStorage": 1}, json.RawMessage(`{"features":{"largeStorage":1}}`))
			limit = int64(h.features.maxMessage())
		}
		if h.replyLimit() != map[bool]int{true: pluginRPCMaxStorageMessage, false: pluginRPCMaxMessage}[negotiated] {
			t.Fatalf("reply limit %d (negotiated=%v)", h.replyLimit(), negotiated)
		}
		lineLimit := newRPCLineLimit()
		lineLimit.Store(limit)
		done := make(chan struct{})
		go func() { h.readStdout(1, strings.NewReader(stdout), lineLimit); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("readStdout did not return")
		}
		select {
		case <-reply:
			return true
		default:
			return false
		}
	}
	if !run(true, setLine("host.storage.set")+small) {
		t.Fatal("negotiated large host.storage.set stopped the reader")
	}
	if run(true, setLine("host.log")+small) {
		t.Fatal("large request other than host.storage.set accepted")
	}
	if run(false, setLine("host.storage.set")+small) {
		t.Fatal("large host.storage.set accepted without largeStorage")
	}
}
