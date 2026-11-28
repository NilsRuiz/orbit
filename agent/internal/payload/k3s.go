package payload

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/NilsRuiz/orbit/agent/internal/reconcile"
)

// Flux and Kubernetes resources the driver touches (ADR-0009). The API versions are
// pinned: health relies on their status fields.
var (
	OCIRepositoryGVR = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "ocirepositories"}
	KustomizationGVR = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	DeploymentGVR    = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

// K3s deploys payload versions on the node's cluster through Flux (ADR-0009): a version is
// an OCI artifact, selected by the tag of the payload OCIRepository. The OCIRepository,
// the Kustomization applying it and the Deployment it contains share one name.
type K3s struct {
	Client    dynamic.Interface
	Namespace string
	Name      string
}

// NewK3s connects to the node's cluster: in-cluster service account first, else the
// KUBECONFIG file (development, outside the cluster).
func NewK3s(namespace, name string) (K3s, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			return K3s{}, fmt.Errorf("not in a cluster and KUBECONFIG unset: %w", err)
		}
		if cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig); err != nil {
			return K3s{}, fmt.Errorf("load KUBECONFIG: %w", err)
		}
	}
	cfg.UserAgent = "orbit-agent"
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return K3s{}, fmt.Errorf("kubernetes client: %w", err)
	}
	return K3s{Client: client, Namespace: namespace, Name: name}, nil
}

// Apply points the payload OCIRepository at version. Flux then fetches and applies it; a
// rollback is the same call with the previous version. It is idempotent.
func (k K3s) Apply(ctx context.Context, version string) error {
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"ref": map[string]any{"tag": version}}})
	if err != nil {
		return fmt.Errorf("build patch: %w", err)
	}
	_, err = k.Client.Resource(OCIRepositoryGVR).Namespace(k.Namespace).
		Patch(ctx, k.Name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "orbit-agent"})
	if err != nil {
		return fmt.Errorf("set %s/%s OCIRepository tag to %s: %w", k.Namespace, k.Name, version, err)
	}
	return nil
}

// Healthy reports whether version is running: the source fetched it, the Kustomization
// applied that exact revision, and the Deployment finished rolling it out. When it is not,
// the error says why: reconcile.NotReady with the payload's state, e.g. "deployment: 1 old
// replicas pending termination" for a new pod that never becomes ready, or a plain error
// when an API call failed.
func (k K3s) Healthy(ctx context.Context, version string) (bool, error) {
	reason, err := k.notReady(ctx, version)
	if err != nil {
		return false, err
	}
	if reason != "" {
		return false, reconcile.NotReady(reason)
	}
	return true, nil
}

// notReady returns why version is not running yet, or "" when it is.
func (k K3s) notReady(ctx context.Context, version string) (string, error) {
	src, err := k.get(ctx, OCIRepositoryGVR)
	if err != nil {
		return "", err
	}
	if tag, _, _ := unstructured.NestedString(src.Object, "spec", "ref", "tag"); tag != version {
		return fmt.Sprintf("source tag is %q", tag), nil
	}
	if !observed(src) {
		return "source has not observed its latest spec", nil
	}
	// Flux revisions of an OCI tag are "<tag>@sha256:<digest>".
	revision, _, _ := unstructured.NestedString(src.Object, "status", "artifact", "revision")
	if !strings.HasPrefix(revision, version+"@") {
		return fmt.Sprintf("source artifact is %q", revision), nil
	}

	ks, err := k.get(ctx, KustomizationGVR)
	if err != nil {
		return "", err
	}
	if applied, _, _ := unstructured.NestedString(ks.Object, "status", "lastAppliedRevision"); applied != revision {
		return fmt.Sprintf("kustomization applied %q", applied), nil
	}
	if !observed(ks) || !ready(ks) {
		return "kustomization is not ready", nil
	}

	obj, err := k.get(ctx, DeploymentGVR)
	if err != nil {
		return "", err
	}
	var d appsv1.Deployment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &d); err != nil {
		return "", fmt.Errorf("decode deployment %s/%s: %w", k.Namespace, k.Name, err)
	}
	return rolloutPending(d), nil
}

// rolloutPending mirrors `kubectl rollout status`: every replica runs the new template and
// is available, and no replica of an older one is left. Old pods count as available, so
// availableReplicas alone would report a stuck rollout as done.
func rolloutPending(d appsv1.Deployment) string {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	s := d.Status
	switch {
	case s.ObservedGeneration < d.Generation:
		return "deployment has not observed its latest spec"
	case s.UpdatedReplicas < want:
		return fmt.Sprintf("deployment: %d of %d replicas updated", s.UpdatedReplicas, want)
	case s.Replicas > s.UpdatedReplicas:
		return fmt.Sprintf("deployment: %d old replicas pending termination", s.Replicas-s.UpdatedReplicas)
	case s.AvailableReplicas < s.UpdatedReplicas:
		return fmt.Sprintf("deployment: %d of %d updated replicas available", s.AvailableReplicas, s.UpdatedReplicas)
	}
	return ""
}

func (k K3s) get(ctx context.Context, gvr schema.GroupVersionResource) (*unstructured.Unstructured, error) {
	obj, err := k.Client.Resource(gvr).Namespace(k.Namespace).Get(ctx, k.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%s %s/%s not found: is the payload GitOps applied?", gvr.Resource, k.Namespace, k.Name)
	}
	if err != nil {
		return nil, fmt.Errorf("get %s %s/%s: %w", gvr.Resource, k.Namespace, k.Name, err)
	}
	return obj, nil
}

// observed reports whether the controller's status reflects the object's current spec.
func observed(u *unstructured.Unstructured) bool {
	g, _, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	return g >= u.GetGeneration()
}

// ready reports whether the Ready condition is True.
func ready(u *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if ok && m["type"] == "Ready" {
			return m["status"] == "True"
		}
	}
	return false
}
