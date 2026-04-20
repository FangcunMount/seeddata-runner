package main

import (
	"context"

	"github.com/FangcunMount/seeddata-runner/internal/dailysim"
	"github.com/FangcunMount/seeddata-runner/internal/seedruntime"
)

func seedDailySimulationDaemon(ctx context.Context, deps *seedruntime.Dependencies) error {
	return dailysim.RunDaemon(ctx, deps)
}
