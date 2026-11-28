package payload

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/NilsRuiz/orbit/agent/internal/reconcile"
)

// The tests run against a real kube-apiserver (envtest) with the pinned Flux CRDs, so a
// wrong field path in the patch is pruned by the schema and fails the test. They play the
// Flux controllers' part by writing the statuses themselves.

var client dynamic.Interface

func TestMain(m *testing.M) {
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"testdata/crds"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest (run `make -C agent test`, which downloads it): %v\n", err)
		os.Exit(1)
	}
	client, err = dynamic.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dynamic client: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
	}
	os.Exit(code)
}

var nsCount atomic.Int32

// newNamespace creates a namespace of its own for one test case.
func newNamespace(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("payload-%d", nsCount.Add(1))
	ns := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": name},
	}}
	_, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).
		Create(context.Background(), ns, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	return name
}

// world describes what the Flux controllers and the Deployment report. healthyWorld is a
// world where version is running; each case breaks one thing.
type world struct {
	tag, artifact            string
	sourceObserved           int64
	applied                  string
	ksReady                  string
	ksObserved               int64
	deployObserved           int64
	replicas, updated, total int64
	available                int64
	skipKustomization        bool
}

const (
	version  = "1.1.0"
	revision = version + "@sha256:0123456789abcdef"
)

func healthyWorld() world {
	return world{
		tag: version, artifact: revision, sourceObserved: 1,
		applied: revision, ksReady: "True", ksObserved: 1,
		deployObserved: 1, replicas: 1, updated: 1, total: 1, available: 1,
	}
}

// create makes the three objects in ns and sets their statuses from w (generation is 1).
func (w world) create(t *testing.T, ns string) {
	t.Helper()
	src := obj("source.toolkit.fluxcd.io/v1", "OCIRepository", ns, map[string]any{
		"interval": "1m", "url": "oci://example.test/orbit-payload", "ref": map[string]any{"tag": w.tag},
	})
	src = mustCreate(t, OCIRepositoryGVR, src)
	src.Object["status"] = map[string]any{
		"observedGeneration": w.sourceObserved,
		"artifact": map[string]any{
			"revision": w.artifact, "digest": "sha256:0123456789abcdef", "path": "x", "url": "http://x",
			"lastUpdateTime": "2026-11-27T00:00:00Z",
		},
	}
	mustUpdateStatus(t, OCIRepositoryGVR, src)

	if !w.skipKustomization {
		ks := obj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", ns, map[string]any{
			"interval": "10m", "prune": true, "path": "./",
			"sourceRef": map[string]any{"kind": "OCIRepository", "name": "payload"},
		})
		ks = mustCreate(t, KustomizationGVR, ks)
		ks.Object["status"] = map[string]any{
			"observedGeneration":  w.ksObserved,
			"lastAppliedRevision": w.applied,
			"conditions": []any{map[string]any{
				"type": "Ready", "status": w.ksReady, "reason": "Test", "message": "test",
				"lastTransitionTime": "2026-11-27T00:00:00Z",
			}},
		}
		mustUpdateStatus(t, KustomizationGVR, ks)
	}

	d := obj("apps/v1", "Deployment", ns, map[string]any{
		"replicas": w.replicas,
		"selector": map[string]any{"matchLabels": map[string]any{"app": "payload"}},
		"template": map[string]any{
			"metadata": map[string]any{"labels": map[string]any{"app": "payload"}},
			"spec": map[string]any{"containers": []any{map[string]any{
				"name": "podinfo", "image": "ghcr.io/stefanprodan/podinfo:6.7.1",
			}}},
		},
	})
	d = mustCreate(t, DeploymentGVR, d)
	d.Object["status"] = map[string]any{
		"observedGeneration": w.deployObserved, "replicas": w.total,
		"updatedReplicas": w.updated, "availableReplicas": w.available, "readyReplicas": w.available,
	}
	mustUpdateStatus(t, DeploymentGVR, d)
}

func obj(apiVersion, kind, ns string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"name": "payload", "namespace": ns},
		"spec":     spec,
	}}
}

func mustCreate(t *testing.T, gvr schema.GroupVersionResource, u *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	out, err := client.Resource(gvr).Namespace(u.GetNamespace()).Create(context.Background(), u, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create %s: %v", gvr.Resource, err)
	}
	return out
}

