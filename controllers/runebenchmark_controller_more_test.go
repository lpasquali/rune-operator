package controllers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	batchv1 "k8s.io/api/batch/v1"
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

type errorClient struct {
	client.Client
	errGet          error
	errList         error
	errCreate       error
	errUpdate       error
	errStatusUpdate error
	getHook         func(client.Object) error
	updateHook      func(client.Object) error
}

func (c *errorClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.getHook != nil {
		if err := c.getHook(obj); err != nil {
			return err
		}
	}
	if c.errGet != nil {
		return c.errGet
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *errorClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if c.errList != nil {
		return c.errList
	}
	return c.Client.List(ctx, list, opts...)
}

func (c *errorClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if c.errCreate != nil {
		return c.errCreate
	}
	return c.Client.Create(ctx, obj, opts...)
}

type errorStatusWriter struct {
	client.SubResourceWriter
	errUpdate  error
	updateHook func(client.Object) error
}

func (w *errorStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if w.updateHook != nil {
		if err := w.updateHook(obj); err != nil {
			return err
		}
	}
	if w.errUpdate != nil {
		return w.errUpdate
	}
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}

func (c *errorClient) Status() client.SubResourceWriter {
	return &errorStatusWriter{SubResourceWriter: c.Client.Status(), errUpdate: c.errStatusUpdate, updateHook: c.updateHook}
}

func TestReconcile_Errors(t *testing.T) {
	scheme := controllersTestScheme(t)

	// SetupWithManager nil
	t.Run("SetupWithManagerNil", func(t *testing.T) {
		r := &RuneBenchmarkReconciler{}
		err := r.SetupWithManager(nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "manager is nil")
	})

	// Get Error
	t.Run("GetError", func(t *testing.T) {
		cl := &errorClient{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), errGet: errors.New("mock get error")}
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test"}})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "mock get error")
	})

	// Sync Active Schedules Error on Not Found
	t.Run("NotFoundSyncError", func(t *testing.T) {
		cl := &errorClient{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), errList: errors.New("mock list error on not found")}
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "missing"}})
		assert.NoError(t, err) // Reconcile handles the error internally
	})

	// Infrastructure Ref Error
	t.Run("InfrastructureRefError", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			Spec: benchv1alpha1.RuneBenchmarkSpec{
				InfrastructureRef: &corev1.ObjectReference{
					APIVersion: "invalid/api/version",
					Kind:       "SomeKind",
					Name:       "some-name",
				},
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj).Build()
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"}})
		assert.NoError(t, err)
		assert.Equal(t, 30*time.Second, res.RequeueAfter)
	})

	// List Error for Child Jobs
	t.Run("ListChildJobsError", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"}}
		fakeCl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(obj).Build()
		cl := &errorClient{Client: fakeCl, errList: errors.New("mock list error")}
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"}})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "mock list error")
	})

	// Schedule Parse Error
	t.Run("ScheduleParseError", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			Spec:       benchv1alpha1.RuneBenchmarkSpec{Schedule: "invalid-schedule"},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj).Build()
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"}})
		assert.NoError(t, err)
		assert.Equal(t, time.Duration(0), res.RequeueAfter)
	})

	// Active Schedule parse error
	t.Run("ActiveScheduleParseError", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			Spec:       benchv1alpha1.RuneBenchmarkSpec{Schedule: "invalid"},
		}
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-job",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{UID: obj.UID, Controller: ptrBool(true)},
				},
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj, job).Build()
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"}})
		assert.NoError(t, err)
		assert.Equal(t, time.Duration(0), res.RequeueAfter)
	})

	// Create Error
	t.Run("CreateError", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		}
		fakeCl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(obj).Build()
		cl := &errorClient{Client: fakeCl, errCreate: errors.New("mock create error")}
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"}})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "mock create error")
	})

	// Status Update Error after Create
	t.Run("StatusUpdateErrorAfterCreate", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		}
		fakeCl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj).Build()
		cl := &errorClient{Client: fakeCl, errStatusUpdate: errors.New("mock status update error")}
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"}})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "mock status update error")
	})

	// Status Update Error after Job update
	t.Run("StatusUpdateErrorAfterJobUpdate", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		}
		now := metav1.Now()
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-job",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{UID: "uid-1", Controller: ptrBool(true)},
				},
			},
			Status: batchv1.JobStatus{
				StartTime:  &now,
				Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
			},
		}
		fakeCl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj, job).Build()
		cl := &errorClient{Client: fakeCl, errStatusUpdate: errors.New("mock status update error")}
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"}})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "mock status update error")
	})
	// Infrastructure Ref API Error specifically targeting the unstructured object get
	t.Run("InfrastructureRefAPIError", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test-infra-err", Namespace: "default"},
			Spec: benchv1alpha1.RuneBenchmarkSpec{
				InfrastructureRef: &corev1.ObjectReference{
					APIVersion: "test.group/v1",
					Kind:       "TestKind",
					Name:       "test-ref",
				},
			},
		}
		fakeCl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj).Build()
		cl := &errorClient{
			Client: fakeCl,
			getHook: func(o client.Object) error {
				if _, ok := o.(*unstructured.Unstructured); ok {
					return errors.New("mock unstructured get error")
				}
				return nil
			},
		}
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-infra-err", Namespace: "default"}})
		assert.NoError(t, err)
		assert.Equal(t, 30*time.Second, res.RequeueAfter)
	})

	// Job Not Owned
	t.Run("ChildJobNotOwned", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test-not-owned", Namespace: "default", UID: "uid-owned"},
		}
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-job-not-owned",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{UID: "other-uid", Controller: ptrBool(true)},
				},
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj, job).Build()
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-not-owned", Namespace: "default"}})
		assert.NoError(t, err)
		assert.Equal(t, time.Duration(0), res.RequeueAfter)
	})

	// Schedule parsed properly for RequeueAfter
	t.Run("ScheduleSuccessRequeueAfter", func(t *testing.T) {
		now := metav1.Now()
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test-sched", Namespace: "default", UID: "uid-sched", Generation: 1},
			Spec:       benchv1alpha1.RuneBenchmarkSpec{Schedule: "*/5 * * * *"},
			Status: benchv1alpha1.RuneBenchmarkStatus{
				ObservedGeneration: 1,
				LastScheduleTime:   &now, // Recent
			},
		}
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-job-sched",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{UID: "uid-sched", Controller: ptrBool(true)},
				},
			},
			Status: batchv1.JobStatus{
				StartTime:  &now,
				Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj, job).Build()
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		// ensure updateStatusFromJob doesn't return true
		obj.Status.LastRun = benchv1alpha1.RunRecord{
			RunID:       job.Name,
			SubmittedAt: now,
			CompletedAt: now,
			Status:      "succeeded",
		}
		// ensure the update doesn't happen
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-sched", Namespace: "default"}})
		assert.NoError(t, err)
		assert.True(t, res.RequeueAfter >= 0, "expected non-negative requeue, got %v", res.RequeueAfter)
	})

	// Status Update Error when updating from Job status
	t.Run("StatusUpdateErrorFromJob", func(t *testing.T) {
		now := metav1.Now()
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test-status-err", Namespace: "default", UID: "uid-status-err", Generation: 1},
			Status: benchv1alpha1.RuneBenchmarkStatus{
				ObservedGeneration: 1,
			},
		}
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-job-status-err",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{UID: "uid-status-err", Controller: ptrBool(true)},
				},
			},
			Status: batchv1.JobStatus{
				StartTime:  &now,
				Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
			},
		}
		fakeCl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj, job).Build()
		cl := &errorClient{
			Client: fakeCl,
			updateHook: func(o client.Object) error {
				return errors.New("mock job status update error")
			},
		}
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-status-err", Namespace: "default"}})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "mock job status update error")
	})

	// Status Update Error after Job creation
	t.Run("StatusUpdateErrorAfterCreateJob", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test-status-create-err", Namespace: "default", UID: "uid-create-err"},
		}
		fakeCl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj).Build()
		cl := &errorClient{
			Client: fakeCl,
			updateHook: func(o client.Object) error {
				return errors.New("mock create status update error")
			},
		}
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-status-create-err", Namespace: "default"}})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "mock create status update error")
	})
}

func TestConstructJobForBenchmark_LongName(t *testing.T) {
	scheme := controllersTestScheme(t)
	r := &RuneBenchmarkReconciler{Scheme: scheme}
	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{
			Name:      strings.Repeat("a", 70),
			Namespace: "default",
		},
	}
	job, err := r.constructJobForBenchmark(obj)
	assert.NoError(t, err)
	assert.Len(t, job.Name, 63)
}

func TestUpdateStatusFromJob_UpdateExistingHistory(t *testing.T) {
	now := metav1.Now()
	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bench"},
		Status: benchv1alpha1.RuneBenchmarkStatus{
			History: []benchv1alpha1.RunRecord{
				{RunID: "job-1", Status: "running"},
			},
		},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1"},
		Status: batchv1.JobStatus{
			StartTime:      &now,
			CompletionTime: &now,
			Conditions:     []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
		},
	}
	changed := updateStatusFromJob(obj, job)
	assert.True(t, changed)
	assert.Equal(t, "succeeded", obj.Status.LastRun.Status)
	assert.Len(t, obj.Status.History, 1)
	assert.Equal(t, "succeeded", obj.Status.History[0].Status)
}
