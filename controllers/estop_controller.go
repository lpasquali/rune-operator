package controllers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

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

// setupEStopControllerWithManager is injectable for unit tests; production code
// sets up a name-filtered watch on ConfigMaps.
var setupEStopControllerWithManager = func(mgr ctrl.Manager, r *EStopReconciler, estopName string) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}, builder.WithPredicates(
			predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetName() == estopName
			}),
		)).
		Complete(r)
}

// EStopReconciler watches a single named ConfigMap for an E-Stop signal.  When
// the ConfigMap's "triggered" key is "true" the reconciler:
//  1. Suspends every RuneBenchmark in the cluster.
//  2. Annotates pods in RuneBenchmark namespaces with RamScrubAnnotation so
//     that external controllers can schedule a RAM-scrub cycle.
type EStopReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	EStopConfigMapName string // defaults to DefaultEStopConfigMapName when empty
	// EStopNamespace restricts the ConfigMap watch to one namespace.
	// Empty string means any namespace (cluster-scoped watch).
	EStopNamespace string
}

// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;patch
// +kubebuilder:rbac:groups=bench.rune.ai,resources=runebenchmarks,verbs=get;list;update;patch

func (r *EStopReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if mgr == nil {
		return fmt.Errorf("manager is nil")
	}
	return setupEStopControllerWithManager(mgr, r, r.estopName())
}

func (r *EStopReconciler) estopName() string {
	if r.EStopConfigMapName != "" {
		return r.EStopConfigMapName
	}
	return DefaultEStopConfigMapName
}

func (r *EStopReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Guard against ConfigMaps that slipped past the predicate (e.g. during
	// re-list on startup) or wrong-namespace triggers.
	if req.Name != r.estopName() {
		return ctrl.Result{}, nil
	}
	if r.EStopNamespace != "" && req.Namespace != r.EStopNamespace {
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

// annotatePodsForScrub annotates pods that live in the same namespaces as
// RuneBenchmark resources.  Using Patch avoids resourceVersion conflicts
// common with Update on frequently-mutating Pod objects.
func (r *EStopReconciler) annotatePodsForScrub(ctx context.Context, logger interface {
	Info(string, ...any)
	Error(error, string, ...any)
}) error {
	var benchmarkList benchv1alpha1.RuneBenchmarkList
	if err := r.List(ctx, &benchmarkList); err != nil {
		return err
	}

	// Collect the set of namespaces that host RuneBenchmarks.
	namespaces := make(map[string]struct{})
	for _, item := range benchmarkList.Items {
		namespaces[item.Namespace] = struct{}{}
	}

	for ns := range namespaces {
		var podList corev1.PodList
		if err := r.List(ctx, &podList, client.InNamespace(ns)); err != nil {
			return err
		}

		for i := range podList.Items {
			pod := &podList.Items[i]
			if pod.Annotations != nil && pod.Annotations[RamScrubAnnotation] == "true" {
				continue
			}
			base := pod.DeepCopy()
			if pod.Annotations == nil {
				pod.Annotations = make(map[string]string)
			}
			pod.Annotations[RamScrubAnnotation] = "true"
			if err := r.Patch(ctx, pod, client.MergeFrom(base)); err != nil {
				// Log but continue — a single pod failure should not block others.
				logger.Error(err, "failed to annotate pod for RAM scrub", "name", pod.Name, "namespace", pod.Namespace)
			} else {
				logger.Info("annotated pod for RAM scrub", "name", pod.Name, "namespace", pod.Namespace)
			}
		}
	}
	return nil
}
