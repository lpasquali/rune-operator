package controllers

import (
	"context"
	"errors"
	"testing"

	benchv1alpha1 "github.com/lpasquali/rune-operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReconcile_AdditionalCoverage(t *testing.T) {
	scheme := controllersTestScheme(t)

	// 1. Coverage for checkInfrastructureRef returning infraErr != nil
	t.Run("CheckInfrastructureRefParseError", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test-infra", Namespace: "default"},
			Spec: benchv1alpha1.RuneBenchmarkSpec{
				InfrastructureRef: &corev1.ObjectReference{
					APIVersion: "invalid/api/version/format/here", // invalid
					Kind:       "SomeKind",
					Name:       "some-name",
				},
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(obj).Build()
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-infra", Namespace: "default"}})
	})

	// 2. Coverage for nextFromCron schedErr == nil inside Reconcile (lines 155-157)
	// We need a valid schedule and no active jobs, and needRun = false, so it falls through to end
	t.Run("ValidScheduleFallsThrough", func(t *testing.T) {
		now := metav1.Now()
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test-valid-sched", Namespace: "default", Generation: 1},
			Spec: benchv1alpha1.RuneBenchmarkSpec{
				Schedule: "* * * * *",
			},
			Status: benchv1alpha1.RuneBenchmarkStatus{
				ObservedGeneration: 1,
				LastScheduleTime:   &now,
			},
		}
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "job-for-sched",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{UID: obj.UID, Controller: ptrBool(true)},
				},
			},
			Status: batchv1.JobStatus{
				StartTime:  &now,
				Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(obj, job).Build()
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-valid-sched", Namespace: "default"}})
	})

	// 3. Coverage for Status Update error after job status changed (lines 181-184)
	t.Run("StatusUpdateErrorFromJobChanged", func(t *testing.T) {
		now := metav1.Now()
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test-status-err", Namespace: "default", Generation: 1},
			Status: benchv1alpha1.RuneBenchmarkStatus{
				ObservedGeneration: 1,
			},
		}
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "job-for-status-err",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{UID: obj.UID, Controller: ptrBool(true)},
				},
			},
			Status: batchv1.JobStatus{
				StartTime:  &now,
				Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
			},
		}

		fakeCl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj, job).Build()
		cl := &errorClient{
			Client:          fakeCl,
			errStatusUpdate: errors.New("mock update error on changed status"),
		}
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-status-err", Namespace: "default"}})
		assert.Error(t, err)
	})

	// 4. Coverage for Create Job error and Status Update error after Job Creation (line 204)
	t.Run("StatusUpdateErrorAfterCreateJob", func(t *testing.T) {
		obj := &benchv1alpha1.RuneBenchmark{
			ObjectMeta: metav1.ObjectMeta{Name: "test-create-job-err", Namespace: "default", Generation: 2},
			Status: benchv1alpha1.RuneBenchmarkStatus{
				ObservedGeneration: 1,
			},
		}

		fakeCl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&benchv1alpha1.RuneBenchmark{}).WithRuntimeObjects(obj).Build()
		cl := &errorClient{
			Client:          fakeCl,
			errStatusUpdate: errors.New("mock update error after create"),
		}
		r := &RuneBenchmarkReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-create-job-err", Namespace: "default"}})
		assert.Error(t, err)
	})
}
