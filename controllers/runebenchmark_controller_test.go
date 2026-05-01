package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	benchv1alpha1 "github.com/lpasquali/rune-operator/api/v1alpha1"
)

func TestRuneBenchmarkReconciler_SetupWithManager(t *testing.T) {
	r := &RuneBenchmarkReconciler{}
	err := r.SetupWithManager(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "manager is nil")
}

func TestUpsertCondition(t *testing.T) {
	conds := []metav1.Condition{}
	c1 := metav1.Condition{Type: "TypeA", Status: "True", Reason: "ReasonA"}
	conds = upsertCondition(conds, c1)
	assert.Len(t, conds, 1)

	c1Updated := metav1.Condition{Type: "TypeA", Status: "False", Reason: "ReasonB"}
	conds = upsertCondition(conds, c1Updated)
	assert.Len(t, conds, 1)
	assert.Equal(t, "False", string(conds[0].Status))

	c2 := metav1.Condition{Type: "TypeB", Status: "True", Reason: "ReasonC"}
	conds = upsertCondition(conds, c2)
	assert.Len(t, conds, 2)
}

func TestNextFromCron(t *testing.T) {
	now := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)
	next, err := nextFromCron("*/5 * * * *", now)
	assert.NoError(t, err)
	assert.Equal(t, time.Date(2023, 1, 1, 12, 5, 0, 0, time.UTC), next)

	_, err = nextFromCron("invalid", now)
	assert.Error(t, err)
}

func TestIsJobFinished(t *testing.T) {
	job := &batchv1.Job{}
	assert.False(t, isJobFinished(job))

	job.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
	}
	assert.True(t, isJobFinished(job))

	job.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
	}
	assert.True(t, isJobFinished(job))

	job.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobComplete, Status: corev1.ConditionFalse},
	}
	assert.False(t, isJobFinished(job))
}

func TestUpdateStatusFromJob(t *testing.T) {
	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bench"},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bench-123"},
	}

	// No start time
	changed := updateStatusFromJob(obj, job)
	assert.False(t, changed)

	// Running
	now := metav1.Now()
	job.Status.StartTime = &now
	changed = updateStatusFromJob(obj, job)
	assert.True(t, changed)
	assert.Equal(t, "running", obj.Status.LastRun.Status)

	// No change
	changed = updateStatusFromJob(obj, job)
	assert.False(t, changed)

	// Completed
	job.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
	}
	job.Status.CompletionTime = &now
	changed = updateStatusFromJob(obj, job)
	assert.True(t, changed)
	assert.Equal(t, "succeeded", obj.Status.LastRun.Status)
	assert.Len(t, obj.Status.History, 1)

	// Completed again (history size limit not hit)
	job2 := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bench-124"},
		Status: batchv1.JobStatus{
			StartTime:      &now,
			CompletionTime: &now,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			},
		},
	}
	changed = updateStatusFromJob(obj, job2)
	assert.True(t, changed)
	assert.Equal(t, "failed", obj.Status.LastRun.Status)
	assert.Len(t, obj.Status.History, 2)
}

func TestConstructJobForBenchmark(t *testing.T) {
	scheme := controllersTestScheme(t)
	r := &RuneBenchmarkReconciler{Scheme: scheme}

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-bench",
			Namespace: "default",
			UID:       "uid-123",
		},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			Workflow:          "test-workflow",
			APIBaseURL:        "http://api",
			Tenant:            "test-tenant",
			Model:             "test-model",
			BackendURL:        "http://backend",
			BackendType:       "test-backend",
			Region:            "test-region",
			APITokenSecretRef: "test-secret/token-key",
		},
	}

	job, err := r.constructJobForBenchmark(obj)
	require.NoError(t, err)
	assert.Contains(t, job.Name, "test-bench-")
	assert.Equal(t, "default", job.Namespace)
	assert.Len(t, job.Spec.Template.Spec.Containers, 1)

	container := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "benchmark", container.Name)
	assert.Equal(t, []string{"rune", "execute", "test-workflow"}, container.Command)

	// Test Env vars
	envMap := make(map[string]string)
	for _, env := range container.Env {
		if env.ValueFrom != nil {
			envMap[env.Name] = env.ValueFrom.SecretKeyRef.LocalObjectReference.Name
		} else {
			envMap[env.Name] = env.Value
		}
	}
	assert.Equal(t, "http://api", envMap["RUNE_API_BASE_URL"])
	assert.Equal(t, "token-key", envMap["RUNE_API_TOKEN"])

	// Test fallback secret name
	obj.Spec.APITokenSecretRef = "test-secret"
	job, err = r.constructJobForBenchmark(obj)
	require.NoError(t, err)
	envMap2 := make(map[string]string)
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		if env.ValueFrom != nil {
			envMap2[env.Name] = env.ValueFrom.SecretKeyRef.LocalObjectReference.Name
		}
	}
	assert.Equal(t, "test-secret", envMap2["RUNE_API_TOKEN"])
}

