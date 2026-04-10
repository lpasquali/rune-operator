// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	benchv1alpha1 "github.com/lpasquali/rune-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type setupManagerStub struct {
	ctrl.Manager
	recorder record.EventRecorder
}

func (m *setupManagerStub) GetEventRecorderFor(string) record.EventRecorder { return m.recorder }

// newJobMockServer creates a test server that handles POST (job submission) and
// GET (job status polling). POST returns 202 with the given jobID. GET returns
// "succeeded" immediately. Use customGET to override GET behavior.
func newJobMockServer(jobID string, customGET http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"` + jobID + `","status":"accepted"}`))
			return
		}
		if customGET != nil {
			customGET(w, r)
			return
		}
		// Default GET: immediate success
		_, _ = w.Write([]byte(`{"status":"succeeded"}`))
	}))
}

type failingStatusWriter struct {
	err error
}

func (w failingStatusWriter) Create(context.Context, client.Object, client.Object, ...client.SubResourceCreateOption) error {
	return nil
}
func (w failingStatusWriter) Update(context.Context, client.Object, ...client.SubResourceUpdateOption) error {
	return w.err
}
func (w failingStatusWriter) Patch(context.Context, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
	return nil
}
func (w failingStatusWriter) Apply(context.Context, runtime.ApplyConfiguration, ...client.SubResourceApplyOption) error {
	return nil
}

type failingStatusClient struct {
	client.Client
	err error
}

func (c failingStatusClient) Status() client.StatusWriter {
	return failingStatusWriter{err: c.err}
}

type failingListClient struct {
	client.Client
	err error
}

func (c failingListClient) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return c.err
}

type failingGetClient struct {
	client.Client
	err error
}

func (c failingGetClient) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return c.err
}

func buildReconciler(t *testing.T, objs ...runtime.Object) (*RuneBenchmarkReconciler, *runtime.Scheme) {
	t.Helper()
	s := runtime.NewScheme()
	if err := benchv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add benchmark scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(objs...).Build()
	return &RuneBenchmarkReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(50)}, s
}

func TestReconcileNotFound(t *testing.T) {
	r, _ := buildReconciler(t)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "missing"}})
	if err != nil {
		t.Fatalf("expected nil error for not found, got %v", err)
	}
	if res.Requeue || res.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue result: %+v", res)
	}
}

func TestReconcileGetError(t *testing.T) {
	r, _ := buildReconciler(t)
	r.Client = failingGetClient{Client: r.Client, err: context.DeadlineExceeded}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected get error to be returned, got %v", err)
	}
}

func TestSetupWithManagerNil(t *testing.T) {
	r := &RuneBenchmarkReconciler{}
	if err := r.SetupWithManager(nil); err == nil {
		t.Fatalf("expected error for nil manager")
	}
}

func TestSetupWithManagerSuccessAndError(t *testing.T) {
	old := setupControllerWithManager
	t.Cleanup(func() { setupControllerWithManager = old })

	r := &RuneBenchmarkReconciler{}
	mgr := &setupManagerStub{recorder: record.NewFakeRecorder(10)}

	setupControllerWithManager = func(ctrl.Manager, *RuneBenchmarkReconciler) error { return nil }
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatalf("expected setup success, got %v", err)
	}
	if r.Recorder == nil {
		t.Fatalf("expected recorder to be set")
	}

	setupControllerWithManager = func(ctrl.Manager, *RuneBenchmarkReconciler) error { return context.Canceled }
	if err := r.SetupWithManager(mgr); err == nil {
		t.Fatalf("expected setup error")
	}
}

func TestReconcileSuspend(t *testing.T) {
	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns"},
		Spec:       benchv1alpha1.RuneBenchmarkSpec{Suspend: true},
	}
	r, _ := buildReconciler(t, obj)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("reconcile suspend: %v", err)
	}
	if res.RequeueAfter != 5*time.Minute {
		t.Fatalf("expected 5m requeue for suspended benchmark, got %v", res.RequeueAfter)
	}
}

func TestReconcileSuccessAndStatusUpdate(t *testing.T) {
	pollCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"job-123","status":"accepted"}`))
			return
		}
		// GET /v1/jobs/job-123 — first call returns "running", then "succeeded"
		pollCount++
		if pollCount < 2 {
			_, _ = w.Write([]byte(`{"status":"running"}`))
		} else {
			_, _ = w.Write([]byte(`{"status":"succeeded"}`))
		}
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 2},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:          ts.URL,
			Workflow:            "wf",
			Question:            "q",
			Model:               "m",
			PollIntervalSeconds: 2, // minimum allowed; fast test
		},
	}
	r, _ := buildReconciler(t, obj)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("reconcile success path failed: %v", err)
	}
	if res.RequeueAfter != 10*time.Minute {
		t.Fatalf("expected default 10m requeue, got %v", res.RequeueAfter)
	}

	updated := &benchv1alpha1.RuneBenchmark{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "rb"}, updated); err != nil {
		t.Fatalf("fetch updated object: %v", err)
	}
	if updated.Status.LastRun.RunID != "job-123" || updated.Status.LastRun.Status != "succeeded" {
		t.Fatalf("unexpected last run status: %+v", updated.Status.LastRun)
	}
	if updated.Status.LastSuccessfulTime == nil {
		t.Fatalf("expected LastSuccessfulTime to be set")
	}
	if len(updated.Status.Conditions) == 0 || updated.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("expected ready=true condition, got %+v", updated.Status.Conditions)
	}
}

func TestReconcileBudgetExceededCondition(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/finops/simulate") {
			_, _ = w.Write([]byte(`{"projected_cost_usd": 50.0, "cost_high_usd": 100.0}`))
			return
		}
		if r.Method == http.MethodPost {
			t.Fatal("job POST should not run after budget failure")
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	qty := resource.MustParse("1")
	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: ts.URL,
			Workflow:   "benchmark",
			Model:      "m",
			Budget:     benchv1alpha1.Budget{MaxCostUSD: &qty},
		},
	}
	r, _ := buildReconciler(t, obj)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("expected reconcile to swallow error, got %v", err)
	}
	updated := &benchv1alpha1.RuneBenchmark{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "rb"}, updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.LastRun.Status != "failed" {
		t.Fatalf("expected failed last run, got %+v", updated.Status.LastRun)
	}
	found := false
	for _, c := range updated.Status.Conditions {
		if c.Type == "Ready" && c.Reason == "BudgetExceeded" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected BudgetExceeded condition, got %+v", updated.Status.Conditions)
	}
}

