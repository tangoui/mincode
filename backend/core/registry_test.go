package core

import (
	"context"
	"errors"
	"testing"
)

type registryTestKey struct{}

func TestDispatchContextPassesCallContext(t *testing.T) {
	RegisterCtx("__test_registry_ctx", func(ctx context.Context, _ string, _ string) (string, error) {
		value, _ := ctx.Value(registryTestKey{}).(string)
		return value, nil
	})
	ctx := context.WithValue(context.Background(), registryTestKey{}, "from-caller")
	got, err := DispatchContext(ctx, "__test_registry_ctx", "{}")
	if err != nil || got != "from-caller" {
		t.Fatalf("DispatchContext() = %q, %v; want caller ctx value", got, err)
	}
}

func TestDispatchContextRejectsCancelledCall(t *testing.T) {
	called := false
	RegisterCtx("__test_registry_cancelled", func(context.Context, string, string) (string, error) {
		called = true
		return "", nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DispatchContext(ctx, "__test_registry_cancelled", "{}"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DispatchContext() error = %v; want context.Canceled", err)
	}
	if called {
		t.Fatal("handler ran for an already cancelled call")
	}
}

func TestDispatchLegacyHandlerAndUnknownCommand(t *testing.T) {
	Register("__test_registry_legacy", func(cmd, args string) (string, error) {
		return cmd + ":" + args, nil
	})
	if got, err := Dispatch("__test_registry_legacy", "{}"); err != nil || got != "__test_registry_legacy:{}" {
		t.Fatalf("Dispatch() = %q, %v", got, err)
	}
	if _, err := DispatchContext(context.Background(), "__test_registry_missing", "{}"); err == nil {
		t.Fatal("expected error for unknown command")
	}
}
