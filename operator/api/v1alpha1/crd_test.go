package v1alpha1_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
)

// k8s is a client to a real kube-apiserver (envtest) with the generated CRDs installed.
var k8s client.Client

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		// Fail loudly rather than skip: these tests guard the API schema.
		println("KUBEBUILDER_ASSETS is not set: run `make test` in operator/ (downloads envtest binaries)")
		os.Exit(1)
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		println("start envtest:", err.Error())
		os.Exit(1)
	}
	scheme := runtime.NewScheme()
	if err := fleetv1alpha1.AddToScheme(scheme); err != nil {
		println("scheme:", err.Error())
		os.Exit(1)
	}
	if k8s, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		println("client:", err.Error())
		os.Exit(1)
	}
	code := m.Run()
	_ = env.Stop()
	os.Exit(code)
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

// decodeSample reads one of config/samples into typed object obj.
func decodeSample(t *testing.T, file string, obj client.Object) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "config", "samples", file))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := yaml.NewYAMLOrJSONDecoder(f, 4096).Decode(obj); err != nil {
		t.Fatalf("decode %s: %v", file, err)
	}
}

func TestSamplesAreAccepted(t *testing.T) {
	tests := []struct {
		file string
		obj  client.Object
	}{
		{"fleet_v1alpha1_edgenode.yaml", &fleetv1alpha1.EdgeNode{}},
		{"fleet_v1alpha1_fleet.yaml", &fleetv1alpha1.Fleet{}},
		{"fleet_v1alpha1_rollout.yaml", &fleetv1alpha1.Rollout{}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			decodeSample(t, tt.file, tt.obj)
			if err := k8s.Create(ctx(t), tt.obj); err != nil {
				t.Fatalf("sample rejected by the API: %v", err)
			}
			t.Cleanup(func() { _ = k8s.Delete(context.Background(), tt.obj) })
		})
	}
}

func TestDefaults(t *testing.T) {
	t.Run("EdgeNode contactInterval", func(t *testing.T) {
		n := &fleetv1alpha1.EdgeNode{ObjectMeta: metav1.ObjectMeta{Name: "defaults-node"}}
		if err := k8s.Create(ctx(t), n); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = k8s.Delete(context.Background(), n) })
		if got := n.Spec.ContactInterval.Duration; got != 5*time.Minute {
			t.Errorf("contactInterval = %v, want 5m", got)
		}
	})
	t.Run("Rollout healthGate", func(t *testing.T) {
		r := rollout("defaults-rollout", 100)
		if err := k8s.Create(ctx(t), r); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = k8s.Delete(context.Background(), r) })
		if g := r.Spec.HealthGate; g.MaxFailurePercent != 0 || g.Timeout.Duration != 10*time.Minute {
			t.Errorf("healthGate = %+v, want maxFailurePercent 0, timeout 10m", g)
		}
	})
	t.Run("Rollout healthGate timeout when only maxFailurePercent is set", func(t *testing.T) {
		r := rollout("defaults-rollout-partial-gate", 100)
		r.Spec.HealthGate.MaxFailurePercent = 10
		if err := k8s.Create(ctx(t), r); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = k8s.Delete(context.Background(), r) })
		if got := r.Spec.HealthGate.Timeout.Duration; got != 10*time.Minute {
			t.Errorf("timeout = %v, want the 10m default", got)
		}
	})
}

func rollout(name string, percents ...int32) *fleetv1alpha1.Rollout {
	r := &fleetv1alpha1.Rollout{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       fleetv1alpha1.RolloutSpec{FleetRef: "constellation-alpha", TargetVersion: "1.2.0"},
	}
	for _, p := range percents {
		r.Spec.Waves = append(r.Spec.Waves, fleetv1alpha1.Wave{Percent: p})
	}
	return r
}

func TestRolloutValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*fleetv1alpha1.Rollout)
		wantErr string // substring of the API error; empty means accepted
	}{
		{name: "single full wave", mutate: func(*fleetv1alpha1.Rollout) {}},
		{name: "increasing waves", mutate: func(r *fleetv1alpha1.Rollout) { r.Spec.Waves = rollout("", 10, 50, 100).Spec.Waves }},
		{name: "decreasing waves", mutate: func(r *fleetv1alpha1.Rollout) { r.Spec.Waves = rollout("", 50, 10, 100).Spec.Waves }, wantErr: "wave percents must not decrease"},
		{name: "last wave below 100", mutate: func(r *fleetv1alpha1.Rollout) { r.Spec.Waves = rollout("", 10, 50).Spec.Waves }, wantErr: "the last wave must reach 100 percent"},
		{name: "percent 0", mutate: func(r *fleetv1alpha1.Rollout) { r.Spec.Waves = rollout("", 0, 100).Spec.Waves }, wantErr: "should be greater than or equal to 1"},
		{name: "no waves", mutate: func(r *fleetv1alpha1.Rollout) { r.Spec.Waves = nil }, wantErr: "spec.waves"},
		{name: "empty target version", mutate: func(r *fleetv1alpha1.Rollout) { r.Spec.TargetVersion = "" }, wantErr: "spec.targetVersion"},
		{name: "failure percent above 100", mutate: func(r *fleetv1alpha1.Rollout) { r.Spec.HealthGate.MaxFailurePercent = 101 }, wantErr: "should be less than or equal to 100"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := rollout(strings.ReplaceAll(strings.ToLower(tt.name), " ", "-")+"-"+string(rune('a'+i)), 100)
			tt.mutate(r)
			err := k8s.Create(ctx(t), r)
			if err == nil {
				t.Cleanup(func() { _ = k8s.Delete(context.Background(), r) })
			}
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("rejected: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("accepted, want error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestEdgeNodeStatusValidation(t *testing.T) {
	tests := []struct {
		name    string
		status  fleetv1alpha1.EdgeNodeStatus
		wantErr string
	}{
		{name: "known phase and reachability", status: fleetv1alpha1.EdgeNodeStatus{Phase: "idle", Reachability: fleetv1alpha1.Reachable}},
		{name: "unknown phase", status: fleetv1alpha1.EdgeNodeStatus{Phase: "exploded"}, wantErr: "status.phase"},
		{name: "unknown reachability", status: fleetv1alpha1.EdgeNodeStatus{Reachability: "Maybe"}, wantErr: "status.reachability"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &fleetv1alpha1.EdgeNode{ObjectMeta: metav1.ObjectMeta{Name: "status-node-" + string(rune('a'+i))}}
			if err := k8s.Create(ctx(t), n); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = k8s.Delete(context.Background(), n) })
			n.Status = tt.status
			err := k8s.Status().Update(ctx(t), n)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("rejected: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