func mustUpdateStatus(t *testing.T, gvr schema.GroupVersionResource, u *unstructured.Unstructured) {
	t.Helper()
	if _, err := client.Resource(gvr).Namespace(u.GetNamespace()).UpdateStatus(context.Background(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update %s status: %v", gvr.Resource, err)
	}
}

func driver(ns string) K3s { return K3s{Client: client, Namespace: ns, Name: "payload"} }

func TestK3sHealthy(t *testing.T) {
	tests := []struct {
		name   string
		change func(*world)
		want   bool
	}{
		{"running the version", func(*world) {}, true},
		{"source points at another tag", func(w *world) { w.tag = "1.0.0" }, false},
		{"source has not seen the new tag", func(w *world) { w.sourceObserved = 0 }, false},
		// The Kustomization applied what the source holds: only the revision check can refuse.
		{"source artifact is the previous version", func(w *world) {
			w.artifact, w.applied = "1.0.0@sha256:fedcba", "1.0.0@sha256:fedcba"
		}, false},
		{"source artifact only shares a prefix", func(w *world) {
			w.artifact, w.applied = "1.1.0-rc.1@sha256:fedcba", "1.1.0-rc.1@sha256:fedcba"
		}, false},
		{"kustomization applied the previous revision", func(w *world) { w.applied = "1.0.0@sha256:fedcba" }, false},
		{"kustomization failed", func(w *world) { w.ksReady = "False" }, false},
		{"kustomization has not observed its spec", func(w *world) { w.ksObserved = 0 }, false},
		{"deployment has not observed its spec", func(w *world) { w.deployObserved = 0 }, false},
		{"deployment not updated yet", func(w *world) { w.updated = 0; w.available = 1 }, false},
		// Scaling up: no old replica left, but the second one does not exist yet.
		{"fewer updated replicas than wanted", func(w *world) {
			w.replicas, w.total, w.updated, w.available = 2, 1, 1, 1
		}, false},
		// Seen on k3d with 6.6.6: the old pod is still available, the new one never ready.
		{"new pod not ready, old pod still serving", func(w *world) { w.total = 2; w.updated = 1; w.available = 1 }, false},
		{"updated pod not available", func(w *world) { w.available = 0 }, false},
		{"scaled to two, both updated and available", func(w *world) {
			w.replicas, w.total, w.updated, w.available = 2, 2, 2, 2
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns := newNamespace(t)
			w := healthyWorld()
			tt.change(&w)
			w.create(t, ns)

			got, err := driver(ns).Healthy(context.Background(), version)
			if got != tt.want {
				t.Errorf("Healthy = %v (%v), want %v", got, err, tt.want)
			}
			// Not healthy always comes with a NotReady reason; healthy never does.
			if (err == nil) != got {
				t.Errorf("Healthy = %v with error %v: want a reason exactly when not healthy", got, err)
			}
			if !got && !reconcile.IsNotReady(err) {
				t.Errorf("not healthy with %v, want a reconcile.NotReady reason", err)
			}
		})
	}
}

func TestK3sHealthyMissingObjects(t *testing.T) {
	t.Run("no source", func(t *testing.T) {
		ns := newNamespace(t)
		_, err := driver(ns).Healthy(context.Background(), version)
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("Healthy error = %v, want not found", err)
		}
		// A missing object is a broken setup, not a payload state.
		if reconcile.IsNotReady(err) {
			t.Errorf("a missing source is reported as NotReady: %v", err)
		}
	})
	t.Run("no kustomization", func(t *testing.T) {
		ns := newNamespace(t)
		w := healthyWorld()
		w.skipKustomization = true
		w.create(t, ns)
		_, err := driver(ns).Healthy(context.Background(), version)
		if err == nil || !strings.Contains(err.Error(), "kustomizations") {
			t.Fatalf("Healthy error = %v, want kustomization not found", err)
		}
	})
}

func TestK3sApply(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)
	w := healthyWorld()
	w.tag = "1.0.0"
	w.create(t, ns)
	d := driver(ns)

	for range 2 { // idempotent
		if err := d.Apply(ctx, version); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
	src, err := client.Resource(OCIRepositoryGVR).Namespace(ns).Get(ctx, "payload", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if tag, _, _ := unstructured.NestedString(src.Object, "spec", "ref", "tag"); tag != version {
		t.Errorf("source tag = %q, want %q", tag, version)
	}
	// The patch only touches the tag: the rest of the spec (owned by Git) is kept.
	if url, _, _ := unstructured.NestedString(src.Object, "spec", "url"); url != "oci://example.test/orbit-payload" {
		t.Errorf("source url = %q, the patch changed it", url)
	}
	// A tag change is a spec change: Flux reconciles it immediately (generation bump).
	if src.GetGeneration() != 2 {
		t.Errorf("generation = %d, want 2 after one effective change", src.GetGeneration())
	}

	if err := driver(newNamespace(t)).Apply(ctx, version); err == nil {
		t.Error("Apply without an OCIRepository succeeded, want an error")
	}
}
