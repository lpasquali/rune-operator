// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"context"
	"errors"
	"testing"

	benchv1alpha1 "github.com/lpasquali/rune-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// buildEStopReconciler constructs an EStopReconciler backed by a fake client
// seeded with the provided objects.
func buildEStopReconciler(t *testing.T, objs ...client.Object) *EStopReconciler {
	t.Helper()
	s := runtime.NewScheme()
	if err := benchv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add benchmark scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	return &EStopReconciler{Client: c, Scheme: s}
}

func estopRequest(namespace, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
}

// TestEStop_NonMatchingName ensures that ConfigMaps other than the E-Stop one
// are silently ignored.
func TestEStop_NonMatchingName(t *testing.T) {
	r := buildEStopReconciler(t)
	res, err := r.Reconcile(context.Background(), estopRequest("default", "not-estop"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Requeue || res.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %+v", res)
	}
}

// TestEStop_NotFound ensures that a missing ConfigMap is treated as a no-op.
func TestEStop_NotFound(t *testing.T) {
	r := buildEStopReconciler(t)
	res, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	if err != nil {
		t.Fatalf("expected nil error for not-found, got %v", err)
	}
	if res.Requeue || res.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %+v", res)
	}
}

// TestEStop_NotTriggered ensures that when triggered != "true" nothing happens.
func TestEStop_NotTriggered(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: DefaultEStopConfigMapName},
		Data:       map[string]string{"triggered": "false"},
	}
	r := buildEStopReconciler(t, cm)
	_, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// TestEStop_EmptyData ensures that an E-Stop ConfigMap with no data is treated
// as not triggered.
func TestEStop_EmptyData(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: DefaultEStopConfigMapName},
	}
	r := buildEStopReconciler(t, cm)
	_, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// TestEStop_Triggered verifies the happy path: all RuneBenchmarks are suspended
// and annotated, and pods in the same namespace receive the RAM-scrub annotation.
func TestEStop_Triggered(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: DefaultEStopConfigMapName},
		Data:       map[string]string{"triggered": "true"},
	}
	bench := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "bench-1"},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: "http://rune:8080",
			Workflow:   "benchmark",
			Suspend:    false,
		},
	}
	// Pod in the SAME namespace as the benchmark — should be annotated.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pod-1"},
	}
	// Pod in a DIFFERENT namespace — should NOT be annotated.
	podOther := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "other-ns", Name: "pod-other"},
	}

	r := buildEStopReconciler(t, cm, bench, pod, podOther)
	_, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify RuneBenchmark is suspended and annotated.
	updated := &benchv1alpha1.RuneBenchmark{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "bench-1"}, updated); err != nil {
		t.Fatalf("get benchmark: %v", err)
	}
	if !updated.Spec.Suspend {
		t.Error("expected benchmark to be suspended")
	}
	if updated.Annotations[RamScrubAnnotation] != "true" {
		t.Errorf("expected annotation %q on benchmark, got %q", RamScrubAnnotation, updated.Annotations[RamScrubAnnotation])
	}

	// Pod in benchmark namespace — should be annotated.
	updatedPod := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "pod-1"}, updatedPod); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if updatedPod.Annotations[RamScrubAnnotation] != "true" {
		t.Errorf("expected annotation %q on pod in benchmark namespace", RamScrubAnnotation)
	}

	// Pod in other namespace — should NOT be annotated.
	otherPod := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "other-ns", Name: "pod-other"}, otherPod); err != nil {
		t.Fatalf("get other pod: %v", err)
	}
	if otherPod.Annotations[RamScrubAnnotation] == "true" {
		t.Error("expected pod in other namespace NOT to be annotated")
	}
}

// TestEStop_AlreadySuspendedBenchmark ensures that already-suspended
// RuneBenchmarks are skipped without error.
func TestEStop_AlreadySuspendedBenchmark(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: DefaultEStopConfigMapName},
		Data:       map[string]string{"triggered": "true"},
	}
	bench := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "bench-suspended"},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: "http://rune:8080",
			Workflow:   "benchmark",
			Suspend:    true,
		},
	}
	r := buildEStopReconciler(t, cm, bench)
	_, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestEStop_AlreadyAnnotatedPod ensures that pods already carrying the