func TestReconcileSuccessWithScheduleAndHistoryTrim(t *testing.T) {
	ts := newJobMockServer("job-abc", nil)
	defer ts.Close()

	history := make([]benchv1alpha1.RunRecord, 21)
	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 3},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: ts.URL,
			Workflow:   "wf",
			Schedule:   "*/15 * * * *",
		},
		Status: benchv1alpha1.RuneBenchmarkStatus{History: history},
	}
	r, _ := buildReconciler(t, obj)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("reconcile scheduled success path failed: %v", err)
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > 15*time.Minute {
		t.Fatalf("expected cron-based requeue within 15m, got %v", res.RequeueAfter)
	}

	updated := &benchv1alpha1.RuneBenchmark{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "rb"}, updated); err != nil {
		t.Fatalf("fetch updated object: %v", err)
	}
	if len(updated.Status.History) != 20 {
		t.Fatalf("expected history to be trimmed to 20 entries, got %d", len(updated.Status.History))
	}
}

func TestReconcileSuccessWithInvalidScheduleFallsBack(t *testing.T) {
	ts := newJobMockServer("job-xyz", nil)
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 4},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: ts.URL,
			Workflow:   "wf",
			Schedule:   "not a cron",
		},
	}
	r, _ := buildReconciler(t, obj)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("reconcile invalid-schedule success path failed: %v", err)
	}
	if res.RequeueAfter != 10*time.Minute {
		t.Fatalf("expected fallback 10m requeue, got %v", res.RequeueAfter)
	}
}

func TestReconcilePanicRecovery(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: ts.URL,
			Workflow:   "wf",
		},
	}
	r, _ := buildReconciler(t, obj)
	r.Recorder = nil

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err == nil || !strings.Contains(err.Error(), "panic recovered") {
		t.Fatalf("expected recovered panic error, got %v", err)
	}
}

func TestReconcileErrorAndBackoff(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:     ts.URL,
			Workflow:       "wf",
			BackoffSeconds: 7,
			TimeoutSeconds: 1,
		},
	}
	r, _ := buildReconciler(t, obj)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("reconcile error path should return nil err, got %v", err)
	}
	if res.RequeueAfter != 7*time.Second {
		t.Fatalf("expected backoff requeue 7s, got %v", res.RequeueAfter)
	}

	updated := &benchv1alpha1.RuneBenchmark{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "rb"}, updated); err != nil {
		t.Fatalf("fetch updated object: %v", err)
	}
	if updated.Status.ConsecutiveFailures != 1 {
		t.Fatalf("expected one failure, got %d", updated.Status.ConsecutiveFailures)
	}
	if len(updated.Status.Conditions) == 0 || updated.Status.Conditions[0].Status != metav1.ConditionFalse {
		t.Fatalf("expected ready=false condition, got %+v", updated.Status.Conditions)
	}
	if updated.Status.LastRun.Status != "failed" || updated.Status.LastRun.Error == "" {
		t.Fatalf("expected failed last run with error recorded, got %+v", updated.Status.LastRun)
	}
}

func TestReconcileFailsFastOnInvalidTokenSecretRef(t *testing.T) {
	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:        "http://example.invalid",
			Workflow:          "wf",
			BackoffSeconds:    7,
			TimeoutSeconds:    1,
			APITokenSecretRef: "badref",
		},
	}
	r, _ := buildReconciler(t, obj)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("reconcile should not return an outer error, got %v", err)
	}
	if res.RequeueAfter != 7*time.Second {
		t.Fatalf("expected backoff requeue 7s, got %v", res.RequeueAfter)
	}

	updated := &benchv1alpha1.RuneBenchmark{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "rb"}, updated); err != nil {
		t.Fatalf("fetch updated object: %v", err)
	}
	if updated.Status.LastRun.Status != "failed" || !strings.Contains(updated.Status.LastRun.Error, "namespace/name") {
		t.Fatalf("expected invalid token secret ref to be persisted as a failed run, got %+v", updated.Status.LastRun)
	}
}

func TestReconcileStatusUpdateErrorOnFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: ts.URL,
			Workflow:   "wf",
		},
	}
	r, s := buildReconciler(t, obj)
	r.Client = failingStatusClient{Client: r.Client, err: context.Canceled}
	r.Scheme = s

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected failure-path status update error, got %v", err)
	}
}

func TestReconcileContinuesWhenActiveScheduleSyncFails(t *testing.T) {
	ts := newJobMockServer("job-sync", nil)
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: ts.URL,
			Workflow:   "wf",
			Suspend:    true,
		},
	}
	r, _ := buildReconciler(t, obj)
	r.Client = failingListClient{Client: r.Client, err: context.DeadlineExceeded}

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("expected reconcile to continue after active schedule sync error, got %v", err)
	}
	if res.RequeueAfter != 5*time.Minute {
		t.Fatalf("expected suspended requeue after sync error, got %v", res.RequeueAfter)
	}
}

func TestReconcileStatusUpdateErrorOnSuccess(t *testing.T) {
	ts := newJobMockServer("job-ok", nil)
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: ts.URL,
			Workflow:   "wf",
		},
	}
	r, s := buildReconciler(t, obj)
	r.Client = failingStatusClient{Client: r.Client, err: context.DeadlineExceeded}
	r.Scheme = s

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err == nil {
		t.Fatalf("expected status update error on success path")
	}
}

