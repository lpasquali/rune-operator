package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	benchv1alpha1 "github.com/lpasquali/rune-operator/api/v1alpha1"
	"github.com/lpasquali/rune-operator/controllers"
	"github.com/lpasquali/rune-operator/internal/metrics"
	"github.com/lpasquali/rune-operator/internal/telemetry"
)

type managerLike interface {
	GetClient() client.Client
	GetScheme() *runtime.Scheme
	GetEventRecorderFor(name string) record.EventRecorder
	AddHealthzCheck(name string, check healthz.Checker) error
	AddReadyzCheck(name string, check healthz.Checker) error
	Start(ctx context.Context) error
}

var (
	newManagerFn = func(s *runtime.Scheme, metricsAddr, probeAddr string, enableLeaderElection bool) (managerLike, error) {
		return ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
			Scheme:                 s,
			Metrics:                server.Options{BindAddress: metricsAddr},
			HealthProbeBindAddress: probeAddr,
			LeaderElection:         enableLeaderElection,
			LeaderElectionID:       "rune-operator.bench.rune.ai",
		})
	}
	setupReconcilerFn = func(mgr managerLike) error {
		ctrlMgr, ok := mgr.(ctrl.Manager)
		if !ok {
			return errors.New("manager does not implement controller-runtime manager")
		}
		return (&controllers.RuneBenchmarkReconciler{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorderFor("rune-benchmark-controller"),
		}).SetupWithManager(ctrlMgr)
	}
	setupSignalHandlerFn = ctrl.SetupSignalHandler
	exitFn               = os.Exit
)

func main() {
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true, "Enable leader election for controller manager.")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if err := run(metricsAddr, probeAddr, enableLeaderElection); err != nil {
		klog.ErrorS(err, "problem running manager")
		exitFn(1)
	}
}

func run(metricsAddr, probeAddr string, enableLeaderElection bool) error {
	s, err := runtimeScheme()
	if err != nil {
		return fmt.Errorf("unable to build runtime scheme: %w", err)
	}

	shutdownTelemetry := telemetry.SetupOTel("rune-operator")
	defer shutdownTelemetry()

	mgr, err := newManagerFn(s, metricsAddr, probeAddr, enableLeaderElection)
	if err != nil {
		return fmt.Errorf("unable to start manager: %w", err)
	}

	metrics.Register()

	if err := setupReconcilerFn(mgr); err != nil {
		return fmt.Errorf("unable to create controller: %w", err)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	klog.InfoS("starting manager")
	if err := mgr.Start(setupSignalHandlerFn()); err != nil {
		return err
	}
	return nil
}

func runtimeScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := benchv1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}
