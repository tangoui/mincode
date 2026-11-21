package plugin

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// The counter is process-wide; this test is intentionally non-parallel and
// restores it so later runtimes in this package keep unique instances.
func TestRuntimeIdentityInstanceExhaustion(t *testing.T) {
	saved := nextRuntimeInstance.Load()
	t.Cleanup(func() { nextRuntimeInstance.Store(saved) })
	nextRuntimeInstance.Store(math.MaxUint64 - 1)
	last := newProcessRuntime("", Manifest{}, nil, nil)
	if got := last.identity(1).Instance; got != "18446744073709551615" {
		t.Fatalf("last instance = %q", got)
	}
	for range 2 {
		exhausted := newProcessRuntime("", Manifest{}, nil, nil)
		if got := exhausted.identity(1).Instance; got != "" {
			t.Fatalf("exhausted runtime received reusable instance %q", got)
		}
		if err := exhausted.Start(context.Background()); err == nil {
			t.Fatal("runtime without an instance started")
		}
	}
	if nextRuntimeInstance.Load() != math.MaxUint64 {
		t.Fatal("exhausted counter wrapped")
	}
}

func TestRuntimeIdentityReplacement(t *testing.T) {
	manager, record := contributionInvokeManager(t)
	if err := manager.startRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	old := record.runtime
	oldID := old.identity(old.Generation())
	if oldID.Instance == "" || oldID.Instance == "0" || oldID.Generation != "1" {
		t.Fatalf("initial identity = %+v, want nonzero instance and generation 1", oldID)
	}
	if err := manager.validateRuntimeIdentity(record.id, oldID); err != nil {
		t.Fatalf("current identity rejected: %v", err)
	}
	if err := old.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	replacement := manager.newRecordRuntime(record, record.root, *record.manifest)
	if err := replacement.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	record.runtime, record.state = replacement, pluginStateRunning
	record.mu.Unlock()
	current := replacement.identity(replacement.Generation())
	if current.Generation != "1" || current.Instance == oldID.Instance {
		t.Fatalf("replacement = %+v, old = %+v; instance must not be reused", current, oldID)
	}
	if err := manager.validateRuntimeIdentity(record.id, oldID); err == nil {
		t.Fatal("old generation 1 admitted to a replacement generation 1")
	}
	if err := manager.validateRuntimeIdentity(record.id, current); err != nil {
		t.Fatal(err)
	}
	if err := manager.validateRuntimeIdentity(record.id, runtimeIdentity{}); err == nil {
		t.Fatal("zero identity accepted")
	}
	for _, malformed := range []runtimeIdentity{
		{Instance: "0" + current.Instance, Generation: current.Generation},
		{Instance: current.Instance, Generation: "01"},
		{Instance: " " + current.Instance, Generation: current.Generation},
		{Instance: current.Instance, Generation: "+1"},
		{Instance: current.Instance, Generation: "0"},
		{Instance: "18446744073709551616", Generation: current.Generation},
	} {
		if err := manager.validateRuntimeIdentity(record.id, malformed); err == nil {
			t.Fatalf("non-canonical identity %+v accepted", malformed)
		}
	}
	encoded, err := json.Marshal(current)
	if err != nil || string(encoded) != `{"instance":"`+current.Instance+`","generation":"1"}` {
		t.Fatalf("JSON identity = %s, %v", encoded, err)
	}
	if err := replacement.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.validateRuntimeIdentity(record.id, current); err == nil {
		t.Fatal("stopped identity accepted while record still says running")
	}
	if err := replacement.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.validateRuntimeIdentity(record.id, current); err == nil {
		t.Fatal("identity survived a new generation in the same runtime")
	}
	fresh := replacement.identity(replacement.Generation())
	if fresh.Instance != current.Instance || fresh.Generation != "2" {
		t.Fatalf("restart identity = %+v", fresh)
	}
	if err := manager.validateRuntimeIdentity(record.id, fresh); err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	record.enabled = false
	record.mu.Unlock()
	if err := manager.validateRuntimeIdentity(record.id, fresh); err == nil {
		t.Fatal("disabled identity accepted")
	}
}

func TestRuntimeIdentityCallerMetadataFrozen(t *testing.T) {
	manager := &pluginManager{}
	for _, source := range []string{"builtin", "local", "development", "", "marketplace"} {
		t.Run(source, func(t *testing.T) {
			record := &pluginRecord{source: source}
			manifest := Manifest{ID: "metadata.test", Version: "1.2.3"}
			runtimeHost := manager.newRecordRuntime(record, "/private/package", manifest)
			metadata := runtimeHost.callerMetadata
			known := source == "builtin" || source == "local" || source == "development"
			wantSource := source
			if !known {
				wantSource = "unknown"
			}
			if metadata.Source != wantSource || metadata.Version != "1.2.3" {
				t.Fatalf("metadata = %+v", metadata)
			}
			if known {
				if metadata.Development == nil || *metadata.Development != (source == "development") {
					t.Fatalf("development not explicitly captured for %s", source)
				}
			} else if metadata.Development != nil {
				t.Fatal("unknown source was interpreted as development:false")
			}
			record.source = "development"
			record.manifest = &Manifest{Version: "9.9.9"}
			if runtimeHost.callerMetadata.Source != wantSource || runtimeHost.callerMetadata.Version != "1.2.3" {
				t.Fatal("runtime metadata changed with the mutable record")
			}
		})
	}
	plain := newProcessRuntime("", Manifest{}, nil, nil)
	if plain.callerMetadata.Source != "unknown" || plain.callerMetadata.Development != nil {
		t.Fatalf("unowned runtime metadata = %+v", plain.callerMetadata)
	}
	for _, version := range []string{strings.Repeat("a", 128), strings.Repeat("é", 65)} {
		runtimeHost := manager.newRecordRuntime(&pluginRecord{source: "local"}, "", Manifest{Version: version})
		want := version
		if len(version) > 128 {
			want = ""
		}
		if runtimeHost.callerMetadata.Version != want {
			t.Fatal("version must be omitted, not truncated, beyond 128 UTF-8 bytes")
		}
	}
}

func TestRuntimeGenerationExhaustion(t *testing.T) {
	manager, record := contributionInvokeManager(t)
	r := record.runtime
	r.generation = math.MaxUint64 - 1
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.Generation() != math.MaxUint64 {
		t.Fatal("last generation not allocated")
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := r.runContext
	for range 2 {
		err := r.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "generation space is exhausted") {
			t.Errorf("exhaustion was not rejected: %v", err)
		}
		r.mu.Lock()
		if r.cmd != nil || r.generation != math.MaxUint64 || r.runContext != before {
			t.Error("exhausted start launched a child or changed previous state")
		}
		r.mu.Unlock()
		if err == nil {
			_ = r.Stop(context.Background())
		}
	}
	_ = manager
}