func TestInfrastructureConditions(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{"type": "Synced", "status": "True"},
					map[string]interface{}{"type": "Ready", "status": "True"},
				},
			},
		},
	}
	synced, ready := infrastructureConditions(u)
	assert.True(t, synced)
	assert.True(t, ready)

	u2 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{"type": "Synced", "status": "False"},
					map[string]interface{}{"type": "Ready", "status": "False"},
				},
			},
		},
	}
	synced, ready = infrastructureConditions(u2)
	assert.False(t, synced)
	assert.False(t, ready)
}

func TestCheckInfrastructureRef(t *testing.T) {
	scheme := controllersTestScheme(t)

	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "test.group/v1",
			"kind":       "TestKind",
			"metadata": map[string]interface{}{
				"name":      "test-ref",
				"namespace": "default",
			},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{"type": "Synced", "status": "True"},
					map[string]interface{}{"type": "Ready", "status": "True"},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(u).Build()
	r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}

	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			InfrastructureRef: &corev1.ObjectReference{
				APIVersion: "test.group/v1",
				Kind:       "TestKind",
				Name:       "test-ref",
			},
		},
	}

	ready, reason, err := r.checkInfrastructureRef(context.Background(), obj)
	assert.NoError(t, err)
	assert.True(t, ready)
	assert.Empty(t, reason)

	// Missing fields
	obj.Spec.InfrastructureRef.Name = ""
	ready, reason, err = r.checkInfrastructureRef(context.Background(), obj)
	assert.NoError(t, err)
	assert.False(t, ready)
	assert.Contains(t, reason, "infrastructureRef must set apiVersion, kind, and name")

	// Invalid GroupVersion
	obj.Spec.InfrastructureRef.Name = "test-ref"
	obj.Spec.InfrastructureRef.APIVersion = "invalid/group/version"
	ready, reason, err = r.checkInfrastructureRef(context.Background(), obj)
	assert.Error(t, err)
	assert.False(t, ready)

	// Not Found
	obj.Spec.InfrastructureRef.APIVersion = "test.group/v1"
	obj.Spec.InfrastructureRef.Name = "non-existent"
	ready, reason, err = r.checkInfrastructureRef(context.Background(), obj)
	assert.Error(t, err)
	assert.False(t, ready)
	assert.Contains(t, reason, "cannot fetch infrastructureRef")

	// Not Ready
	u2 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "test.group/v1",
			"kind":       "TestKind",
			"metadata": map[string]interface{}{
				"name":      "not-ready-ref",
				"namespace": "default",
			},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{"type": "Synced", "status": "True"},
					map[string]interface{}{"type": "Ready", "status": "False"},
				},
			},
		},
	}
	cl = fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(u, u2).Build()
	r.Client = cl
	obj.Spec.InfrastructureRef.Name = "not-ready-ref"
	ready, reason, err = r.checkInfrastructureRef(context.Background(), obj)
	assert.NoError(t, err)
	assert.False(t, ready)
	assert.Contains(t, reason, "not ready")
}

func TestUpdateStatusFromJob_NoCompletionTime(t *testing.T) {
	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bench"},
		Status: benchv1alpha1.RuneBenchmarkStatus{
			History: []benchv1alpha1.RunRecord{
				{RunID: "old-1"}, {RunID: "old-2"}, {RunID: "old-3"}, {RunID: "old-4"}, {RunID: "old-5"},
				{RunID: "old-6"}, {RunID: "old-7"}, {RunID: "old-8"}, {RunID: "old-9"}, {RunID: "old-10"},
				{RunID: "old-11"}, {RunID: "old-12"}, {RunID: "old-13"}, {RunID: "old-14"}, {RunID: "old-15"},
				{RunID: "old-16"}, {RunID: "old-17"}, {RunID: "old-18"}, {RunID: "old-19"}, {RunID: "old-20"},
				{RunID: "old-21"}, // This should be truncated
			},
		},
	}
	now := metav1.Now()
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bench-nil-completion"},
		Status: batchv1.JobStatus{
			StartTime: &now,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}
	changed := updateStatusFromJob(obj, job)
	assert.True(t, changed)
	assert.Equal(t, "succeeded", obj.Status.LastRun.Status)
	assert.Len(t, obj.Status.History, 20) // Should truncate to 20
}

