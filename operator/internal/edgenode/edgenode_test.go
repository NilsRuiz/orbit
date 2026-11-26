package edgenode

import (
	"maps"
	"slices"
	"testing"
	"time"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
)

var t0 = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

func TestReachability(t *testing.T) {
	ci := 5 * time.Minute // Unreachable after 10m of silence
	tests := []struct {
		name        string
		lastSeen    time.Time
		now         time.Time
		want        fleetv1alpha1.Reachability
		wantRequeue time.Duration
	}{
		{"never seen", time.Time{}, t0, fleetv1alpha1.Unreachable, 0},
		{"just seen", t0, t0, fleetv1alpha1.Reachable, 10 * time.Minute},
		{"silent 4m", t0, t0.Add(4 * time.Minute), fleetv1alpha1.Reachable, 6 * time.Minute},
		{"silent exactly 2x", t0, t0.Add(10 * time.Minute), fleetv1alpha1.Unreachable, 0},
		{"silent 1h", t0, t0.Add(time.Hour), fleetv1alpha1.Unreachable, 0},
		// A lastSeen in the future (hub clock stepped back) must not keep the node
		// Reachable forever: the requeue is bounded by the threshold.
		{"lastSeen in the future", t0.Add(time.Hour), t0, fleetv1alpha1.Reachable, 10 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, requeue := Reachability(tt.lastSeen, ci, tt.now)
			if got != tt.want || requeue != tt.wantRequeue {
				t.Errorf("Reachability = %s, %v; want %s, %v", got, requeue, tt.want, tt.wantRequeue)
			}
		})
	}
}

func TestShouldWriteLastSeen(t *testing.T) {
	tests := []struct {
		name string
		prev time.Time
		seen time.Time
		ci   time.Duration
		want bool
	}{
		{"first contact", time.Time{}, t0, 5 * time.Minute, true},
		{"10s later", t0, t0.Add(10 * time.Second), 5 * time.Minute, false},
		{"59s later", t0, t0.Add(59 * time.Second), 5 * time.Minute, false},
		{"1m later", t0, t0.Add(time.Minute), 5 * time.Minute, true},
		{"older than stored", t0, t0.Add(-time.Hour), 5 * time.Minute, false},
		// A short contact interval shrinks the budget, or the node would flap Unreachable
		// between two writes: 2 x 40s = 80s threshold, writes every 20s.
		{"short interval, 20s later", t0, t0.Add(20 * time.Second), 40 * time.Second, true},
		{"short interval, 19s later", t0, t0.Add(19 * time.Second), 40 * time.Second, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldWriteLastSeen(tt.prev, tt.seen, tt.ci); got != tt.want {
				t.Errorf("ShouldWriteLastSeen = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSyncLabels(t *testing.T) {
	tests := []struct {
		name         string
		current      map[string]string
		reported     map[string]string
		want         map[string]string
		wantChanges  []string
		wantRejected []string
	}{
		{
			name:        "new node",
			current:     nil,
			reported:    map[string]string{"orbit.io/constellation": "alpha"},
			want:        map[string]string{"orbit.io/constellation": "alpha"},
			wantChanges: []string{"+orbit.io/constellation=alpha"},
		},
		{
			name:     "unchanged",
			current:  map[string]string{"orbit.io/constellation": "alpha"},
			reported: map[string]string{"orbit.io/constellation": "alpha"},
			want:     map[string]string{"orbit.io/constellation": "alpha"},
		},
		{
			name:        "changed value",
			current:     map[string]string{"orbit.io/constellation": "alpha"},
			reported:    map[string]string{"orbit.io/constellation": "beta"},
			want:        map[string]string{"orbit.io/constellation": "beta"},
			wantChanges: []string{"~orbit.io/constellation=beta"},
		},
		{
			name:        "removed by the agent",
			current:     map[string]string{"orbit.io/constellation": "alpha", "orbit.io/orbit": "leo"},
			reported:    map[string]string{"orbit.io/orbit": "leo"},
			want:        map[string]string{"orbit.io/orbit": "leo"},
			wantChanges: []string{"-orbit.io/constellation"},
		},
		{
			name:        "human labels are kept",
			current:     map[string]string{"team": "ops", "orbit.io/constellation": "alpha"},
			reported:    map[string]string{},
			want:        map[string]string{"team": "ops"},
			wantChanges: []string{"-orbit.io/constellation"},
		},
		{
			name:         "foreign prefix from the agent is rejected",
			current:      map[string]string{"team": "ops"},
			reported:     map[string]string{"team": "evil", "kubernetes.io/hostname": "x"},
			want:         map[string]string{"team": "ops"},
			wantRejected: []string{"kubernetes.io/hostname", "team"},
		},
		{
			name:         "invalid value is rejected",
			current:      nil,
			reported:     map[string]string{"orbit.io/constellation": "not valid!"},
			want:         map[string]string{},
			wantRejected: []string{"orbit.io/constellation"},
		},
		{
			name:        "changes are sorted",
			current:     nil,
			reported:    map[string]string{"orbit.io/b": "2", "orbit.io/a": "1"},
			want:        map[string]string{"orbit.io/a": "1", "orbit.io/b": "2"},
			wantChanges: []string{"+orbit.io/a=1", "+orbit.io/b=2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := maps.Clone(tt.current)
			got, changes, rejected := SyncLabels(tt.current, tt.reported)
			if !maps.Equal(got, tt.want) {
				t.Errorf("labels = %v, want %v", got, tt.want)
			}
			if !slices.Equal(changes, tt.wantChanges) {
				t.Errorf("changes = %q, want %q", changes, tt.wantChanges)
			}
			if !slices.Equal(rejected, tt.wantRejected) {
				t.Errorf("rejected = %q, want %q", rejected, tt.wantRejected)
			}
			if !maps.Equal(tt.current, before) {
				t.Errorf("SyncLabels modified its input: %v", tt.current)
			}
		})
	}
}

func TestValidNodeID(t *testing.T) {
	for id, want := range map[string]bool{
		"sat-1": true, "a": true,
		"": false, "Sat-1": false, "sat.1": false, "sat_1": false, "*": false, ">": false,
		"-sat": false, string(make([]byte, 64)): false,
	} {
		if got := ValidNodeID(id); got != want {
			t.Errorf("ValidNodeID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestValidPhase(t *testing.T) {
	for p, want := range map[string]bool{
		"": true, "idle": true, "applying": true, "verifying": true, "rolled-back": true,
		"Idle": false, "unknown": false,
	} {
		if got := ValidPhase(p); got != want {
			t.Errorf("ValidPhase(%q) = %v, want %v", p, got, want)
		}
	}
}
