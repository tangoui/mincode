package snapshot

import (
	"context"

	"mincode/app/backend/core"
	projectfs "mincode/app/backend/fs"
)

func init() {
	core.RegisterCtx("ai_snapshot_capture", func(ctx context.Context, _, raw string) (string, error) {
		var a struct {
			ProjectRoot string `json:"projectRoot"`
		}
		if err := core.DecodeArgs(raw, &a); err != nil {
			return "", err
		}
		root, err := projectfs.CanonicalProjectRoot(a.ProjectRoot)
		if err != nil {
			return "", err
		}
		c, err := CaptureProject(ctx, root)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(c)
	})
	core.RegisterCtx("ai_snapshot_diff", func(ctx context.Context, _, raw string) (string, error) {
		var a struct {
			ProjectRoot string `json:"projectRoot"`
			From        string `json:"from"`
			To          string `json:"to"`
		}
		if err := core.DecodeArgs(raw, &a); err != nil {
			return "", err
		}
		root, err := projectfs.CanonicalProjectRoot(a.ProjectRoot)
		if err != nil {
			return "", err
		}
		changes, err := DiffTrees(ctx, root, a.From, a.To)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(struct {
			Changes []FileChange `json:"changes"`
		}{Changes: changes})
	})
}

func init() {
	core.RegisterCtx("ai_snapshot_restore", func(ctx context.Context, _, raw string) (string, error) {
		var a RestoreRequest
		if err := core.DecodeArgs(raw, &a); err != nil {
			return "", err
		}
		root, err := projectfs.CanonicalProjectRoot(a.ProjectRoot)
		if err != nil {
			return "", err
		}
		a.ProjectRoot = root
		res, err := Restore(ctx, a)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(res)
	})
	core.RegisterCtx("ai_snapshot_prune", func(ctx context.Context, _, raw string) (string, error) {
		var a struct {
			ProjectRoot string   `json:"projectRoot"`
			Keep        []string `json:"keep"`
		}
		if err := core.DecodeArgs(raw, &a); err != nil {
			return "", err
		}
		root, err := projectfs.CanonicalProjectRoot(a.ProjectRoot)
		if err != nil {
			return "", err
		}
		if err := Prune(ctx, root, a.Keep); err != nil {
			return "", err
		}
		size, err := StoreSize(root)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(storeSizeResult{Bytes: size})
	})
	core.RegisterCtx("ai_snapshot_size", func(_ context.Context, _, raw string) (string, error) {
		var a struct {
			ProjectRoot string `json:"projectRoot"`
		}
		if err := core.DecodeArgs(raw, &a); err != nil {
			return "", err
		}
		root, err := projectfs.CanonicalProjectRoot(a.ProjectRoot)
		if err != nil {
			return "", err
		}
		size, err := StoreSize(root)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(storeSizeResult{Bytes: size})
	})
}

type storeSizeResult struct {
	Bytes int64 `json:"bytes"`
}