func TestExecuteBenchmarkAndReadTokenBranches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"status":"succeeded"}`))
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-value" {
			t.Fatalf("missing auth header, got %q", got)
		}
		if got := r.Header.Get("X-Tenant-ID"); got != "tenant-a" {
			t.Fatalf("missing tenant header, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"job-777"}`))
	}))
	defer server.Close()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sec", Namespace: "ns"},
		Data:       map[string][]byte{"token": []byte(" token-value ")},
	}
	r, _ := buildReconciler(t, secret)

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns"},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:        server.URL,
			Workflow:          "wf",
			Tenant:            "tenant-a",
			APITokenSecretRef: "ns/sec",
		},
	}

	rec, err := r.executeBenchmark(context.Background(), obj, 5*time.Second)
	if err != nil {
		t.Fatalf("executeBenchmark failed: %v", err)
	}
	if rec.RunID != "job-777" || rec.Status != "succeeded" {
		t.Fatalf("unexpected run record: %+v", rec)
	}

	obj.Spec.APIBaseURL = "://bad"
	if _, err := r.executeBenchmark(context.Background(), obj, 5*time.Second); err == nil {
		t.Fatalf("expected request build error for invalid URL")
	}

	obj.Spec.APITokenSecretRef = "not-valid"
	if _, err := r.readToken(context.Background(), obj); err == nil {
		t.Fatalf("expected secret ref format error")
	}

	obj.Spec.APITokenSecretRef = "ns/missing"
	if _, err := r.readToken(context.Background(), obj); err == nil {
		t.Fatalf("expected missing secret error")
	}

	badSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sec2", Namespace: "ns"}}
	r2, _ := buildReconciler(t, badSecret)
	obj.Spec.APITokenSecretRef = "ns/sec2"
	if _, err := r2.readToken(context.Background(), obj); err == nil {
		t.Fatalf("expected token key missing error")
	}
}

func TestExecuteBenchmarkNonJSONBodyAndHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/jobs/wf" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not-json"))
			return
		}
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer ts.Close()

	r, _ := buildReconciler(t)
	obj := &benchv1alpha1.RuneBenchmark{Spec: benchv1alpha1.RuneBenchmarkSpec{APIBaseURL: ts.URL, Workflow: "wf", InsecureTLS: true}}

	rec, err := r.executeBenchmark(context.Background(), obj, 2*time.Second)
	if err == nil {
		t.Fatalf("expected invalid JSON response error")
	}
	if rec.Status != "submitted" {
		t.Fatalf("expected record to remain submitted before reconcile failure handling, got %+v", rec)
	}

	obj.Spec.APIBaseURL = ts.URL + "/err"
	if _, err := r.executeBenchmark(context.Background(), obj, 2*time.Second); err == nil {
		t.Fatalf("expected HTTP status error")
	}
}

func TestExecuteBenchmarkMarshalError(t *testing.T) {
	oldMarshal := jsonMarshal
	t.Cleanup(func() { jsonMarshal = oldMarshal })

	jsonMarshal = func(any) ([]byte, error) {
		return nil, &json.UnsupportedTypeError{Type: nil}
	}

	r, _ := buildReconciler(t)
	obj := &benchv1alpha1.RuneBenchmark{Spec: benchv1alpha1.RuneBenchmarkSpec{APIBaseURL: "http://example.invalid", Workflow: "wf"}}

	if _, err := r.executeBenchmark(context.Background(), obj, time.Second); err == nil || !strings.Contains(err.Error(), "failed to marshal request payload") {
		t.Fatalf("expected marshal failure, got %v", err)
	}
}

func TestSyncActiveSchedulesListError(t *testing.T) {
	r, _ := buildReconciler(t)
	r.Client = failingListClient{Client: r.Client, err: context.DeadlineExceeded}

	if err := r.syncActiveSchedules(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected list error, got %v", err)
	}
}

func TestExecuteBenchmarkJobIDNotString(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"status":"succeeded"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":123}`))
	}))
	defer ts.Close()

	r, _ := buildReconciler(t)
	obj := &benchv1alpha1.RuneBenchmark{Spec: benchv1alpha1.RuneBenchmarkSpec{APIBaseURL: ts.URL, Workflow: "wf"}}

	rec, err := r.executeBenchmark(context.Background(), obj, 2*time.Second)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if rec.Status != "succeeded" || rec.RunID != "" {
		t.Fatalf("expected succeeded with empty run id, got %+v", rec)
	}
}

func TestReadTokenCrossNamespaceRef(t *testing.T) {
	r, _ := buildReconciler(t)
	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns"},
		Spec:       benchv1alpha1.RuneBenchmarkSpec{APITokenSecretRef: "other-ns/sec"},
	}
	_, err := r.readToken(context.Background(), obj)
	if err == nil || !strings.Contains(err.Error(), "namespace must match") {
		t.Fatalf("expected namespace mismatch error, got %v", err)
	}
}

func TestReconcileNotFoundWithSyncError(t *testing.T) {
	r, _ := buildReconciler(t)
	notFoundErr := apierrors.NewNotFound(schema.GroupResource{Group: "bench.rune.ai", Resource: "runebenchmarks"}, "missing")
	r.Client = failingListClient{
		Client: failingGetClient{Client: r.Client, err: notFoundErr},
		err:    context.DeadlineExceeded,
	}

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "missing"}})
	if err != nil {
		t.Fatalf("expected nil error for not found with sync error, got %v", err)
	}
	if res.Requeue || res.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue result: %+v", res)
	}
}

func TestExecuteBenchmarkBodyReadError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, buf, _ := hj.Hijack()
		// Send headers claiming 100 bytes but close immediately — io.ReadAll will get unexpected EOF.
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	defer ts.Close()

	r, _ := buildReconciler(t)
	obj := &benchv1alpha1.RuneBenchmark{Spec: benchv1alpha1.RuneBenchmarkSpec{APIBaseURL: ts.URL, Workflow: "wf"}}

	_, err := r.executeBenchmark(context.Background(), obj, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "failed to read RUNE API response body") {
		t.Fatalf("expected body read error, got %v", err)
	}
}

func TestExecuteBenchmarkHTTPTransportError(t *testing.T) {
	r, _ := buildReconciler(t)
	obj := &benchv1alpha1.RuneBenchmark{Spec: benchv1alpha1.RuneBenchmarkSpec{APIBaseURL: "http://127.0.0.1:1", Workflow: "wf"}}

	if _, err := r.executeBenchmark(context.Background(), obj, 150*time.Millisecond); err == nil {
		t.Fatalf("expected transport error")
	}
}

