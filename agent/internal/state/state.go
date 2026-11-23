// Package state persists the agent's A/B deployment state on local disk so a node
// survives reboots (and spot preemptions) without forgetting what it was running.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Phase of the node's deployment state machine.
type Phase string

// Phases of the state machine: idle → applying → verifying → idle | rolled-back.
const (
	PhaseIdle       Phase = "idle"        // running Current, nothing in progress
	PhaseApplying   Phase = "applying"    // switching to Pending
	PhaseVerifying  Phase = "verifying"   // Pending applied, dead man's switch armed
	PhaseRolledBack Phase = "rolled-back" // Pending failed, back on Current
)

// State is the A/B slot state. Current is the last known-good version.
type State struct {
	Current   string `json:"current"`
	Previous  string `json:"previous,omitempty"`
	Pending   string `json:"pending,omitempty"`
	Phase     Phase  `json:"phase"`
	RolloutID string `json:"rolloutId,omitempty"`
	// LastFailed remembers the last version that failed verification so the agent does
	// not loop on a bad desired state until the hub publishes something new.
	LastFailed string `json:"lastFailed,omitempty"`
}

// Store reads and writes State atomically.
type Store struct {
	path string
}

// NewStore creates dir if needed and stores state in dir/state.json.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	return &Store{path: filepath.Join(dir, "state.json")}, nil
}

// Load returns the persisted state, or an idle state on first boot.
func (s *Store) Load(initialVersion string) (State, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return State{Current: initialVersion, Phase: PhaseIdle}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read state: %w", err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("decode state: %w", err)
	}
	return st, nil
}

// Save writes via a temp file + rename so a crash never leaves a half-written file.
func (s *Store) Save(st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	// 0600: only the agent (nonroot) reads its own state.
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return os.Rename(tmp, s.path)
}