// RAM-scrub annotation are not re-patched.
func TestEStop_AlreadyAnnotatedPod(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: DefaultEStopConfigMapName},
		Data:       map[string]string{"triggered": "true"},
	}
	bench := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "bench-x"},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: "http://rune:8080",
			Workflow:   "benchmark",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        "pod-annotated",
			Annotations: map[string]string{RamScrubAnnotation: "true"},
		},
	}
	r := buildEStopReconciler(t, cm, bench, pod)
	_, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestEStop_NamespaceFilter verifies that when EStopNamespace is set,
// ConfigMaps from other namespaces are ignored.
func TestEStop_NamespaceFilter(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "wrong-ns", Name: DefaultEStopConfigMapName},
		Data:       map[string]string{"triggered": "true"},
	}
	bench := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "bench-ns"},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: "http://rune:8080",
			Workflow:   "benchmark",
		},
	}
	r := buildEStopReconciler(t, cm, bench)
	r.EStopNamespace = "ops" // Only accept ConfigMaps from "ops" namespace.

	_, err := r.Reconcile(context.Background(), estopRequest("wrong-ns", DefaultEStopConfigMapName))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Benchmark should NOT be suspended (wrong namespace filtered).
	updated := &benchv1alpha1.RuneBenchmark{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "bench-ns"}, updated); err != nil {
		t.Fatalf("get benchmark: %v", err)
	}
	if updated.Spec.Suspend {
		t.Error("benchmark should not be suspended when namespace filter rejects the trigger")
	}
}

// TestEStop_CustomConfigMapName exercises a non-default E-Stop ConfigMap name.
func TestEStop_CustomConfigMapName(t *testing.T) {
	const customName = "my-custom-estop"
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: customName},
		Data:       map[string]string{"triggered": "true"},
	}
	bench := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "bench-custom"},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: "http://rune:8080",
			Workflow:   "benchmark",
		},
	}
	r := buildEStopReconciler(t, cm, bench)
	r.EStopConfigMapName = customName
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "ops", Name: customName},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &benchv1alpha1.RuneBenchmark{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "bench-custom"}, updated); err != nil {
		t.Fatalf("get benchmark: %v", err)
	}
	if !updated.Spec.Suspend {
		t.Error("expected benchmark to be suspended for custom E-Stop ConfigMap")
	}
}

// ---------------------------------------------------------------------------
// Error-path tests using stub clients
// ---------------------------------------------------------------------------

type estopGetErrorClient struct {
	client.Client
	getErr error
}

func (c estopGetErrorClient) Get(_ context.Context, _ types.NamespacedName, obj client.Object, _ ...client.GetOption) error {
	if _, ok := obj.(*corev1.ConfigMap); ok {
		return c.getErr
	}
	return c.Client.Get(context.Background(), types.NamespacedName{}, obj)
}

type estopListErrorClient struct {
	client.Client
	benchErr  error
	podErr    error
	callCount int
}

func (c *estopListErrorClient) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *estopListErrorClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.callCount++
	if _, ok := list.(*benchv1alpha1.RuneBenchmarkList); ok && c.benchErr != nil {
		return c.benchErr
	}
	if _, ok := list.(*corev1.PodList); ok && c.podErr != nil {
		return c.podErr
	}
	return c.Client.List(ctx, list, opts...)
}

type estopUpdateErrorClient struct {
	client.Client
	updateErr error
}

func (c estopUpdateErrorClient) Update(_ context.Context, _ client.Object, _ ...client.UpdateOption) error {
	return c.updateErr
}

// TestEStop_GetError verifies that a non-NotFound error from Get is propagated.
func TestEStop_GetError(t *testing.T) {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = benchv1alpha1.AddToScheme(s)
	base := fake.NewClientBuilder().WithScheme(s).Build()

	r := &EStopReconciler{
		Client: estopGetErrorClient{
			Client: base,
			getErr: errors.New("api-server-down"),
		},
		Scheme: s,
	}
	_, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	if err == nil || err.Error() != "api-server-down" {
		t.Fatalf("expected api-server-down error, got %v", err)
	}
}

// TestEStop_GetError_IsNotFound verifies the IsNotFound branch in Get.
func TestEStop_GetError_IsNotFound(t *testing.T) {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = benchv1alpha1.AddToScheme(s)
	base := fake.NewClientBuilder().WithScheme(s).Build()
	r := &EStopReconciler{
		Client: estopGetErrorClient{
			Client: base,
			getErr: apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, DefaultEStopConfigMapName),
		},
		Scheme: s,
	}
	_, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	if err != nil {
		t.Fatalf("expected nil for not-found, got %v", err)
	}
}