func TestBuildPayloadAgenticAgent(t *testing.T) {
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Workflow:                    "agentic-agent",
		Question:                    "why is the cluster degraded?",
		Model:                       "llama3.1:8b",
		BackendURL:                  "http://ollama:11434",
		BackendWarmup:               true,
		BackendWarmupTimeoutSeconds: 120,
		Kubeconfig:                  "/etc/kubeconfig",
	}
	p := buildPayload(spec)
	if p["question"] != spec.Question {
		t.Fatalf("unexpected question: %v", p["question"])
	}
	if p["backend_warmup"] != true {
		t.Fatalf("expected backend_warmup=true")
	}
	if p["backend_warmup_timeout"] != 120 {
		t.Fatalf("expected backend_warmup_timeout=120, got %v", p["backend_warmup_timeout"])
	}
	if p["kubeconfig"] != "/etc/kubeconfig" {
		t.Fatalf("unexpected kubeconfig: %v", p["kubeconfig"])
	}
	for _, k := range []string{"vastai", "template_hash", "vastai_stop_instance", "workflow"} {
		if _, ok := p[k]; ok {
			t.Fatalf("agentic-agent payload must not contain key %q", k)
		}
	}
}

func TestBuildPayloadOllamaInstance(t *testing.T) {
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Workflow:     "ollama-instance",
		VastAI:       true,
		TemplateHash: "abc123",
		MinDPH:       0.1,
		MaxDPH:       0.5,
		Reliability:  0.99,
		BackendURL:   "http://ollama:11434",
	}
	p := buildPayload(spec)
	if p["vastai"] != true {
		t.Fatalf("expected vastai=true")
	}
	if p["template_hash"] != "abc123" {
		t.Fatalf("unexpected template_hash: %v", p["template_hash"])
	}
	if p["min_dph"] != 0.1 {
		t.Fatalf("unexpected min_dph: %v", p["min_dph"])
	}
	for _, k := range []string{"question", "model", "kubeconfig", "vastai_stop_instance", "workflow"} {
		if _, ok := p[k]; ok {
			t.Fatalf("ollama-instance payload must not contain key %q", k)
		}
	}
}

func TestBuildPayloadBenchmark(t *testing.T) {
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Workflow:                    "benchmark",
		VastAI:                      true,
		TemplateHash:                "tpl",
		MinDPH:                      0.2,
		MaxDPH:                      0.8,
		Reliability:                 0.95,
		BackendURL:                  "http://ollama:11434",
		Question:                    "q",
		Model:                       "m",
		BackendWarmup:               false,
		BackendWarmupTimeoutSeconds: 60,
		Kubeconfig:                  "/kube/config",
		VastAIStopInstance:          true,
	}
	p := buildPayload(spec)
	for _, k := range []string{
		"vastai", "template_hash", "min_dph", "max_dph", "reliability",
		"backend_url", "question", "model", "backend_warmup", "backend_warmup_timeout",
		"kubeconfig", "vastai_stop_instance",
	} {
		if _, ok := p[k]; !ok {
			t.Fatalf("benchmark payload missing key %q", k)
		}
	}
	if p["vastai_stop_instance"] != true {
		t.Fatalf("expected vastai_stop_instance=true")
	}
	if p["backend_warmup_timeout"] != 60 {
		t.Fatalf("expected backend_warmup_timeout=60, got %v", p["backend_warmup_timeout"])
	}
}

func TestBuildPayloadUnknownWorkflow(t *testing.T) {
	spec := benchv1alpha1.RuneBenchmarkSpec{Workflow: "custom-workflow", Question: "test", Model: "m"}
	p := buildPayload(spec)
	if p["workflow"] != "custom-workflow" {
		t.Fatalf("expected workflow key in fallback payload, got %v", p["workflow"])
	}
}

func TestReconcileUsesWorkflowInURL(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"status":"succeeded"}`))
			return
		}
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"job-path-check"}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: ts.URL,
			Workflow:   "agentic-agent",
			Question:   "q",
			Model:      "m",
		},
	}
	r, _ := buildReconciler(t, obj)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if gotPath != "/v1/jobs/agentic-agent" {
		t.Fatalf("expected request to /v1/jobs/agentic-agent, got %q", gotPath)
	}
}

func TestUpsertConditionAndCronError(t *testing.T) {
	conds := []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse}}
	next := upsertCondition(conds, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue})
	if len(next) != 1 || next[0].Status != metav1.ConditionTrue {
		t.Fatalf("expected condition replacement, got %+v", next)
	}

	next = upsertCondition(next, metav1.Condition{Type: "Other", Status: metav1.ConditionFalse})
	if len(next) != 2 {
		t.Fatalf("expected append for new condition type")
	}

	if _, err := nextFromCron("bad cron", time.Now()); err == nil {
		t.Fatalf("expected parse error for invalid cron")
	}
}

// ---------------------------------------------------------------------------
// Cost estimation pre-flight gate tests
// ---------------------------------------------------------------------------

func TestEstimatesPreflightSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"status":"succeeded"}`))
			return
		}
		if r.URL.Path == "/v1/estimates" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"confidence_score":0.99}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"job-est-ok"}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:   ts.URL,
			Workflow:     "benchmark",
			VastAI:       true,
			TemplateHash: "tpl",
			MinDPH:       0.1,
			MaxDPH:       0.5,
			Reliability:  0.95,
		},
	}
	r, _ := buildReconciler(t, obj)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("expected reconcile to succeed, got %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected requeue after success")
	}

	updated := &benchv1alpha1.RuneBenchmark{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "rb"}, updated); err != nil {
		t.Fatalf("fetch updated object: %v", err)
	}
	if updated.Status.LastRun.Status != "succeeded" {
		t.Fatalf("expected succeeded, got %q", updated.Status.LastRun.Status)
	}
}

func TestEstimatesPreflightBelowThreshold(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/estimates" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"confidence_score":0.80}`))
			return
		}
		t.Fatal("job endpoint should not be reached")
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:     ts.URL,
			Workflow:       "benchmark",
			VastAI:         true,
			BackoffSeconds: 10,
		},
	}
	r, _ := buildReconciler(t, obj)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("reconcile should return nil error, got %v", err)
	}
	if res.RequeueAfter != 10*time.Second {
		t.Fatalf("expected backoff requeue 10s, got %v", res.RequeueAfter)
	}

	updated := &benchv1alpha1.RuneBenchmark{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "rb"}, updated); err != nil {
		t.Fatalf("fetch updated: %v", err)
	}
	if updated.Status.LastRun.Status != "failed" {
		t.Fatalf("expected failed, got %q", updated.Status.LastRun.Status)
	}
	if !strings.Contains(updated.Status.LastRun.Error, "confidence") {
		t.Fatalf("expected confidence error in last run, got %q", updated.Status.LastRun.Error)
	}
}

func TestEstimatesPreflightHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/estimates" {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		t.Fatal("job endpoint should not be reached")
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:     ts.URL,
			Workflow:       "benchmark",
			VastAI:         true,
			BackoffSeconds: 5,
		},
	}
	r, _ := buildReconciler(t, obj)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("reconcile should return nil error, got %v", err)
	}
	if res.RequeueAfter != 5*time.Second {
		t.Fatalf("expected backoff requeue 5s, got %v", res.RequeueAfter)
	}

	updated := &benchv1alpha1.RuneBenchmark{}
	_ = r.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "rb"}, updated)
	if updated.Status.LastRun.Status != "failed" {
		t.Fatalf("expected failed, got %q", updated.Status.LastRun.Status)
	}
}

func TestEstimatesPreflightParseError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/estimates" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not-json"))
			return
		}
		t.Fatal("job endpoint should not be reached")
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:     ts.URL,
			Workflow:       "benchmark",
			VastAI:         true,
			BackoffSeconds: 5,
		},
	}
	r, _ := buildReconciler(t, obj)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("reconcile should return nil error, got %v", err)
	}
	if res.RequeueAfter != 5*time.Second {
		t.Fatalf("expected backoff, got %v", res.RequeueAfter)
	}

	updated := &benchv1alpha1.RuneBenchmark{}
	_ = r.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "rb"}, updated)
	if !strings.Contains(updated.Status.LastRun.Error, "parse response") {
		t.Fatalf("expected parse error, got %q", updated.Status.LastRun.Error)
	}
}

