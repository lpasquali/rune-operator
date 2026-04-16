// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"

	benchv1alpha1 "github.com/lpasquali/rune-operator/api/v1alpha1"
	"github.com/lpasquali/rune-operator/controllers"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
)

type fakeManager struct {
	scheme     *runtime.Scheme
	healthErr  error
	readyErr   error
	startErr   error
	startCalls int
}

func (f *fakeManager) GetClient() client.Client { return nil }

func (f *fakeManager) GetScheme() *runtime.Scheme {
	if f.scheme == nil {
		f.scheme = runtime.NewScheme()
	}
	return f.scheme
}

func (f *fakeManager) GetEventRecorderFor(string) record.EventRecorder {
	return record.NewFakeRecorder(10)
}
func (f *fakeManager) AddHealthzCheck(string, healthz.Checker) error { return f.healthErr }
func (f *fakeManager) AddReadyzCheck(string, healthz.Checker) error  { return f.readyErr }
func (f *fakeManager) Start(context.Context) error {
	f.startCalls++
	return f.startErr
}

func TestRuntimeSchemeIncludesAPIs(t *testing.T) {
	s, err := runtimeScheme()
	if err != nil {
		t.Fatalf("runtimeScheme failed: %v", err)
	}
	if s == nil {
		t.Fatalf("expected non-nil scheme")
	}

	gvks, _, err := s.ObjectKinds(&benchv1alpha1.RuneBenchmark{})
	if err != nil {
		t.Fatalf("ObjectKinds failed: %v", err)
	}
	if len(gvks) == 0 {
		t.Fatalf("expected at least one GVK")
	}

	if !s.Recognizes(benchv1alpha1.GroupVersion.WithKind("RuneBenchmark")) {
		t.Fatalf("expected scheme to recognize RuneBenchmark kind")
	}
}

func TestRuntimeSchemeErrorBranches(t *testing.T) {
	oldClientGo := addClientGoSchemeFn
	oldBench := addBenchSchemeFn
	t.Cleanup(func() {
		addClientGoSchemeFn = oldClientGo
		addBenchSchemeFn = oldBench
	})

	addClientGoSchemeFn = func(*runtime.Scheme) error { return errors.New("clientgoscheme") }
	if _, err := runtimeScheme(); err == nil || !strings.Contains(err.Error(), "clientgoscheme") {
		t.Fatalf("expected client-go scheme error, got %v", err)
	}

	addClientGoSchemeFn = oldClientGo
	addBenchSchemeFn = func(*runtime.Scheme) error { return errors.New("benchscheme") }
	if _, err := runtimeScheme(); err == nil || !strings.Contains(err.Error(), "benchscheme") {
		t.Fatalf("expected benchmark scheme error, got %v", err)
	}
}

func TestDefaultNewManagerFnUsesConfiguredOptions(t *testing.T) {
	oldConfig := getConfigOrDieFn
	oldBuild := buildManagerFn
	t.Cleanup(func() {
		getConfigOrDieFn = oldConfig
		buildManagerFn = oldBuild
	})

	getConfigOrDieFn = func() *rest.Config { return &rest.Config{Host: "https://example.invalid"} }

	called := false
	buildManagerFn = func(cfg *rest.Config, options ctrl.Options) (managerLike, error) {
		called = true
		if cfg == nil || cfg.Host != "https://example.invalid" {
			t.Fatalf("unexpected config: %+v", cfg)
		}
		if options.Metrics.BindAddress != ":8080" {
			t.Fatalf("unexpected metrics bind address: %q", options.Metrics.BindAddress)
		}
		if options.HealthProbeBindAddress != ":8081" {
			t.Fatalf("unexpected probe bind address: %q", options.HealthProbeBindAddress)
		}
		if options.PprofBindAddress != "0" {
			t.Fatalf("unexpected pprof bind address: %q", options.PprofBindAddress)
		}
		if !options.LeaderElection {
			t.Fatalf("expected leader election enabled")
		}
		if options.LeaderElectionID != "rune-operator.bench.rune.ai" {
			t.Fatalf("unexpected leader election id: %q", options.LeaderElectionID)
		}
		return &fakeManager{}, nil
	}

	mgr, err := newManagerFn(runtime.NewScheme(), ":8080", ":8081", "0", true)
	if err != nil {
		t.Fatalf("expected newManagerFn success, got %v", err)
	}
	if !called || mgr == nil {
		t.Fatalf("expected buildManagerFn to be invoked")
	}
}

func TestDefaultSetupReconcilerFnSuccessBranch(t *testing.T) {
	oldSetup := setupReconcilerWithManagerFn
	t.Cleanup(func() { setupReconcilerWithManagerFn = oldSetup })

	called := false
	setupReconcilerWithManagerFn = func(reconciler *controllers.RuneBenchmarkReconciler, mgr managerLike) error {
		called = true
		if reconciler == nil || reconciler.Recorder == nil {
			t.Fatalf("expected reconciler with recorder")
		}
		if mgr == nil {
			t.Fatalf("expected manager to be passed through")
		}
		return nil
	}

	if err := setupReconcilerFn(&fakeManager{}); err != nil {
		t.Fatalf("expected setupReconcilerFn success, got %v", err)
	}
	if !called {
		t.Fatalf("expected setupReconcilerWithManagerFn to be called")
	}
}

