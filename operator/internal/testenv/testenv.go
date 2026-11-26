// Package testenv starts a real kube-apiserver (envtest) with the generated CRDs, for
// integration tests. Run them through `make test`, which downloads the binaries.
package testenv

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Start starts envtest with config/crd/bases installed. Call stop when done.
func Start() (cfg *rest.Config, stop func(), err error) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		// Fail loudly rather than skip: these tests guard the controllers.
		return nil, nil, errors.New("KUBEBUILDER_ASSETS is not set: run `make test` in operator/ (downloads envtest binaries)")
	}
	_, file, _, _ := runtime.Caller(0)
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(filepath.Dir(file), "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if cfg, err = env.Start(); err != nil {
		return nil, nil, err
	}
	return cfg, func() { _ = env.Stop() }, nil
}
