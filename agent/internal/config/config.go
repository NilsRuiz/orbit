// Package config loads agent settings from environment variables (12-factor).
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Payload drivers (internal/payload).
const (
	PayloadSim = "sim" // simulator, for docker compose
	PayloadK3s = "k3s" // Flux OCIRepository tag on the node's cluster (ADR-0009)
)

// Config is the agent configuration, read from ORBIT_* environment variables.
type Config struct {
	NodeID            string
	NATSURL           string
	NATSCredsFile     string
	JetStreamDomain   string
	StateDir          string
	InitialVersion    string
	HeartbeatInterval time.Duration
	VerifyTimeout     time.Duration
	LogLevel          string
	SimBadVersions    []string
	// Payload selects the driver; with k3s, PayloadNamespace/PayloadName name the
	// OCIRepository, the Kustomization and the Deployment of the payload.
	Payload          string
	PayloadNamespace string
	PayloadName      string
	// NodeLabels are sent in heartbeats; the operator mirrors them on the EdgeNode.
	NodeLabels map[string]string
}

// FromEnv loads Config from the environment, applying defaults; ORBIT_NODE_ID is required.
func FromEnv() (Config, error) {
	c := Config{
		NodeID:        os.Getenv("ORBIT_NODE_ID"),
		NATSURL:       getenv("ORBIT_NATS_URL", "nats://127.0.0.1:4222"),
		NATSCredsFile: os.Getenv("ORBIT_NATS_CREDS"),
		// Hub JetStream domain, reached through the local leaf node. Set to "" when
		// connecting straight to the hub.
		JetStreamDomain: getenv("ORBIT_JS_DOMAIN", "hub"),
		StateDir:        getenv("ORBIT_STATE_DIR", "/var/lib/orbit"),
		InitialVersion:  getenv("ORBIT_INITIAL_VERSION", "1.0.0"),
		LogLevel:        getenv("ORBIT_LOG_LEVEL", "info"),

		Payload:          getenv("ORBIT_PAYLOAD", PayloadSim),
		PayloadNamespace: getenv("ORBIT_PAYLOAD_NAMESPACE", "payload"),
		PayloadName:      getenv("ORBIT_PAYLOAD_NAME", "payload"),
	}
	if c.NodeID == "" {
		return c, fmt.Errorf("ORBIT_NODE_ID is required")
	}
	if err := ValidateNodeID(c.NodeID); err != nil {
		return c, fmt.Errorf("ORBIT_NODE_ID: %w", err)
	}
	if c.Payload != PayloadSim && c.Payload != PayloadK3s {
		return c, fmt.Errorf("ORBIT_PAYLOAD: %q is not %q or %q", c.Payload, PayloadSim, PayloadK3s)
	}
	var err error
	if c.NodeLabels, err = ParseNodeLabels(os.Getenv("ORBIT_NODE_LABELS")); err != nil {
		return c, fmt.Errorf("ORBIT_NODE_LABELS: %w", err)
	}
	if c.HeartbeatInterval, err = time.ParseDuration(getenv("ORBIT_HEARTBEAT_INTERVAL", "10s")); err != nil {
		return c, fmt.Errorf("ORBIT_HEARTBEAT_INTERVAL: %w", err)
	}
	if c.VerifyTimeout, err = time.ParseDuration(getenv("ORBIT_VERIFY_TIMEOUT", "30s")); err != nil {
		return c, fmt.Errorf("ORBIT_VERIFY_TIMEOUT: %w", err)
	}
	if v := os.Getenv("ORBIT_SIM_BAD_VERSIONS"); v != "" {
		c.SimBadVersions = strings.Split(v, ",")
	}
	return c, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
