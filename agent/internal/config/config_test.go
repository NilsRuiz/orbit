package config

import (
	"maps"
	"strings"
	"testing"
)

func TestParseNodeLabels(t *testing.T) {
	long := strings.Repeat("a", 64)
	tests := []struct {
		name    string
		in      string
		want    map[string]string
		wantErr string // substring; empty means success
	}{
		{name: "unset", in: "", want: map[string]string{}},
		{name: "one label", in: "orbit.io/constellation=alpha", want: map[string]string{"orbit.io/constellation": "alpha"}},
		{
			name: "several labels, spaces trimmed",
			in:   " orbit.io/constellation=alpha , orbit.io/plane=2 ",
			want: map[string]string{"orbit.io/constellation": "alpha", "orbit.io/plane": "2"},
		},
		{name: "empty value allowed", in: "orbit.io/canary=", want: map[string]string{"orbit.io/canary": ""}},
		{name: "trailing comma", in: "orbit.io/plane=2,", want: map[string]string{"orbit.io/plane": "2"}},
		{name: "missing =", in: "orbit.io/plane", wantErr: `"orbit.io/plane": want key=value`},
		{name: "foreign prefix", in: "example.com/team=a", wantErr: "must start with orbit.io/"},
		{name: "no prefix", in: "team=a", wantErr: "must start with orbit.io/"},
		{name: "empty name", in: "orbit.io/=a", wantErr: "invalid label name"},
		{name: "name with slash", in: "orbit.io/a/b=c", wantErr: "invalid label name"},
		{name: "name too long", in: "orbit.io/" + long + "=x", wantErr: "invalid label name"},
		{name: "value too long", in: "orbit.io/a=" + long, wantErr: "invalid label value"},
		{name: "value with space", in: "orbit.io/a=b c", wantErr: "invalid label value"},
		{name: "value starting with dash", in: "orbit.io/a=-b", wantErr: "invalid label value"},
		{name: "duplicate key", in: "orbit.io/a=1,orbit.io/a=2", wantErr: "duplicate label"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseNodeLabels(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !maps.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateNodeID(t *testing.T) {
	tests := []struct {
		id string
		ok bool
	}{
		{"sat-1", true},
		{"a", true},
		{strings.Repeat("a", 63), true},
		{strings.Repeat("a", 64), false},
		{"Sat-1", false}, // object names are lowercase
		{"sat.1", false}, // a dot would split the NATS subject token
		{"sat_1", false}, // not allowed in a DNS label
		{"-sat", false},  // must start with an alphanumeric
		{"sat-", false},  // must end with an alphanumeric
		{"sat*1", false}, // NATS wildcard
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if err := ValidateNodeID(tt.id); (err == nil) != tt.ok {
				t.Fatalf("ValidateNodeID(%q) = %v, want ok=%v", tt.id, err, tt.ok)
			}
		})
	}
}

func TestFromEnvRejectsInvalidSettings(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{name: "invalid node id", env: map[string]string{"ORBIT_NODE_ID": "sat.1"}, wantErr: "ORBIT_NODE_ID"},
		{name: "invalid labels", env: map[string]string{"ORBIT_NODE_ID": "sat-1", "ORBIT_NODE_LABELS": "team=a"}, wantErr: "ORBIT_NODE_LABELS"},
		{name: "unknown payload driver", env: map[string]string{"ORBIT_NODE_ID": "sat-1", "ORBIT_PAYLOAD": "docker"}, wantErr: "ORBIT_PAYLOAD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %s", err, tt.wantErr)
			}
		})
	}
	t.Run("valid labels are loaded", func(t *testing.T) {
		t.Setenv("ORBIT_NODE_ID", "sat-1")
		t.Setenv("ORBIT_NODE_LABELS", "orbit.io/constellation=alpha")
		c, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if c.NodeLabels["orbit.io/constellation"] != "alpha" {
			t.Fatalf("NodeLabels = %v", c.NodeLabels)
		}
	})
}

func TestFromEnvPayload(t *testing.T) {
	t.Run("defaults to the simulator", func(t *testing.T) {
		t.Setenv("ORBIT_NODE_ID", "sat-1")
		c, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if c.Payload != PayloadSim || c.PayloadNamespace != "payload" || c.PayloadName != "payload" {
			t.Fatalf("payload = %q %q/%q, want sim payload/payload", c.Payload, c.PayloadNamespace, c.PayloadName)
		}
	})
	t.Run("k3s driver with its objects", func(t *testing.T) {
		t.Setenv("ORBIT_NODE_ID", "sat-1")
		t.Setenv("ORBIT_PAYLOAD", "k3s")
		t.Setenv("ORBIT_PAYLOAD_NAMESPACE", "workloads")
		t.Setenv("ORBIT_PAYLOAD_NAME", "camera")
		c, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if c.Payload != PayloadK3s || c.PayloadNamespace != "workloads" || c.PayloadName != "camera" {
			t.Fatalf("payload = %q %q/%q, want k3s workloads/camera", c.Payload, c.PayloadNamespace, c.PayloadName)
		}
	})
}
