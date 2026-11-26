package main

import (
	"context"
	"log/slog"

	"btn-ops/internal/deps"
	"btn-ops/internal/jobs"
)

// startJobs runs the job scheduler until ctx is cancelled (JOBS_ENABLED=false disables it on this
// instance). The returned channel closes once running jobs have finished and the lock is released.
func startJobs(ctx context.Context, d *deps.Deps) <-chan struct{} {
	done := make(chan struct{})
	if !d.Cfg.JobsEnabled {
		slog.Info("job scheduler disabled on this instance (JOBS_ENABLED=false)")
		close(done)
		return done
	}
	s := jobs.NewScheduler(d.Pool, jobs.Options{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	return done
}
