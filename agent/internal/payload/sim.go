// Package payload drives the workload the node runs.
//
//   - Sim is a simulator for local dev (docker compose): it "applies" after a delay and
//     reports any version listed in ORBIT_SIM_BAD_VERSIONS as unhealthy, so rollbacks can
//     be demoed.
//   - K3s deploys versions on the node's cluster through a Flux OCIRepository tag
//     (ADR-0009).
package payload

import (
	"context"
	"slices"
	"time"
)

// Sim is a simulated payload: applying takes ApplyDelay, and versions listed in
// BadVersions are reported unhealthy (to exercise the dead man's switch).
type Sim struct {
	ApplyDelay  time.Duration
	BadVersions []string
}

// Apply waits ApplyDelay, or returns early if ctx is cancelled.
func (s Sim) Apply(ctx context.Context, _ string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.ApplyDelay):
		return nil
	}
}

// Healthy reports false for versions listed in BadVersions.
func (s Sim) Healthy(_ context.Context, version string) (bool, error) {
	return !slices.Contains(s.BadVersions, version), nil
}
