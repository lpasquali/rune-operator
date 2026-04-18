// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	benchv1alpha1 "github.com/lpasquali/rune-operator/api/v1alpha1"
)

// newRunedatabaseXR returns an unstructured Crossplane RuneDatabase Claim
// (group database.infra.rune.ai) stamped with the supplied conditions. Pass
// nil `conditions` to construct an object with no status yet.
func newRunedatabaseXR(name, ns string, conditions []map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("database.infra.rune.ai/v1alpha1")
	u.SetKind("RuneDatabase")
	u.SetName(name)
	u.SetNamespace(ns)
	if conditions != nil {
		condList := make([]any, 0, len(conditions))
		for _, c := range conditions {
			condList = append(condList, c)
		}
		_ = unstructured.SetNestedSlice(u.Object, condList, "status", "conditions")
	}
	return u
}

// condition builds a Kubernetes-style condition map for a Crossplane XR.
func condition(t, status string) map[string]any {
	return map[string]any{
		"type":               t,
		"status":             status,
		"lastTransitionTime": time.Now().Format(time.RFC3339),
	}
}

// buildReconcilerWithExtra seeds the scheme-registered fake client with both
// the RuneBenchmark under test and the infrastructureRef target.
func buildReconcilerWithExtra(t *testing.T, bench *benchv1alpha1.RuneBenchmark, extras ...client.Object) *RuneBenchmarkReconciler {
	t.Helper()
	s := controllersTestScheme(t)
	objs := []client.Object{bench}
	objs = append(objs, extras...)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).
		WithObjects(objs...).
		Build()
	return &RuneBenchmarkReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(50)}
}

func baseBenchmark(ns, name string) *benchv1alpha1.RuneBenchmark {
	return &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: "http://example.invalid",
			Workflow:   "benchmark",
			Question:   "ok?",
		},
	}
}

// drainEvents pulls every queued event into a single joined string for
// assert_contains-style checks without blocking if none are queued.
func drainEvents(r *RuneBenchmarkReconciler) string {
	rec, ok := r.Recorder.(*record.FakeRecorder)
	if !ok {
		return ""
	}
	var got []string
	for {
		select {
		case e := <-rec.Events:
			got = append(got, e)
		default:
			return strings.Join(got, "\n")
		}
	}
}

// TestCheckInfrastructureRef_Nil verifies the fast-path: a benchmark with no
// infrastructureRef returns (true, "", nil) regardless of cluster state.
func TestCheckInfrastructureRef_Nil(t *testing.T) {
	bench := baseBenchmark("rune", "bench")
	r := buildReconcilerWithExtra(t, bench)

	ready, reason, err := r.checkInfrastructureRef(context.Background(), bench)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ready {
		t.Fatalf("expected ready=true with no infrastructureRef, got ready=%v reason=%q", ready, reason)
	}
	if reason != "" {
		t.Fatalf("expected empty reason, got %q", reason)
	}
}

// TestCheckInfrastructureRef_MissingFields covers the partial-ref case: the
// user set an infrastructureRef but left one of the three required keys
// empty. We return not-ready with a descriptive reason and no error.
func TestCheckInfrastructureRef_MissingFields(t *testing.T) {
	bench := baseBenchmark("rune", "bench")
	bench.Spec.InfrastructureRef = &corev1.ObjectReference{
		APIVersion: "database.infra.rune.ai/v1alpha1",
		Kind:       "RuneDatabase",
		// Name intentionally missing
	}
	r := buildReconcilerWithExtra(t, bench)

	ready, reason, err := r.checkInfrastructureRef(context.Background(), bench)
	if err != nil {
		t.Fatalf("expected nil err for missing fields, got %v", err)
	}
	if ready {
		t.Fatalf("expected ready=false, got ready=true")
	}
	if !strings.Contains(reason, "must set") {
		t.Fatalf("expected 'must set' guidance in reason, got %q", reason)
	}
}