// TestEStop_ListBenchmarksError verifies that a List error for RuneBenchmarks
// is propagated.
func TestEStop_ListBenchmarksError(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: DefaultEStopConfigMapName},
		Data:       map[string]string{"triggered": "true"},
	}
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = benchv1alpha1.AddToScheme(s)
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(cm).Build()
	listClient := &estopListErrorClient{Client: base, benchErr: errors.New("list-bench-err")}
	r := &EStopReconciler{Client: listClient, Scheme: s}

	_, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	if err == nil || err.Error() != "list-bench-err" {
		t.Fatalf("expected list-bench-err, got %v", err)
	}
}

// TestEStop_ListPodsError verifies that a List error for Pods is propagated.
// The benchmark must be in the same namespace as the list error triggers.
func TestEStop_ListPodsError(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: DefaultEStopConfigMapName},
		Data:       map[string]string{"triggered": "true"},
	}
	bench := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "bench-pod-err"},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: "http://rune:8080",
			Workflow:   "benchmark",
		},
	}
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = benchv1alpha1.AddToScheme(s)
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(cm, bench).Build()
	listClient := &estopListErrorClient{Client: base, podErr: errors.New("list-pod-err")}
	r := &EStopReconciler{Client: listClient, Scheme: s}

	_, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	if err == nil || err.Error() != "list-pod-err" {
		t.Fatalf("expected list-pod-err, got %v", err)
	}
}

// TestEStop_UpdateBenchmarkError verifies that a benchmark Update error is
// returned immediately.
func TestEStop_UpdateBenchmarkError(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: DefaultEStopConfigMapName},
		Data:       map[string]string{"triggered": "true"},
	}
	bench := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "bench-err"},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: "http://rune:8080",
			Workflow:   "benchmark",
		},
	}
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = benchv1alpha1.AddToScheme(s)
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(cm, bench).Build()

	r := &EStopReconciler{
		Client: estopUpdateErrorClient{Client: base, updateErr: errors.New("update-bench-err")},
		Scheme: s,
	}
	_, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	if err == nil || err.Error() != "update-bench-err" {
		t.Fatalf("expected update-bench-err, got %v", err)
	}
}

// TestEStop_PatchPodErrorContinues verifies that a pod Patch error is logged
// but does not abort the reconcile loop.
func TestEStop_PatchPodErrorContinues(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: DefaultEStopConfigMapName},
		Data:       map[string]string{"triggered": "true"},
	}
	bench := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "bench-patch-err"},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: "http://rune:8080",
			Workflow:   "benchmark",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pod-fail"},
	}
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = benchv1alpha1.AddToScheme(s)
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(cm, bench, pod).Build()

	r := &EStopReconciler{
		Client: podPatchErrorClient{Client: base, podPatchErr: errors.New("pod-patch-err")},
		Scheme: s,
	}
	_, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	// Pod patch failure is logged, not returned.
	if err != nil {
		t.Fatalf("expected nil error (pod patch errors are non-fatal), got %v", err)
	}
}

// podPatchErrorClient returns an error only when patching Pod objects.
type podPatchErrorClient struct {
	client.Client
	podPatchErr error
}

func (c podPatchErrorClient) Patch(_ context.Context, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
	if _, ok := obj.(*corev1.Pod); ok {
		return c.podPatchErr
	}
	return c.Client.Patch(context.Background(), obj, nil)
}

func (c podPatchErrorClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	return c.Client.Update(ctx, obj, opts...)
}

func (c podPatchErrorClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.Client.List(ctx, list, opts...)
}

func (c podPatchErrorClient) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	return c.Client.Get(ctx, key, obj, opts...)
}

// TestEStop_SetupWithManager_NilGuard tests the nil manager guard.
func TestEStop_SetupWithManager_NilGuard(t *testing.T) {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	base := fake.NewClientBuilder().WithScheme(s).Build()
	r := &EStopReconciler{Client: base, Scheme: s}

	err := r.SetupWithManager(nil)
	if err == nil || err.Error() != "manager is nil" {
		t.Fatalf("expected 'manager is nil' error, got %v", err)
	}
}