func TestEstimatesSkippedForLocalWorkflow(t *testing.T) {
	estimatesCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"status":"succeeded"}`))
			return
		}
		if r.URL.Path == "/v1/estimates" {
			estimatesCalled = true
			t.Fatal("estimates should not be called when VastAI is false")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"job-local"}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: ts.URL,
			Workflow:   "benchmark",
			VastAI:     false,
		},
	}
	r, _ := buildReconciler(t, obj)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if estimatesCalled {
		t.Fatal("estimates endpoint should not be called when VastAI is false")
	}
}

func TestBuildPayloadAgenticAgentWithAgent(t *testing.T) {
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Workflow: "agentic-agent",
		Agent:    "holmes",
	}
	p := buildPayload(spec)
	if p["agent"] != "holmes" {
		t.Fatalf("expected agent=holmes, got %v", p["agent"])
	}
}

func TestBuildPayloadAgenticAgentWithoutAgent(t *testing.T) {
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Workflow: "agentic-agent",
	}
	p := buildPayload(spec)
	if _, ok := p["agent"]; ok {
		t.Fatalf("expected agent key to be omitted when empty, got %v", p["agent"])
	}
}

func TestBuildPayloadBenchmarkWithAttestationRequired(t *testing.T) {
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Workflow:            "benchmark",
		AttestationRequired: true,
	}
	p := buildPayload(spec)
	if p["attestation_required"] != true {
		t.Fatalf("expected attestation_required=true, got %v", p["attestation_required"])
	}
}

func TestBuildPayloadBenchmarkWithAttestationRequiredFalse(t *testing.T) {
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Workflow:            "benchmark",
		AttestationRequired: false,
	}
	p := buildPayload(spec)
	if p["attestation_required"] != false {
		t.Fatalf("expected attestation_required=false, got %v", p["attestation_required"])
	}
}

func TestBuildPayloadBackendTypeNonDefault(t *testing.T) {
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Workflow:    "agentic-agent",
		BackendType: "k8s-inference",
		Question:    "test",
	}
	p := buildPayload(spec)
	if p["backend_type"] != "k8s-inference" {
		t.Errorf("expected backend_type=k8s-inference, got %v", p["backend_type"])
	}
}

func TestBuildPayloadBackendTypeInAllWorkflows(t *testing.T) {
	for _, wf := range []string{"agentic-agent", "ollama-instance", "benchmark", "unknown"} {
		spec := benchv1alpha1.RuneBenchmarkSpec{
			Workflow:    wf,
			BackendType: "ollama",
		}
		p := buildPayload(spec)
		if p["backend_type"] != "ollama" {
			t.Errorf("workflow %q: expected backend_type=ollama, got %v", wf, p["backend_type"])
		}
	}
}

// ---------------------------------------------------------------------------
// Idempotency key tests
// ---------------------------------------------------------------------------

func TestIdempotencyKeyHeaderSent(t *testing.T) {
	var gotKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			gotKey = r.Header.Get("Idempotency-Key")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"job-idem","status":"succeeded"}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 3},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: ts.URL,
			Workflow:   "agentic-agent",
		},
	}
	r, _ := buildReconciler(t, obj)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotKey == "" {
		t.Fatal("expected Idempotency-Key header to be set")
	}
	if !strings.Contains(gotKey, "ns/rb/3/") {
		t.Fatalf("idempotency key should contain namespace/name/generation, got: %s", gotKey)
	}
}

func TestIdempotencyKeyFormat(t *testing.T) {
	var gotKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			gotKey = r.Header.Get("Idempotency-Key")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"job-det","status":"succeeded"}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 5},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: ts.URL,
			Workflow:   "agentic-agent",
		},
	}
	r, _ := buildReconciler(t, obj)
	_, _ = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if gotKey == "" {
		t.Fatal("expected Idempotency-Key header")
	}
	// Key format: namespace/name/generation/scheduleTime
	if !strings.HasPrefix(gotKey, "ns/rb/5/") {
		t.Fatalf("key should start with ns/rb/5/, got: %s", gotKey)
	}
}

// ---------------------------------------------------------------------------
// getJobStatus and polling unit tests
// ---------------------------------------------------------------------------

func TestGetJobStatusSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tenant-ID") != "t1" {
			t.Errorf("expected tenant header t1")
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("expected auth header")
		}
		_, _ = w.Write([]byte(`{"status":"succeeded","message":"done"}`))
	}))
	defer ts.Close()

	result, err := getJobStatus(context.Background(), ts.URL, "j1", "t1", http.DefaultClient, "tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "succeeded" {
		t.Fatalf("expected succeeded, got %s", result.Status)
	}
}

func TestGetJobStatusRequestError(t *testing.T) {
	// Invalid URL triggers request creation error
	_, err := getJobStatus(context.Background(), "://bad", "j1", "", http.DefaultClient, "")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestGetJobStatusNetworkError(t *testing.T) {
	// Connect to a port that refuses connections
	_, err := getJobStatus(context.Background(), "http://127.0.0.1:1", "j1", "", &http.Client{Timeout: time.Second}, "")
	if err == nil {
		t.Fatal("expected error for connection refused")
	}
}

func TestGetJobStatusHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer ts.Close()

	_, err := getJobStatus(context.Background(), ts.URL, "j1", "", http.DefaultClient, "")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("error should mention status 500: %v", err)
	}
}

func TestGetJobStatusInvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer ts.Close()

	_, err := getJobStatus(context.Background(), ts.URL, "j1", "", http.DefaultClient, "")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestGetJobStatusNoTenantNoToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tenant-ID") != "" {
			t.Error("expected no tenant header")
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("expected no auth header")
		}
		_, _ = w.Write([]byte(`{"status":"running"}`))
	}))
	defer ts.Close()

	result, err := getJobStatus(context.Background(), ts.URL, "j1", "", http.DefaultClient, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "running" {
		t.Fatalf("expected running, got %s", result.Status)
	}
}

func TestPollTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"j-timeout"}`))
			return
		}
		// Always return "running" — never completes
		_, _ = w.Write([]byte(`{"status":"running"}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:          ts.URL,
			Workflow:            "agentic-agent",
			PollIntervalSeconds: 2,
		},
	}
	rec, _ := buildReconciler(t, obj)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = rec.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	updated := &benchv1alpha1.RuneBenchmark{}
	if err := rec.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "rb"}, updated); err != nil {
		t.Fatalf("failed to get: %v", err)
	}
	if updated.Status.LastRun.Status != "failed" {
		t.Fatalf("expected failed, got %s", updated.Status.LastRun.Status)
	}
	if !strings.Contains(updated.Status.LastRun.Error, "timeout") {
		t.Fatalf("error should mention timeout: %s", updated.Status.LastRun.Error)
	}
}

func TestPollJobFailedWithMessage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"j-msg"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"failed","message":"disk full"}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:          ts.URL,
			Workflow:            "agentic-agent",
			PollIntervalSeconds: 2,
		},
	}
	rec, _ := buildReconciler(t, obj)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = rec.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	updated := &benchv1alpha1.RuneBenchmark{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "rb"}, updated)
	if !strings.Contains(updated.Status.LastRun.Error, "disk full") {
		t.Fatalf("error should contain message: %s", updated.Status.LastRun.Error)
	}
}

func TestPollJobFailed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"j-fail"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"failed","error":"OOM killed"}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:          ts.URL,
			Workflow:            "agentic-agent",
			PollIntervalSeconds: 2,
		},
	}
	rec, _ := buildReconciler(t, obj)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = rec.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	updated := &benchv1alpha1.RuneBenchmark{}
	if err := rec.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "rb"}, updated); err != nil {
		t.Fatalf("failed to get updated object: %v", err)
	}
	if updated.Status.LastRun.Status != "failed" {
		t.Fatalf("expected failed status, got %s", updated.Status.LastRun.Status)
	}
	if !strings.Contains(updated.Status.LastRun.Error, "OOM killed") {
		t.Fatalf("error should contain OOM killed: %s", updated.Status.LastRun.Error)
	}
}

func TestPollTransientError(t *testing.T) {
	pollCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"j-transient"}`))
			return
		}
		pollCount++
		if pollCount < 2 {
			// First GET: transient error
			w.WriteHeader(500)
			_, _ = w.Write([]byte("temporary failure"))
			return
		}
		// Second GET: success
		_, _ = w.Write([]byte(`{"status":"succeeded"}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:          ts.URL,
			Workflow:            "agentic-agent",
			PollIntervalSeconds: 2,
		},
	}
	rec, _ := buildReconciler(t, obj)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := rec.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("expected success after transient error recovery: %v", err)
	}
}

func TestPollCapturesResult(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"j-res"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"succeeded","result":{"answer":"Pod OOM killed"}}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:          ts.URL,
			Workflow:            "agentic-agent",
			PollIntervalSeconds: 2,
		},
	}
	rec, _ := buildReconciler(t, obj)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = rec.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	updated := &benchv1alpha1.RuneBenchmark{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "rb"}, updated)
	if updated.Status.LastRun.Result == "" {
		t.Fatal("expected result to be captured")
	}
	if !strings.Contains(updated.Status.LastRun.Result, "Pod OOM killed") {
		t.Fatalf("result should contain answer: %s", updated.Status.LastRun.Result)
	}
}

func TestPollNoResultField(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"j-nores"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"succeeded"}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:          ts.URL,
			Workflow:            "agentic-agent",
			PollIntervalSeconds: 2,
		},
	}
	rec, _ := buildReconciler(t, obj)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = rec.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	updated := &benchv1alpha1.RuneBenchmark{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "rb"}, updated)
	if updated.Status.LastRun.Result != "" {
		t.Fatalf("expected empty result, got: %s", updated.Status.LastRun.Result)
	}
}

func TestPollSkippedWhenNoJobID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			t.Fatal("GET should not be called when no job_id")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: ts.URL,
			Workflow:   "agentic-agent",
		},
	}
	r, _ := buildReconciler(t, obj)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err != nil {
		t.Fatalf("expected success when no job_id: %v", err)
	}
}

func TestPollCancelled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"j-cancel"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"cancelled"}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 1},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL:          ts.URL,
			Workflow:            "agentic-agent",
			PollIntervalSeconds: 2,
		},
	}
	rec, _ := buildReconciler(t, obj)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = rec.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	// Reconciler absorbs the error and records it in status
	updated := &benchv1alpha1.RuneBenchmark{}
	if err := rec.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "rb"}, updated); err != nil {
		t.Fatalf("failed to get updated object: %v", err)
	}
	if updated.Status.LastRun.Status != "failed" {
		t.Fatalf("expected failed status, got %s", updated.Status.LastRun.Status)
	}
	if !strings.Contains(updated.Status.LastRun.Error, "cancelled") {
		t.Fatalf("error should mention cancelled: %s", updated.Status.LastRun.Error)
	}
}

// ---------------------------------------------------------------------------
// checkCostEstimate unit tests (direct function calls)
// ---------------------------------------------------------------------------

func TestCheckCostEstimateSkipsWhenVastAIFalse(t *testing.T) {
	spec := benchv1alpha1.RuneBenchmarkSpec{VastAI: false}
	if err := checkCostEstimate(context.Background(), "http://unused", spec, http.DefaultClient, ""); err != nil {
		t.Fatalf("expected nil for non-VastAI, got %v", err)
	}
}

func TestCheckCostEstimateSkipsWhenNoCostProvider(t *testing.T) {
	// No VastAI, no CostEstimation providers → skip
	spec := benchv1alpha1.RuneBenchmarkSpec{VastAI: false}
	if err := checkCostEstimate(context.Background(), "http://unused", spec, http.DefaultClient, ""); err != nil {
		t.Fatalf("expected nil when no providers, got %v", err)
	}
}

func TestCheckCostEstimateFiresForAWS(t *testing.T) {
	var gotPayload map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		_, _ = w.Write([]byte(`{"confidence_score":0.99}`))
	}))
	defer ts.Close()

	spec := benchv1alpha1.RuneBenchmarkSpec{
		CostEstimation: benchv1alpha1.CostEstimation{AWS: true},
		Model:          "llama3",
		TimeoutSeconds: 300,
	}
	if err := checkCostEstimate(context.Background(), ts.URL, spec, http.DefaultClient, ""); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if gotPayload["aws"] != true {
		t.Fatal("expected aws=true in payload")
	}
	if gotPayload["model"] != "llama3" {
		t.Fatalf("expected model=llama3, got %v", gotPayload["model"])
	}
}

func TestCheckCostEstimateFiresForLocalHardware(t *testing.T) {
	var gotPayload map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		_, _ = w.Write([]byte(`{"confidence_score":0.99}`))
	}))
	defer ts.Close()

	spec := benchv1alpha1.RuneBenchmarkSpec{
		CostEstimation: benchv1alpha1.CostEstimation{
			LocalHardware: true,
			LocalTDPWatts: 350,
		},
	}
	if err := checkCostEstimate(context.Background(), ts.URL, spec, http.DefaultClient, ""); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if gotPayload["local_hardware"] != true {
		t.Fatal("expected local_hardware=true")
	}
	if gotPayload["local_tdp_watts"] != 350.0 {
		t.Fatalf("expected local_tdp_watts=350, got %v", gotPayload["local_tdp_watts"])
	}
}

func TestCheckCostEstimateBackwardCompat(t *testing.T) {
	// spec.VastAI=true with empty CostEstimation → gate fires
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"confidence_score":0.99}`))
	}))
	defer ts.Close()

	spec := benchv1alpha1.RuneBenchmarkSpec{VastAI: true}
	if err := checkCostEstimate(context.Background(), ts.URL, spec, http.DefaultClient, ""); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !called {
		t.Fatal("cost gate should fire when VastAI=true even without explicit CostEstimation")
	}
}