// TestCheckInfrastructureRef_BadAPIVersion covers the schema.ParseGroupVersion
// failure path: malformed apiVersion surfaces as (false, reason, err).
func TestCheckInfrastructureRef_BadAPIVersion(t *testing.T) {
	bench := baseBenchmark("rune", "bench")
	bench.Spec.InfrastructureRef = &corev1.ObjectReference{
		APIVersion: "not a valid / group / version",
		Kind:       "RuneDatabase",
		Name:       "rune-database",
	}
	r := buildReconcilerWithExtra(t, bench)

	ready, reason, err := r.checkInfrastructureRef(context.Background(), bench)
	if ready {
		t.Fatalf("expected ready=false")
	}
	if err == nil {
		t.Fatalf("expected non-nil error for malformed apiVersion")
	}
	if !strings.Contains(reason, "invalid infrastructureRef apiVersion") {
		t.Fatalf("expected 'invalid apiVersion' reason, got %q", reason)
	}
}

// TestCheckInfrastructureRef_NotFound covers the GET-returns-NotFound path.
// The operator treats a missing target as "not ready yet" (transient) —
// err is propagated so callers can decide policy, reason describes the miss.
func TestCheckInfrastructureRef_NotFound(t *testing.T) {
	bench := baseBenchmark("rune", "bench")
	bench.Spec.InfrastructureRef = &corev1.ObjectReference{
		APIVersion: "database.infra.rune.ai/v1alpha1",
		Kind:       "RuneDatabase",
		Name:       "absent",
	}
	r := buildReconcilerWithExtra(t, bench) // no RuneDatabase in the cluster

	ready, reason, err := r.checkInfrastructureRef(context.Background(), bench)
	if ready {
		t.Fatalf("expected ready=false for missing target")
	}
	if err == nil {
		t.Fatalf("expected NotFound error to bubble up")
	}
	if !strings.Contains(reason, "cannot fetch infrastructureRef") {
		t.Fatalf("expected fetch-failure reason, got %q", reason)
	}
}

// TestCheckInfrastructureRef_NotReady covers the found-but-conditions-missing
// and found-but-False paths. Both map to (false, reason, nil) — no error,
// just a requeue hint.
func TestCheckInfrastructureRef_NotReady(t *testing.T) {
	cases := []struct {
		name   string
		conds  []map[string]any
		expect string // substring we want to see in the reason
	}{
		{
			name:   "no conditions yet",
			conds:  nil,
			expect: "not ready (Synced=false Ready=false)",
		},
		{
			name: "only Synced=True",
			conds: []map[string]any{
				condition("Synced", "True"),
			},
			expect: "Synced=true Ready=false",
		},
		{
			name: "Synced=True, Ready=False",
			conds: []map[string]any{
				condition("Synced", "True"),
				condition("Ready", "False"),
			},
			expect: "Synced=true Ready=false",
		},
		{
			name: "Synced=False, Ready=True",
			conds: []map[string]any{
				condition("Synced", "False"),
				condition("Ready", "True"),
			},
			expect: "Synced=false Ready=true",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bench := baseBenchmark("rune", "bench")
			bench.Spec.InfrastructureRef = &corev1.ObjectReference{
				APIVersion: "database.infra.rune.ai/v1alpha1",
				Kind:       "RuneDatabase",
				Name:       "rune-database",
			}
			xr := newRunedatabaseXR("rune-database", "rune", tc.conds)
			r := buildReconcilerWithExtra(t, bench, xr)

			ready, reason, err := r.checkInfrastructureRef(context.Background(), bench)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ready {
				t.Fatalf("expected ready=false, got true (reason=%q)", reason)
			}
			if !strings.Contains(reason, tc.expect) {
				t.Fatalf("expected reason to contain %q, got %q", tc.expect, reason)
			}
		})
	}
}

// TestCheckInfrastructureRef_Ready covers the happy path: Synced=True AND
// Ready=True — proceed with benchmark.
func TestCheckInfrastructureRef_Ready(t *testing.T) {
	bench := baseBenchmark("rune", "bench")
	bench.Spec.InfrastructureRef = &corev1.ObjectReference{
		APIVersion: "database.infra.rune.ai/v1alpha1",
		Kind:       "RuneDatabase",
		Name:       "rune-database",
	}
	xr := newRunedatabaseXR("rune-database", "rune", []map[string]any{
		condition("Synced", "True"),
		condition("Ready", "True"),
	})
	r := buildReconcilerWithExtra(t, bench, xr)

	ready, reason, err := r.checkInfrastructureRef(context.Background(), bench)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ready {
		t.Fatalf("expected ready=true, got false (reason=%q)", reason)
	}
	if reason != "" {
		t.Fatalf("expected empty reason on happy path, got %q", reason)
	}
}