func TestConstructJobForBenchmark_Error(t *testing.T) {
	// Scheme is nil, should fail SetControllerReference
	r := &RuneBenchmarkReconciler{Scheme: runtime.NewScheme()}
	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bench"},
	}
	_, err := r.constructJobForBenchmark(obj)
	assert.Error(t, err)
}

func TestSyncActiveSchedules(t *testing.T) {
	scheme := controllersTestScheme(t)
	obj1 := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "bench-1", Namespace: "default"},
		Spec:       benchv1alpha1.RuneBenchmarkSpec{Suspend: false},
	}
	obj2 := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "bench-2", Namespace: "default"},
		Spec:       benchv1alpha1.RuneBenchmarkSpec{Suspend: true},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(obj1, obj2).Build()
	r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
	err := r.syncActiveSchedules(context.Background())
	assert.NoError(t, err)
}

func TestInfrastructureConditions_EdgeCases(t *testing.T) {
	// Not a slice
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"status": map[string]interface{}{
				"conditions": "not a slice",
			},
		},
	}
	synced, ready := infrastructureConditions(u)
	assert.False(t, synced)
	assert.False(t, ready)

	// Items not map[string]interface{}
	u2 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"status": map[string]interface{}{
				"conditions": []interface{}{
					"not a map",
				},
			},
		},
	}
	synced, ready = infrastructureConditions(u2)
	assert.False(t, synced)
	assert.False(t, ready)
}

func TestReconcile(t *testing.T) {
	scheme := controllersTestScheme(t)

	t.Run("NotFound", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "non-existent"}})
		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
	})

	t.Run("PanicRecovery", func(t *testing.T) {
		// nil client will panic
		r := &RuneBenchmarkReconciler{Client: nil, Scheme: scheme}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "non-existent"}})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "panic recovered")
	})

	t.Run("Suspended", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "bench-suspended", Namespace: "default"},
			Spec:       benchv1alpha1.RuneBenchmarkSpec{Suspend: true},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(obj).Build()
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "bench-suspended", Namespace: "default"}})
		assert.NoError(t, err)
		assert.Equal(t, time.Minute*5, res.RequeueAfter)
	})

	t.Run("InfrastructureNotReady", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "bench-infra", Namespace: "default"},
			Spec: benchv1alpha1.RuneBenchmarkSpec{
				InfrastructureRef: &corev1.ObjectReference{
					APIVersion: "test.group/v1",
					Kind:       "TestKind",
					Name:       "test-ref",
				},
			},
		}
		// Ref doesn't exist, so checkInfrastructureRef will return error
		cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(obj).Build()
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "bench-infra", Namespace: "default"}})
		assert.NoError(t, err)
		assert.Equal(t, time.Second*30, res.RequeueAfter)
	})

	t.Run("ActiveJobs", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "bench-active", Namespace: "default", UID: "uid-1"},
		}
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "job-1",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{UID: "uid-1", Controller: ptrBool(true)},
				},
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(obj, job).Build()
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "bench-active", Namespace: "default"}})
		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res) // returns early because active jobs > 0
	})

	t.Run("NeedRun_NoLatestJob", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "bench-run", Namespace: "default", UID: "uid-1"},
			Spec:       benchv1alpha1.RuneBenchmarkSpec{Schedule: "*/1 * * * *"},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj).Build()
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "bench-run", Namespace: "default"}})
		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)

		// Job should be created
		var jobs batchv1.JobList
		err = cl.List(context.Background(), &jobs)
		assert.NoError(t, err)
		assert.Len(t, jobs.Items, 1)
	})

	t.Run("LatestJob_Finished_NeedRun_Schedule", func(t *testing.T) {
		now := metav1.Now()
		past := metav1.NewTime(now.Add(-2 * time.Minute))

		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "bench-run-sched", Namespace: "default", UID: "uid-1"},
			Spec:       benchv1alpha1.RuneBenchmarkSpec{Schedule: "*/1 * * * *"},
			Status: benchv1alpha1.RuneBenchmarkStatus{
				LastScheduleTime:   &past,
				ObservedGeneration: 1, // match generation
			},
			// setting generation
		}
		obj.Generation = 1

		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "job-1",
				Namespace:         "default",
				CreationTimestamp: past,
				OwnerReferences: []metav1.OwnerReference{
					{UID: "uid-1", Controller: ptrBool(true)},
				},
			},
			Status: batchv1.JobStatus{
				StartTime:      &past,
				CompletionTime: &past,
				Conditions:     []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj, job).Build()
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "bench-run-sched", Namespace: "default"}})
		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
	})
}

func ptrBool(b bool) *bool { return &b }
