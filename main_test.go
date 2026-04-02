package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"

	benchv1alpha1 "github.com/lpasquali/rune-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
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
	s := runtimeScheme()
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

func TestRun_ErrorBranchesAndSuccess(t *testing.T) {
	oldNewMgr := newManagerFn
	oldSetupRec := setupReconcilerFn
	oldSignal := setupSignalHandlerFn
	t.Cleanup(func() {
		newManagerFn = oldNewMgr
		setupReconcilerFn = oldSetupRec
		setupSignalHandlerFn = oldSignal
	})

	setupSignalHandlerFn = func() context.Context { return context.Background() }

	newManagerFn = func(*runtime.Scheme, string, string, bool) (managerLike, error) {
		return nil, errors.New("new-manager")
	}
	if err := run(":1", ":2", true); err == nil || !strings.Contains(err.Error(), "unable to start manager") {
		t.Fatalf("expected start-manager error, got %v", err)
	}

	m := &fakeManager{}
	newManagerFn = func(*runtime.Scheme, string, string, bool) (managerLike, error) { return m, nil }
	setupReconcilerFn = func(managerLike) error { return errors.New("setup-reconciler") }
	if err := run(":1", ":2", true); err == nil || !strings.Contains(err.Error(), "unable to create controller") {
		t.Fatalf("expected create-controller error, got %v", err)
	}

	setupReconcilerFn = func(managerLike) error { return nil }
	m.healthErr = errors.New("health")
	if err := run(":1", ":2", true); err == nil || !strings.Contains(err.Error(), "health check") {
		t.Fatalf("expected health check error, got %v", err)
	}

	m.healthErr = nil
	m.readyErr = errors.New("ready")
	if err := run(":1", ":2", true); err == nil || !strings.Contains(err.Error(), "ready check") {
		t.Fatalf("expected ready check error, got %v", err)
	}

	m.readyErr = nil
	m.startErr = errors.New("start")
	if err := run(":1", ":2", true); err == nil || !strings.Contains(err.Error(), "start") {
		t.Fatalf("expected start error, got %v", err)
	}

	m.startErr = nil
	if err := run(":1", ":2", true); err != nil {
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
	newManagerFn = func(*runtime.Scheme, string, string, bool) (managerLike, error) { return nil, errors.New("boom") }
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