func TestCheckCostEstimateMarshalError(t *testing.T) {
	oldMarshal := jsonMarshal
	t.Cleanup(func() { jsonMarshal = oldMarshal })
	jsonMarshal = func(any) ([]byte, error) { return nil, errors.New("marshal-boom") }

	spec := benchv1alpha1.RuneBenchmarkSpec{VastAI: true}
	err := checkCostEstimate(context.Background(), "http://unused", spec, http.DefaultClient, "")
	if err == nil || !strings.Contains(err.Error(), "marshal") {
		t.Fatalf("expected marshal error, got %v", err)
	}
}

func TestCheckCostEstimateBadURL(t *testing.T) {
	spec := benchv1alpha1.RuneBenchmarkSpec{VastAI: true}
	err := checkCostEstimate(context.Background(), "://bad", spec, http.DefaultClient, "")
	if err == nil || !strings.Contains(err.Error(), "build request") {
		t.Fatalf("expected build request error, got %v", err)
	}
}

func TestCheckCostEstimateTransportError(t *testing.T) {
	spec := benchv1alpha1.RuneBenchmarkSpec{VastAI: true}
	err := checkCostEstimate(context.Background(), "http://127.0.0.1:1", spec, &http.Client{Timeout: 100 * time.Millisecond}, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP request failed") {
		t.Fatalf("expected transport error, got %v", err)
	}
}

func TestCheckCostEstimateBodyReadError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, buf, _ := hj.Hijack()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	defer ts.Close()

	spec := benchv1alpha1.RuneBenchmarkSpec{VastAI: true}
	err := checkCostEstimate(context.Background(), ts.URL, spec, http.DefaultClient, "")
	if err == nil || !strings.Contains(err.Error(), "read response") {
		t.Fatalf("expected read response error, got %v", err)
	}
}

func TestCheckCostEstimateSendsAuthHeader(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"confidence_score":0.99}`))
	}))
	defer ts.Close()

	spec := benchv1alpha1.RuneBenchmarkSpec{VastAI: true}
	err := checkCostEstimate(context.Background(), ts.URL, spec, http.DefaultClient, "my-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer my-token" {
		t.Fatalf("expected auth header, got %q", gotAuth)
	}
}

func TestCheckCostEstimateExactThreshold(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"confidence_score":0.95}`))
	}))
	defer ts.Close()

	spec := benchv1alpha1.RuneBenchmarkSpec{VastAI: true}
	err := checkCostEstimate(context.Background(), ts.URL, spec, http.DefaultClient, "")
	if err != nil {
		t.Fatalf("expected 0.95 to pass threshold, got %v", err)
	}
}

func TestCheckBudgetSkipsWhenNilMax(t *testing.T) {
	spec := benchv1alpha1.RuneBenchmarkSpec{Budget: benchv1alpha1.Budget{}}
	if err := checkBudget(context.Background(), "http://unused", spec, http.DefaultClient, ""); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckBudgetAllowsUnderCap(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/finops/simulate" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"projected_cost_usd": 1.5, "cost_high_usd": 2.0, "currency":"USD"}`))
	}))
	defer ts.Close()

	qty := resource.MustParse("10")
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Workflow: "benchmark",
		Model:    "llama",
		Budget:   benchv1alpha1.Budget{MaxCostUSD: &qty},
	}
	if err := checkBudget(context.Background(), ts.URL, spec, http.DefaultClient, ""); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestCheckBudgetExactCapPasses(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"projected_cost_usd": 10.0, "cost_high_usd": 10.0}`))
	}))
	defer ts.Close()

	qty := resource.MustParse("10")
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Budget: benchv1alpha1.Budget{MaxCostUSD: &qty},
	}
	if err := checkBudget(context.Background(), ts.URL, spec, http.DefaultClient, ""); err != nil {
		t.Fatalf("expected success at exact cap, got %v", err)
	}
}

func TestCheckBudgetBlocksOverCap(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"projected_cost_usd": 50.0, "cost_high_usd": 99.0}`))
	}))
	defer ts.Close()

	qty := resource.MustParse("10")
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Budget: benchv1alpha1.Budget{MaxCostUSD: &qty},
	}
	err := checkBudget(context.Background(), ts.URL, spec, http.DefaultClient, "")
	if err == nil || !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
}

