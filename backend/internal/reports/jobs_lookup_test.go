package reports_test

import "btn-ops/internal/jobs"

func jobsLookup(name string) (jobs.Job, bool) { return jobs.Lookup(name) }
