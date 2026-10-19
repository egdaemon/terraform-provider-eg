package provider

import (
	"context"
	"net/http"

	"github.com/egdaemon/eg/schedules"
)

// scheduleAPI the eg api for a repository's scheduled workloads, see the schedules package.
type scheduleAPI interface {
	Create(ctx context.Context, s *schedules.Schedule) (*schedules.Schedule, error)
	Find(ctx context.Context, vcsuri string, id string) (*schedules.Schedule, error)
	Update(ctx context.Context, id string, s *schedules.Schedule) (*schedules.Schedule, error)
	Remove(ctx context.Context, id string) error
}

// httpSchedules the eg api over an authorized http client.
type httpSchedules struct {
	c *http.Client
}

func (t httpSchedules) Create(ctx context.Context, s *schedules.Schedule) (*schedules.Schedule, error) {
	return schedules.Create(ctx, t.c, s)
}

func (t httpSchedules) Find(ctx context.Context, vcsuri string, id string) (*schedules.Schedule, error) {
	return schedules.Find(ctx, t.c, vcsuri, id)
}

func (t httpSchedules) Update(ctx context.Context, id string, s *schedules.Schedule) (*schedules.Schedule, error) {
	return schedules.Update(ctx, t.c, id, s)
}

func (t httpSchedules) Remove(ctx context.Context, id string) error {
	return schedules.Remove(ctx, t.c, id)
}
