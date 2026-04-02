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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"job-123"}`))
	}))
	defer ts.Close()

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns", Generation: 2},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: ts.URL,
			Workflow:   "wf",
			Question:   "q",
			Model:      "m",
		},
	}
	r, _ := buildReconciler(t, obj)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
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

func TestReconcileSuccessWithScheduleAndHistoryTrim(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"job-abc"}`))
	}))
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"job-xyz"}`))
	}))
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"job-sync"}`))
	}))
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"job-ok"}`))
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
	r.Client = failingStatusClient{Client: r.Client, err: context.DeadlineExceeded}
	r.Scheme = s

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rb"}})
	if err == nil {
		t.Fatalf("expected status update error on success path")
	}
}

func TestExecuteBenchmarkAndReadTokenBranches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	obj := &benchv1alpha1.RuneBenchmark{Spec: benchv1alpha1.RuneBenchmarkSpec{
		APIBaseURL:        server.URL,
		Workflow:          "wf",
		Tenant:            "tenant-a",
		APITokenSecretRef: "ns/sec",
	}}

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
		if r.URL.Path == "/v1/jobs" {
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

func TestExecuteBenchmarkHTTPTransportError(t *testing.T) {
	r, _ := buildReconciler(t)
	obj := &benchv1alpha1.RuneBenchmark{Spec: benchv1alpha1.RuneBenchmarkSpec{APIBaseURL: "http://127.0.0.1:1", Workflow: "wf"}}

	if _, err := r.executeBenchmark(context.Background(), obj, 150*time.Millisecond); err == nil {
		t.Fatalf("expected transport error")
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