// TestCheckInfrastructureRef_NamespaceFallback verifies that leaving
// ObjectReference.Namespace empty falls back to the RuneBenchmark's namespace.
func TestCheckInfrastructureRef_NamespaceFallback(t *testing.T) {
	bench := baseBenchmark("my-rune", "bench")
	bench.Spec.InfrastructureRef = &corev1.ObjectReference{
		APIVersion: "database.infra.rune.ai/v1alpha1",
		Kind:       "RuneDatabase",
		Name:       "rune-database",
		// Namespace intentionally empty: must resolve to bench.Namespace.
	}
	// Seed the target in a *different* namespace to prove the fallback:
	// resolving to "my-rune" must succeed, resolving anywhere else must not.
	xr := newRunedatabaseXR("rune-database", "my-rune", []map[string]any{
		condition("Synced", "True"),
		condition("Ready", "True"),
	})
	r := buildReconcilerWithExtra(t, bench, xr)

	ready, reason, err := r.checkInfrastructureRef(context.Background(), bench)
	if err != nil || !ready {
		t.Fatalf("expected namespace fallback success, got ready=%v err=%v reason=%q", ready, err, reason)
	}
}

// TestInfrastructureConditions_MalformedSlice exercises the silent-false path
// when status.conditions is present but not a well-formed slice-of-maps.
// Non-map elements are tolerated (ignored) and the result is (false, false).
func TestInfrastructureConditions_MalformedSlice(t *testing.T) {
	u := &unstructured.Unstructured{}
	u.Object = map[string]any{
		"status": map[string]any{
			// NestedSlice deep-copies each element with
			// runtime.DeepCopyJSONValue, so stick to JSON types
			// (string / float64 / bool / map / slice / nil).
			"conditions": []any{"not-a-map", float64(42), nil},
		},
	}
	synced, ready := infrastructureConditions(u)
	if synced || ready {
		t.Fatalf("expected both false, got synced=%v ready=%v", synced, ready)
	}
}

// TestInfrastructureConditions_MissingStatus covers the early-return when the
// unstructured object has no .status.conditions at all.
func TestInfrastructureConditions_MissingStatus(t *testing.T) {
	u := &unstructured.Unstructured{}
	u.Object = map[string]any{"metadata": map[string]any{"name": "x"}}
	synced, ready := infrastructureConditions(u)
	if synced || ready {
		t.Fatalf("expected both false with no status.conditions, got synced=%v ready=%v", synced, ready)
	}
}

// TestReconcile_InfrastructureRefNotReady_Requeues wires the whole reconciler:
// a benchmark with an unready infrastructureRef must come back with a 30-second
// requeue, an InfrastructureNotReady Warning event, and NO attempted HTTP call
// (we use an URL that would explode on connect; the test passes only if the
// reconciler short-circuited before executeBenchmark).
func TestReconcile_InfrastructureRefNotReady_Requeues(t *testing.T) {
	bench := baseBenchmark("rune", "bench")
	bench.Spec.APIBaseURL = "http://127.0.0.1:1" // closed port; any HTTP call would fail loudly
	bench.Spec.InfrastructureRef = &corev1.ObjectReference{
		APIVersion: "database.infra.rune.ai/v1alpha1",
		Kind:       "RuneDatabase",
		Name:       "rune-database",
	}
	xr := newRunedatabaseXR("rune-database", "rune", []map[string]any{
		condition("Synced", "True"),
		condition("Ready", "False"),
	})
	r := buildReconcilerWithExtra(t, bench, xr)

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "rune", Name: "bench"},
	})
	if err != nil {
		t.Fatalf("expected nil err on not-ready requeue, got %v", err)
	}
	if res.RequeueAfter != 30*time.Second {
		t.Fatalf("expected 30s requeue, got %v", res.RequeueAfter)
	}
	ev := drainEvents(r)
	if !strings.Contains(ev, "InfrastructureNotReady") {
		t.Fatalf("expected InfrastructureNotReady event, got %q", ev)
	}
}

