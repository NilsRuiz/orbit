package controller

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
	"github.com/NilsRuiz/orbit/operator/internal/testenv"
)

var (
	k8s client.Client
	// cfg reaches the envtest API server, for tests that run a manager.
	cfg *rest.Config
)

func TestMain(m *testing.M) {
	var stop func()
	var err error
	cfg, stop, err = testenv.Start()
	if err != nil {
		println("envtest:", err.Error())
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
	stop()
	os.Exit(code)
}

var t0 = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

func TestEdgeNodeReachability(t *testing.T) {
	tests := []struct {
		name        string
		stored      fleetv1alpha1.Reachability
		lastSeen    *time.Time
		want        fleetv1alpha1.Reachability
		wantRequeue time.Duration
		wantEvent   string
	}{
		// Default contactInterval 5m: Unreachable after 10m of silence.
		{"recent contact", fleetv1alpha1.Reachable, new(t0.Add(-4 * time.Minute)), fleetv1alpha1.Reachable, 6 * time.Minute, ""},
		{"silent too long", fleetv1alpha1.Reachable, new(t0.Add(-11 * time.Minute)), fleetv1alpha1.Unreachable, 0, "Normal ContactLost"},
		{"already unreachable", fleetv1alpha1.Unreachable, new(t0.Add(-time.Hour)), fleetv1alpha1.Unreachable, 0, ""},
		{"never seen", "", nil, fleetv1alpha1.Unreachable, 0, ""},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			name := "reach-" + strings.ReplaceAll(tt.name, " ", "-")
			en := &fleetv1alpha1.EdgeNode{ObjectMeta: metav1.ObjectMeta{Name: name}}
			if err := k8s.Create(ctx, en); err != nil {
				t.Fatal(err)
			}
			en.Status.Reachability = tt.stored
			if tt.lastSeen != nil {
				en.Status.LastSeen = &metav1.Time{Time: *tt.lastSeen}
			}
			if err := k8s.Status().Update(ctx, en); err != nil {
				t.Fatal(err)
			}

			rec := events.NewFakeRecorder(10)
			r := &EdgeNodeReconciler{Client: k8s, Recorder: rec, Now: func() time.Time { return t0 }}
			res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: name}})
			if err != nil {
				t.Fatal(err)
			}
			if res.RequeueAfter != tt.wantRequeue {
				t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, tt.wantRequeue)
			}
			got := &fleetv1alpha1.EdgeNode{}
			if err := k8s.Get(ctx, client.ObjectKey{Name: name}, got); err != nil {
				t.Fatal(err)
			}
			if got.Status.Reachability != tt.want {
				t.Errorf("reachability = %q, want %q", got.Status.Reachability, tt.want)
			}
			if tt.stored == tt.want && got.ResourceVersion != en.ResourceVersion {
				t.Errorf("unchanged reachability was written (case %d)", i)
			}
			select {
			case e := <-rec.Events:
				if tt.wantEvent == "" || !strings.HasPrefix(e, tt.wantEvent) {
					t.Errorf("event = %q, want %q", e, tt.wantEvent)
				}
			default:
				if tt.wantEvent != "" {
					t.Errorf("no event, want %q", tt.wantEvent)
				}
			}
		})
	}
}

func TestEdgeNodeNotFound(t *testing.T) {
	r := &EdgeNodeReconciler{Client: k8s, Recorder: events.NewFakeRecorder(1), Now: time.Now}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "missing"}})
	if err != nil || res.RequeueAfter != 0 {
		t.Errorf("Reconcile(missing) = %+v, %v", res, err)
	}
}
