package desired

import "testing"

func TestPlanEnsure(t *testing.T) {
	const id, target = "payload-g2", "1.2.0"
	tests := []struct {
		name      string
		entry     Entry
		fallback  string
		want      Value
		wantWrite bool
	}{
		{
			name:      "no entry: previous is the version the node reports",
			entry:     Entry{},
			fallback:  "1.0.0",
			want:      Value{Version: target, RolloutID: id, PreviousVersion: "1.0.0"},
			wantWrite: true,
		},
		{
			name:      "no entry, node never seen: no previous",
			entry:     Entry{},
			want:      Value{Version: target, RolloutID: id},
			wantWrite: true,
		},
		{
			name:      "previous desired state wins over the reported version",
			entry:     Entry{Exists: true, Revision: 4, Value: Value{Version: "1.1.0", RolloutID: "manual"}},
			fallback:  "1.0.0",
			want:      Value{Version: target, RolloutID: id, PreviousVersion: "1.1.0"},
			wantWrite: true,
		},
		{
			name:  "already written by this rollout: no write",
			entry: Entry{Exists: true, Revision: 5, Value: Value{Version: target, RolloutID: id, PreviousVersion: "1.1.0"}},
			want:  Value{Version: target, RolloutID: id, PreviousVersion: "1.1.0"},
		},
		{
			// Same version set by someone else: the rollout takes it over, so that its
			// revert recognises the value and nodes report this rollout's id.
			name:      "same version written by another writer",
			entry:     Entry{Exists: true, Value: Value{Version: target, RolloutID: "manual"}},
			want:      Value{Version: target, RolloutID: id, PreviousVersion: target},
			wantWrite: true,
		},
		{
			// A later generation of the same Rollout reverts to the state before this
			// generation: the superseded target. If that version had failed, the agent
			// remembers it (LastFailed) and stays on its healthy version.
			name:      "superseded generation of the same rollout",
			entry:     Entry{Exists: true, Value: Value{Version: "1.1.5", RolloutID: "payload-g1", PreviousVersion: "1.1.0"}},
			want:      Value{Version: target, RolloutID: id, PreviousVersion: "1.1.5"},
			wantWrite: true,
		},
		{
			name:      "reverted value of an earlier rollout",
			entry:     Entry{Exists: true, Value: Value{Version: "1.1.0", RolloutID: "payload-g1" + RevertSuffix}},
			want:      Value{Version: target, RolloutID: id, PreviousVersion: "1.1.0"},
			wantWrite: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, write := planEnsure(tt.entry, target, id, tt.fallback)
			if got != tt.want || write != tt.wantWrite {
				t.Errorf("planEnsure = %+v, %v; want %+v, %v", got, write, tt.want, tt.wantWrite)
			}
		})
	}
}

func TestPlanRevert(t *testing.T) {
	const id = "payload-g2"
	tests := []struct {
		name       string
		entry      Entry
		wantAction revertAction
		want       Value
	}{
		{"no entry", Entry{}, revertNone, Value{}},
		{
			name:       "restore the previous version",
			entry:      Entry{Exists: true, Value: Value{Version: "1.2.0", RolloutID: id, PreviousVersion: "1.1.0"}},
			wantAction: revertPut,
			want:       Value{Version: "1.1.0", RolloutID: id + RevertSuffix},
		},
		{
			// The node had nothing desired before: removing the key leaves it on the
			// version it runs (the agent ignores deletes).
			name:       "no previous version",
			entry:      Entry{Exists: true, Value: Value{Version: "1.2.0", RolloutID: id}},
			wantAction: revertDelete,
		},
		{
			name:       "already reverted",
			entry:      Entry{Exists: true, Value: Value{Version: "1.1.0", RolloutID: id + RevertSuffix}},
			wantAction: revertNone,
		},
		{
			// Never undo what another rollout, or a human, wrote since.
			name:       "entry of another writer",
			entry:      Entry{Exists: true, Value: Value{Version: "1.3.0", RolloutID: "manual", PreviousVersion: "1.2.0"}},
			wantAction: revertNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, got := planRevert(tt.entry, id)
			if action != tt.wantAction || got != tt.want {
				t.Errorf("planRevert = %v, %+v; want %v, %+v", action, got, tt.wantAction, tt.want)
			}
		})
	}
}