func TestCheckBudgetPrefersCostHighOverProjected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mid estimate under cap, but upper bound over cap — must block.
		_, _ = w.Write([]byte(`{"projected_cost_usd": 1.0, "cost_high_usd": 50.0}`))
	}))
	defer ts.Close()

	qty := resource.MustParse("10")
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Budget: benchv1alpha1.Budget{MaxCostUSD: &qty},
	}
	err := checkBudget(context.Background(), ts.URL, spec, http.DefaultClient, "")
	if err == nil || !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded from cost_high_usd, got %v", err)
	}
	if !strings.Contains(err.Error(), "cost_high_usd") {
		t.Fatalf("expected error to name cost_high_usd, got %v", err)
	}
}

func TestCheckBudgetFallbackToProjectedWhenCostHighMissing(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"projected_cost_usd": 99.0}`))
	}))
	defer ts.Close()

	qty := resource.MustParse("10")
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Budget: benchv1alpha1.Budget{MaxCostUSD: &qty},
	}
	err := checkBudget(context.Background(), ts.URL, spec, http.DefaultClient, "")
	if err == nil || !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded from projected_cost_usd fallback, got %v", err)
	}
	if !strings.Contains(err.Error(), "projected_cost_usd") {
		t.Fatalf("expected error to name projected_cost_usd, got %v", err)
	}
}

func TestCheckBudgetNegativeMax(t *testing.T) {
	q, err := resource.ParseQuantity("-1")
	if err != nil {
		t.Fatal(err)
	}
	spec := benchv1alpha1.RuneBenchmarkSpec{Budget: benchv1alpha1.Budget{MaxCostUSD: &q}}
	err = checkBudget(context.Background(), "http://unused", spec, http.DefaultClient, "")
	if err == nil || !strings.Contains(err.Error(), "maxCostUSD must be >= 0") {
		t.Fatalf("expected negative max error, got %v", err)
	}
}

func TestCheckBudgetBadAPIBaseURL(t *testing.T) {
	q := resource.MustParse("10")
	spec := benchv1alpha1.RuneBenchmarkSpec{Budget: benchv1alpha1.Budget{MaxCostUSD: &q}}
	err := checkBudget(context.Background(), "://bad", spec, http.DefaultClient, "")
	if err == nil || !strings.Contains(err.Error(), "invalid API base") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestCheckBudgetTransportError(t *testing.T) {
	q := resource.MustParse("10")
	spec := benchv1alpha1.RuneBenchmarkSpec{Budget: benchv1alpha1.Budget{MaxCostUSD: &q}}
	err := checkBudget(context.Background(), "http://127.0.0.1:1", spec, &http.Client{Timeout: 100 * time.Millisecond}, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP request failed") {
		t.Fatalf("expected transport error, got %v", err)
	}
}

func TestCheckBudgetAPIErrorStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer ts.Close()

	q := resource.MustParse("10")
	spec := benchv1alpha1.RuneBenchmarkSpec{Budget: benchv1alpha1.Budget{MaxCostUSD: &q}}
	err := checkBudget(context.Background(), ts.URL, spec, http.DefaultClient, "")
	if err == nil || !strings.Contains(err.Error(), "API returned 500") {
		t.Fatalf("expected API error, got %v", err)
	}
}

func TestCheckBudgetInvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{`))
	}))
	defer ts.Close()

	q := resource.MustParse("10")
	spec := benchv1alpha1.RuneBenchmarkSpec{Budget: benchv1alpha1.Budget{MaxCostUSD: &q}}
	err := checkBudget(context.Background(), ts.URL, spec, http.DefaultClient, "")
	if err == nil || !strings.Contains(err.Error(), "failed to parse response") {
		t.Fatalf("expected JSON error, got %v", err)
	}
}

func TestCheckBudgetBodyReadError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, buf, _ := hj.Hijack()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	defer ts.Close()

	q := resource.MustParse("10")
	spec := benchv1alpha1.RuneBenchmarkSpec{Budget: benchv1alpha1.Budget{MaxCostUSD: &q}}
	err := checkBudget(context.Background(), ts.URL, spec, http.DefaultClient, "")
	if err == nil || !strings.Contains(err.Error(), "read response") {
		t.Fatalf("expected read error, got %v", err)
	}
}

func TestCheckBudgetSendsTenantAndAuth(t *testing.T) {
	var gotTenant, gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get("X-Tenant-ID")
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"projected_cost_usd": 0.1}`))
	}))
	defer ts.Close()

	q := resource.MustParse("10")
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Tenant: "tenant-z",
		Budget: benchv1alpha1.Budget{MaxCostUSD: &q},
	}
	if err := checkBudget(context.Background(), ts.URL, spec, http.DefaultClient, "tok"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "tenant-z" || gotAuth != "Bearer tok" {
		t.Fatalf("tenant=%q auth=%q", gotTenant, gotAuth)
	}
}

func TestFinopsSimulateQuery(t *testing.T) {
	v := finopsSimulateQuery(benchv1alpha1.RuneBenchmarkSpec{
		Workflow:     "benchmark",
		Model:        "m1",
		TemplateHash: "tpl",
	})
	if v.Get("suite") != "tpl" || v.Get("model") != "m1" {
		t.Fatalf("unexpected query: %v", v)
	}
	v2 := finopsSimulateQuery(benchv1alpha1.RuneBenchmarkSpec{Workflow: "agentic-agent", Agent: "holmes", Model: "m2"})
	if v2.Get("agent") != "holmes" || v2.Get("model") != "m2" {
		t.Fatalf("expected agent and model, got %v", v2)
	}
}