// TestReconcile_InfrastructureRefGetError_Requeues covers the RBAC-denied /
// NotFound path at reconciler level: it must surface as a 30s requeue with
// an InfrastructureNotReady event, not a hard reconcile failure.
func TestReconcile_InfrastructureRefGetError_Requeues(t *testing.T) {
	bench := baseBenchmark("rune", "bench")
	bench.Spec.APIBaseURL = "http://127.0.0.1:1"
	bench.Spec.InfrastructureRef = &corev1.ObjectReference{
		APIVersion: "database.infra.rune.ai/v1alpha1",
		Kind:       "RuneDatabase",
		Name:       "nope",
	}
	r := buildReconcilerWithExtra(t, bench) // no XR seeded

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "rune", Name: "bench"},
	})
	if err != nil {
		t.Fatalf("expected nil err on lookup-failure requeue, got %v", err)
	}
	if res.RequeueAfter != 30*time.Second {
		t.Fatalf("expected 30s requeue, got %v", res.RequeueAfter)
	}
	ev := drainEvents(r)
	if !strings.Contains(ev, "InfrastructureNotReady") {
		t.Fatalf("expected InfrastructureNotReady event, got %q", ev)
	}
}

// TestReconcile_InfrastructureRefReady_ProceedsToHTTP verifies that when the
// ref is ready the reconciler moves past the gate and into executeBenchmark.
// We point APIBaseURL at a closed port so executeBenchmark FAILS, which is
// fine: the assertion is that we reached that call at all. Observation:
// status.lastScheduleTime gets stamped only when the infra gate passes.
func TestReconcile_InfrastructureRefReady_ProceedsToHTTP(t *testing.T) {
	bench := baseBenchmark("rune", "bench")
	bench.Spec.APIBaseURL = "http://127.0.0.1:1" // connect refused -> run.Status=failed
	bench.Spec.InfrastructureRef = &corev1.ObjectReference{
		APIVersion: "database.infra.rune.ai/v1alpha1",
		Kind:       "RuneDatabase",
		Name:       "rune-database",
	}
	xr := newRunedatabaseXR("rune-database", "rune", []map[string]any{
		condition("Synced", "True"),
		condition("Ready", "True"),
	})
	r := buildReconcilerWithExtra(t, bench, xr)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "rune", Name: "bench"},
	})
	// Connection-refused errors are surfaced through the RunFailed
	// path (RunFailed condition + backoff requeue), not as a reconcile error.
	if err != nil {
		t.Fatalf("expected nil err (RunFailed handled via status), got %v", err)
	}

	var refreshed benchv1alpha1.RuneBenchmark
	if getErr := r.Get(context.Background(), types.NamespacedName{Namespace: "rune", Name: "bench"}, &refreshed); getErr != nil {
		t.Fatalf("fetch refreshed bench: %v", getErr)
	}
	if refreshed.Status.LastScheduleTime == nil {
		t.Fatalf("expected LastScheduleTime to be stamped after passing the infra gate")
	}
	if refreshed.Status.LastRun.Status != "failed" {
		t.Fatalf("expected failed RunRecord (closed port), got %q", refreshed.Status.LastRun.Status)
	}
	if !strings.Contains(refreshed.Status.LastRun.Error, "connect") &&
		!strings.Contains(refreshed.Status.LastRun.Error, "refused") &&
		!errors.Is(err, nil) /* satisfy lint */ {
		t.Logf("RunRecord.Error (informational): %q", refreshed.Status.LastRun.Error)
	}
}

// TestDeepCopy_InfrastructureRefIsIndependent protects against a regression
// of the pre-existing shallow-copy bug on RuneBenchmarkSpec. Mutating the
// copy's ref MUST NOT mutate the original's ref.
func TestDeepCopy_InfrastructureRefIsIndependent(t *testing.T) {
	orig := baseBenchmark("rune", "bench")
	orig.Spec.InfrastructureRef = &corev1.ObjectReference{
		APIVersion: "database.infra.rune.ai/v1alpha1",
		Kind:       "RuneDatabase",
		Name:       "rune-database",
		Namespace:  "rune",
	}

	cp := orig.DeepCopy()
	cp.Spec.InfrastructureRef.Name = "different"

	if orig.Spec.InfrastructureRef.Name == cp.Spec.InfrastructureRef.Name {
		t.Fatalf("DeepCopy aliased InfrastructureRef — mutation leaked to original")
	}
}