// TestEStop_SetupWithManager_Injectable exercises the SetupWithManager path
// via the injectable setupEStopControllerWithManager function.
func TestEStop_SetupWithManager_Injectable(t *testing.T) {
	oldSetup := setupEStopControllerWithManager
	t.Cleanup(func() { setupEStopControllerWithManager = oldSetup })

	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = benchv1alpha1.AddToScheme(s)
	base := fake.NewClientBuilder().WithScheme(s).Build()
	r := &EStopReconciler{Client: base, Scheme: s, EStopConfigMapName: "custom-estop"}

	called := false
	var gotName string
	setupEStopControllerWithManager = func(_ ctrl.Manager, _ *EStopReconciler, name string) error {
		called = true
		gotName = name
		return nil
	}

	// stubManager embeds ctrl.Manager to satisfy the interface check in SetupWithManager.
	type stubManager struct{ ctrl.Manager }
	if err := r.SetupWithManager(stubManager{}); err != nil {
		t.Fatalf("SetupWithManager returned error: %v", err)
	}
	if !called {
		t.Fatal("expected setupEStopControllerWithManager to be called")
	}
	if gotName != "custom-estop" {
		t.Fatalf("expected custom-estop, got %q", gotName)
	}
}

// TestEStop_RamScrubAnnotationConstant documents the stable annotation key.
func TestEStop_RamScrubAnnotationConstant(t *testing.T) {
	const want = "rune.io/ram-scrub-on-estop"
	if RamScrubAnnotation != want {
		t.Fatalf("RamScrubAnnotation changed: want %q, got %q", want, RamScrubAnnotation)
	}
}

// TestEStop_DefaultConfigMapNameConstant documents the stable default name.
func TestEStop_DefaultConfigMapNameConstant(t *testing.T) {
	const want = "rune-estop"
	if DefaultEStopConfigMapName != want {
		t.Fatalf("DefaultEStopConfigMapName changed: want %q, got %q", want, DefaultEStopConfigMapName)
	}
}

// noopLogger satisfies the inline logger interface for direct unit-test injection.
type noopLogger struct{}

func (noopLogger) Info(_ string, _ ...any)           {}
func (noopLogger) Error(_ error, _ string, _ ...any) {}

// TestSuspendAllBenchmarks_Direct tests suspendAllBenchmarks in isolation.
func TestSuspendAllBenchmarks_Direct(t *testing.T) {
	bench := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "direct-bench"},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: "http://rune:8080",
			Workflow:   "benchmark",
		},
	}
	r := buildEStopReconciler(t, bench)
	if err := r.suspendAllBenchmarks(context.Background(), noopLogger{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	updated := &benchv1alpha1.RuneBenchmark{}
	_ = r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "direct-bench"}, updated)
	if !updated.Spec.Suspend {
		t.Error("expected bench to be suspended")
	}
}

// TestAnnotatePodsForScrub_Direct tests annotatePodsForScrub in isolation.
// Pods are only annotated when a RuneBenchmark is in the same namespace.
func TestAnnotatePodsForScrub_Direct(t *testing.T) {
	bench := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "bench-for-scrub"},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: "http://rune:8080",
			Workflow:   "benchmark",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "direct-pod"},
	}
	r := buildEStopReconciler(t, bench, pod)
	if err := r.annotatePodsForScrub(context.Background(), log.FromContext(context.Background())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	updated := &corev1.Pod{}
	_ = r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "direct-pod"}, updated)
	if updated.Annotations[RamScrubAnnotation] != "true" {
		t.Error("expected pod to be annotated for RAM scrub")
	}
}

// TestAnnotatePodsForScrub_NoBenchmarks ensures that with no benchmarks, no
// pods are annotated (namespace loop is empty).
func TestAnnotatePodsForScrub_NoBenchmarks(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "lonely-pod"},
	}
	r := buildEStopReconciler(t, pod)
	if err := r.annotatePodsForScrub(context.Background(), noopLogger{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	updated := &corev1.Pod{}
	_ = r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "lonely-pod"}, updated)
	if updated.Annotations[RamScrubAnnotation] == "true" {
		t.Error("expected pod NOT to be annotated when no benchmarks exist")
	}
}

// TestAnnotatePodsForScrub_BenchmarkListError covers the benchmark List error
// branch inside annotatePodsForScrub when called directly.
func TestAnnotatePodsForScrub_BenchmarkListError(t *testing.T) {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = benchv1alpha1.AddToScheme(s)
	base := fake.NewClientBuilder().WithScheme(s).Build()
	lc := &estopListErrorClient{Client: base, benchErr: errors.New("bench-list-in-annotate")}
	r := &EStopReconciler{Client: lc, Scheme: s}

	err := r.annotatePodsForScrub(context.Background(), noopLogger{})
	if err == nil || err.Error() != "bench-list-in-annotate" {
		t.Fatalf("expected bench-list-in-annotate error, got %v", err)
	}
}
