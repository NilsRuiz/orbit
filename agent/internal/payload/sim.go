// Package payload drives the workload the node runs.
//
// Sim is a simulator used in local dev (docker compose): it "applies" instantly and
// reports any version listed in OrbitBadVersions as unhealthy, so rollbacks can be demoed.
//
// TODO(phase 3): add a K3s implementation that patches the local Flux OCIRepository tag
// (client-go, in-cluster config) and checks the payload Deployment's readiness.
package payload

import (
	"context"
	"slices"
	"time"
)

type Sim struct {
	ApplyDelay  time.Duration
	BadVersions []string
}

func (s Sim) Apply(ctx context.Context, _ string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.ApplyDelay):
		return nil
	}
}

func (s Sim) Healthy(_ context.Context, version string) (bool, error) {
	return !slices.Contains(s.BadVersions, version), nil
}
