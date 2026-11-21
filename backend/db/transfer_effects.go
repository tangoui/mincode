package db

import (
	"context"
	"mincode/app/backend/tasks"
)

type transferReporterKey struct{}

func withTransferReporter(ctx context.Context, r *tasks.Reporter) context.Context {
	return context.WithValue(ctx, transferReporterKey{}, r)
}
func beginTransferEffect(ctx context.Context, kind string) (string, error) {
	r, _ := ctx.Value(transferReporterKey{}).(*tasks.Reporter)
	if r == nil {
		return "", nil
	}
	return r.BeginEffect(ctx, tasks.EffectIntent{Kind: kind})
}
func finishTransferEffect(ctx context.Context, id string, outcome tasks.EffectOutcome) error {
	r, _ := ctx.Value(transferReporterKey{}).(*tasks.Reporter)
	if r == nil {
		return nil
	}
	return r.FinishEffect(ctx, id, outcome)
}
