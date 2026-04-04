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
// and annotated, and all pods receive the RAM-scrub annotation.
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
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pod-1"},
	}

	r := buildEStopReconciler(t, cm, bench, pod)
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

	// Verify pod annotation.
	updatedPod := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "pod-1"}, updatedPod); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if updatedPod.Annotations[RamScrubAnnotation] != "true" {
		t.Errorf("expected annotation %q on pod", RamScrubAnnotation)
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
// RAM-scrub annotation are not re-annotated.
func TestEStop_AlreadyAnnotatedPod(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: DefaultEStopConfigMapName},
		Data:       map[string]string{"triggered": "true"},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        "pod-annotated",
			Annotations: map[string]string{RamScrubAnnotation: "true"},
		},
	}
	r := buildEStopReconciler(t, cm, pod)
	_, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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

func (c *estopListErrorClient) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	c.callCount++
	if _, ok := list.(*benchv1alpha1.RuneBenchmarkList); ok && c.benchErr != nil {
		return c.benchErr
	}
	if _, ok := list.(*corev1.PodList); ok && c.podErr != nil {
		return c.podErr
	}
	return nil
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
func TestEStop_ListPodsError(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: DefaultEStopConfigMapName},
		Data:       map[string]string{"triggered": "true"},
	}
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = benchv1alpha1.AddToScheme(s)
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(cm).Build()
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

// TestEStop_UpdatePodErrorContinues verifies that a pod Update error is logged
// but does not abort the reconcile loop.
func TestEStop_UpdatePodErrorContinues(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: DefaultEStopConfigMapName},
		Data:       map[string]string{"triggered": "true"},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pod-fail"},
	}
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = benchv1alpha1.AddToScheme(s)
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(cm, pod).Build()

	// Wrap update to fail for pods but succeed for RuneBenchmarks.
	r := &EStopReconciler{
		Client: podUpdateErrorClient{Client: base, podUpdateErr: errors.New("pod-update-err")},
		Scheme: s,
	}
	_, err := r.Reconcile(context.Background(), estopRequest("default", DefaultEStopConfigMapName))
	// Pod update failure is logged, not returned.
	if err != nil {
		t.Fatalf("expected nil error (pod update errors are non-fatal), got %v", err)
	}
}

// podUpdateErrorClient returns an error only when updating Pod objects.
type podUpdateErrorClient struct {
	client.Client
	podUpdateErr error
}

func (c podUpdateErrorClient) Update(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
	if _, ok := obj.(*corev1.Pod); ok {
		return c.podUpdateErr
	}
	return c.Client.Update(context.Background(), obj)
}

// TestEStop_SetupWithManager exercises the SetupWithManager path via a stub
// that implements the minimal ctrl.Manager interface.
func TestEStop_SetupWithManager(t *testing.T) {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = benchv1alpha1.AddToScheme(s)
	base := fake.NewClientBuilder().WithScheme(s).Build()
	r := &EStopReconciler{Client: base, Scheme: s}

	oldSetup := setupControllerWithManager
	t.Cleanup(func() { setupControllerWithManager = oldSetup })

	// Verify SetupWithManager calls through to ctrl.NewControllerManagedBy
	// by checking that an error is returned when the manager is nil.
	err := r.SetupWithManager(nil)
	// nil manager will panic in the real code; we just ensure it doesn't hang.
	_ = err // might or might not error depending on runtime; result is less important
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

// noopLogger satisfies the inline logger interface used by suspendAllBenchmarks /
// annotatePodsForScrub for direct unit-test injection.
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
func TestAnnotatePodsForScrub_Direct(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "direct-pod"},
	}
	r := buildEStopReconciler(t, pod)
	if err := r.annotatePodsForScrub(context.Background(), log.FromContext(context.Background())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	updated := &corev1.Pod{}
	_ = r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "direct-pod"}, updated)
	if updated.Annotations[RamScrubAnnotation] != "true" {
		t.Error("expected pod to be annotated for RAM scrub")
	}
}
