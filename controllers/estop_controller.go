package controllers

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	benchv1alpha1 "github.com/lpasquali/rune-operator/api/v1alpha1"
)

const (
	// RamScrubAnnotation is placed on RuneBenchmark resources and pods when an
	// E-Stop is triggered.  Actual memory scrubbing is delegated to the kubelet
	// via normal pod-termination guarantees; this annotation is the signal.
	RamScrubAnnotation = "rune.io/ram-scrub-on-estop"

	// DefaultEStopConfigMapName is the name of the ConfigMap watched for an
	// E-Stop signal.  Set key "triggered" to "true" to activate.
	DefaultEStopConfigMapName = "rune-estop"
)

// EStopReconciler watches a single ConfigMap for an E-Stop signal.  When the
// ConfigMap's "triggered" key is "true" the reconciler suspends every
// RuneBenchmark in the cluster and annotates all pods so that operators and
// higher-level controllers know a RAM-scrub cycle is required.
type EStopReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	EStopConfigMapName string // defaults to DefaultEStopConfigMapName when empty
}

// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;patch;update
// +kubebuilder:rbac:groups=bench.rune.ai,resources=runebenchmarks,verbs=get;list;update;patch

func (r *EStopReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}).
		Complete(r)
}

func (r *EStopReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	estopName := r.EStopConfigMapName
	if estopName == "" {
		estopName = DefaultEStopConfigMapName
	}

	// Ignore every ConfigMap that is not the designated E-Stop ConfigMap.
	if req.Name != estopName {
		return ctrl.Result{}, nil
	}

	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, req.NamespacedName, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if cm.Data["triggered"] != "true" {
		return ctrl.Result{}, nil
	}

	logger.Info("E-Stop triggered: suspending all RuneBenchmark resources and annotating pods for RAM scrub")

	if err := r.suspendAllBenchmarks(ctx, logger); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.annotatePodsForScrub(ctx, logger); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *EStopReconciler) suspendAllBenchmarks(ctx context.Context, logger interface {
	Info(string, ...any)
	Error(error, string, ...any)
}) error {
	var list benchv1alpha1.RuneBenchmarkList
	if err := r.List(ctx, &list); err != nil {
		return err
	}

	for i := range list.Items {
		item := &list.Items[i]
		if item.Spec.Suspend {
			continue
		}
		item.Spec.Suspend = true
		if item.Annotations == nil {
			item.Annotations = make(map[string]string)
		}
		item.Annotations[RamScrubAnnotation] = "true"
		if err := r.Update(ctx, item); err != nil {
			return err
		}
		logger.Info("suspended RuneBenchmark due to E-Stop", "name", item.Name, "namespace", item.Namespace)
	}
	return nil
}

func (r *EStopReconciler) annotatePodsForScrub(ctx context.Context, logger interface {
	Info(string, ...any)
	Error(error, string, ...any)
}) error {
	var podList corev1.PodList
	if err := r.List(ctx, &podList); err != nil {
		return err
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Annotations != nil && pod.Annotations[RamScrubAnnotation] == "true" {
			continue
		}
		if pod.Annotations == nil {
			pod.Annotations = make(map[string]string)
		}
		pod.Annotations[RamScrubAnnotation] = "true"
		if err := r.Update(ctx, pod); err != nil {
			// Log but continue — a single pod failure should not block the others.
			logger.Error(err, "failed to annotate pod for RAM scrub", "name", pod.Name, "namespace", pod.Namespace)
		} else {
			logger.Info("annotated pod for RAM scrub", "name", pod.Name, "namespace", pod.Namespace)
		}
	}
	return nil
}

// clientLike is a minimal interface over client.Client used for compile-time
// verification in tests; the full client.Client is embedded in EStopReconciler.
type clientLike = client.Client