func TestRun_ErrorBranchesAndSuccess(t *testing.T) {
	oldNewMgr := newManagerFn
	oldSetupRec := setupReconcilerFn
	oldSetupEstop := setupEStopFn
	oldSignal := setupSignalHandlerFn
	oldClientGo := addClientGoSchemeFn
	t.Cleanup(func() {
		newManagerFn = oldNewMgr
		setupReconcilerFn = oldSetupRec
		setupEStopFn = oldSetupEstop
		setupSignalHandlerFn = oldSignal
		addClientGoSchemeFn = oldClientGo
	})

	setupSignalHandlerFn = func() context.Context { return context.Background() }

	addClientGoSchemeFn = func(*runtime.Scheme) error { return errors.New("scheme-build") }
	if err := run(":1", ":2", "0", true, false, ""); err == nil || !strings.Contains(err.Error(), "unable to build runtime scheme") {
		t.Fatalf("expected runtime scheme build error, got %v", err)
	}
	addClientGoSchemeFn = oldClientGo

	newManagerFn = func(*runtime.Scheme, string, string, string, bool) (managerLike, error) {
		return nil, errors.New("new-manager")
	}
	if err := run(":1", ":2", "0", true, false, ""); err == nil || !strings.Contains(err.Error(), "unable to create manager") {
		t.Fatalf("expected start-manager error, got %v", err)
	}

	m := &fakeManager{}
	newManagerFn = func(*runtime.Scheme, string, string, string, bool) (managerLike, error) { return m, nil }
	setupReconcilerFn = func(managerLike) error { return errors.New("setup-reconciler") }
	if err := run(":1", ":2", "0", true, false, ""); err == nil || !strings.Contains(err.Error(), "unable to create controller") {
		t.Fatalf("expected create-controller error, got %v", err)
	}

	setupReconcilerFn = func(managerLike) error { return nil }
	// E-Stop error branch.
	setupEStopFn = func(managerLike, bool, string) error { return errors.New("estop-err") }
	if err := run(":1", ":2", "0", true, true, "rune-estop"); err == nil || !strings.Contains(err.Error(), "e-stop controller") {
		t.Fatalf("expected e-stop controller error, got %v", err)
	}
	setupEStopFn = func(managerLike, bool, string) error { return nil }
	m.healthErr = errors.New("health")
	if err := run(":1", ":2", "0", true, false, ""); err == nil || !strings.Contains(err.Error(), "health check") {
		t.Fatalf("expected health check error, got %v", err)
	}

	m.healthErr = nil
	m.readyErr = errors.New("ready")
	if err := run(":1", ":2", "0", true, false, ""); err == nil || !strings.Contains(err.Error(), "ready check") {
		t.Fatalf("expected ready check error, got %v", err)
	}

	m.readyErr = nil
	m.startErr = errors.New("start")
	if err := run(":1", ":2", "0", true, false, ""); err == nil || !strings.Contains(err.Error(), "start") {
		t.Fatalf("expected start error, got %v", err)
	}

	m.startErr = nil
	if err := run(":1", ":2", "0", true, false, ""); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if m.startCalls == 0 {
		t.Fatalf("expected manager Start to be called")
	}
}

func TestMain_ExitOnRunError(t *testing.T) {
	oldNewMgr := newManagerFn
	oldSetupRec := setupReconcilerFn
	oldExit := exitFn
	oldSignal := setupSignalHandlerFn
	oldFS := flag.CommandLine
	oldArgs := os.Args
	t.Cleanup(func() {
		newManagerFn = oldNewMgr
		setupReconcilerFn = oldSetupRec
		exitFn = oldExit
		setupSignalHandlerFn = oldSignal
		flag.CommandLine = oldFS
		os.Args = oldArgs
	})

	setupSignalHandlerFn = func() context.Context { return context.Background() }
	flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
	os.Args = []string{"cmd"}
	newManagerFn = func(*runtime.Scheme, string, string, string, bool) (managerLike, error) { return nil, errors.New("boom") }
	setupReconcilerFn = func(managerLike) error { return nil }

	exited := 0
	exitFn = func(code int) { exited = code }

	main()
	if exited != 1 {
		t.Fatalf("expected exit code 1, got %d", exited)
	}
}

func TestDefaultSetupReconcilerFnTypeAssertionError(t *testing.T) {
	err := setupReconcilerFn(&fakeManager{})
	if err == nil || !strings.Contains(err.Error(), "controller-runtime manager") {
		t.Fatalf("expected controller-runtime manager error, got %v", err)
	}
}

// TestDefaultSetupEStopFn_Disabled verifies that a disabled E-Stop setup is a
// no-op.
func TestDefaultSetupEStopFn_Disabled(t *testing.T) {
	oldFn := setupEStopFn
	t.Cleanup(func() { setupEStopFn = oldFn })

	if err := setupEStopFn(&fakeManager{}, false, "rune-estop"); err != nil {
		t.Fatalf("expected nil for disabled estop, got %v", err)
	}
}

// TestDefaultSetupEStopFn_TypeAssertionError verifies that an enabled E-Stop
// setup with a non-ctrl.Manager returns a clear error.
func TestDefaultSetupEStopFn_TypeAssertionError(t *testing.T) {
	oldFn := setupEStopFn
	t.Cleanup(func() { setupEStopFn = oldFn })

	err := setupEStopFn(&fakeManager{}, true, "rune-estop")
	if err == nil || !strings.Contains(err.Error(), "controller-runtime manager") {
		t.Fatalf("expected controller-runtime manager error, got %v", err)
	}
}
