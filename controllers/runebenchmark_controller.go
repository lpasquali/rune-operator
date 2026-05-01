// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	benchv1alpha1 "github.com/lpasquali/rune-operator/api/v1alpha1"
	"github.com/lpasquali/rune-operator/internal/metrics"
)

type RuneBenchmarkReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=bench.rune.ai,resources=runebenchmarks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bench.rune.ai,resources=runebenchmarks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bench.rune.ai,resources=runebenchmarks/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch;update
// +kubebuilder:rbac:groups=database.infra.rune.ai,resources=runedatabases;xrunedatabases,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage.infra.rune.ai,resources=runeobjectstores;xruneobjectstores,verbs=get;list;watch

func (r *RuneBenchmarkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if mgr == nil {
		return fmt.Errorf("manager is nil")
	}
	r.Recorder = mgr.GetEventRecorderFor("rune-benchmark-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&benchv1alpha1.RuneBenchmark{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}

func (r *RuneBenchmarkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, retErr error) {
	tracer := otel.Tracer("rune-operator/reconciler")
	ctx, span := tracer.Start(ctx, "RuneBenchmarkReconcile")
	defer span.End()

	defer func() {
		if recovered := recover(); recovered != nil {
			stack := string(debug.Stack())
			retErr = fmt.Errorf("panic recovered: %v", recovered)
			log.FromContext(ctx).Error(retErr, "panic in reconcile", "stack", stack)
			metrics.ReconcileTotal.WithLabelValues("panic").Inc()
		}
	}()

	logger := log.FromContext(ctx)
	obj := &benchv1alpha1.RuneBenchmark{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		if apierrors.IsNotFound(err) {
			metrics.ReconcileTotal.WithLabelValues("not_found").Inc()
			if syncErr := r.syncActiveSchedules(ctx); syncErr != nil {
				logger.Error(syncErr, "failed to refresh active schedule metric")
			}
			return ctrl.Result{}, nil
		}
		metrics.ReconcileTotal.WithLabelValues("get_error").Inc()
		return ctrl.Result{}, err
	}
	if err := r.syncActiveSchedules(ctx); err != nil {
		logger.Error(err, "failed to refresh active schedule metric")
	}

	span.SetAttributes(
		attribute.String("resource.name", obj.Name),
		attribute.String("resource.namespace", obj.Namespace),
		attribute.String("workflow", obj.Spec.Workflow),
	)

	if obj.Spec.Suspend {
		metrics.ReconcileTotal.WithLabelValues("suspended").Inc()
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	if ready, reason, infraErr := r.checkInfrastructureRef(ctx, obj); infraErr != nil {
		metrics.ReconcileTotal.WithLabelValues("infra_get_error").Inc()
		r.Recorder.Event(obj, "Warning", "InfrastructureNotReady", reason)
		logger.Error(infraErr, "infrastructureRef lookup failed", "reason", reason)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	} else if !ready {
		metrics.ReconcileTotal.WithLabelValues("infra_not_ready").Inc()
		r.Recorder.Event(obj, "Warning", "InfrastructureNotReady", reason)
		logger.Info("infrastructureRef not ready; requeuing", "reason", reason)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	now := metav1.Now()

	// Find child jobs
	var childJobs batchv1.JobList
	if err := r.List(ctx, &childJobs, client.InNamespace(req.Namespace)); err != nil {
		logger.Error(err, "unable to list child Jobs")
		return ctrl.Result{}, err
	}

	var activeJobs []*batchv1.Job
	var latestJob *batchv1.Job
	for i := range childJobs.Items {
		job := &childJobs.Items[i]
		// verify owner ref
		isOwner := false
		for _, ref := range job.OwnerReferences {
			if ref.Controller != nil && *ref.Controller && ref.UID == obj.UID {
				isOwner = true
				break
			}
		}
		if !isOwner {
			continue
		}
		if latestJob == nil || job.CreationTimestamp.After(latestJob.CreationTimestamp.Time) {
			latestJob = job
		}
		if !isJobFinished(job) {
			activeJobs = append(activeJobs, job)
		}
	}

	if len(activeJobs) > 0 {
		return ctrl.Result{}, nil
	}

	requeueAfter := 10 * time.Minute
	if strings.TrimSpace(obj.Spec.Schedule) != "" {
		next, schedErr := nextFromCron(obj.Spec.Schedule, now.Time)
		if schedErr == nil {
			requeueAfter = time.Until(next)
		}
	}

	needRun := false
	if obj.Status.ObservedGeneration != obj.Generation {
		needRun = true
	} else if latestJob != nil && strings.TrimSpace(obj.Spec.Schedule) != "" {
		if obj.Status.LastScheduleTime != nil {
			nextRun, err := nextFromCron(obj.Spec.Schedule, obj.Status.LastScheduleTime.Time)
			if err == nil && now.Time.After(nextRun) {
				needRun = true
			}
		}
	} else if latestJob == nil {
		needRun = true
	}

	if latestJob != nil {
		statusChanged := updateStatusFromJob(obj, latestJob)
		if statusChanged {
			if err := r.Status().Update(ctx, obj); err != nil {
				logger.Error(err, "unable to update RuneBenchmark status")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
	}

	if needRun {
		job, err := r.constructJobForBenchmark(obj)
		if err != nil {
			logger.Error(err, "unable to construct job")
			return ctrl.Result{}, err
		}

		if err := r.Create(ctx, job); err != nil {
			logger.Error(err, "unable to create Job for RuneBenchmark", "job", job.Name)
			metrics.ReconcileTotal.WithLabelValues("error").Inc()
			return ctrl.Result{}, err
		}

		metrics.ReconcileTotal.WithLabelValues("job_created").Inc()

		obj.Status.ObservedGeneration = obj.Generation
		obj.Status.LastScheduleTime = &now
		if err := r.Status().Update(ctx, obj); err != nil {
			logger.Error(err, "unable to update RuneBenchmark status")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func (r *RuneBenchmarkReconciler) constructJobForBenchmark(obj *benchv1alpha1.RuneBenchmark) (*batchv1.Job, error) {
	name := fmt.Sprintf("%s-%d", obj.Name, time.Now().Unix())
	if len(name) > 63 {
		name = name[:63]
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: obj.Namespace,
			Labels: map[string]string{
				"bench.rune.ai/benchmark": obj.Name,
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"bench.rune.ai/benchmark": obj.Name,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "benchmark",
							Image:   "ghcr.io/lpasquali/rune:latest",
							Command: []string{"rune", "execute", obj.Spec.Workflow},
							Env: []corev1.EnvVar{
								{Name: "RUNE_API_BASE_URL", Value: obj.Spec.APIBaseURL},
								{Name: "RUNE_TENANT", Value: obj.Spec.Tenant},
								{Name: "RUNE_MODEL", Value: obj.Spec.Model},
								{Name: "RUNE_BACKEND_URL", Value: obj.Spec.BackendURL},
								{Name: "RUNE_BACKEND_TYPE", Value: obj.Spec.BackendType},
								{Name: "RUNE_REGION", Value: obj.Spec.Region},
							},
						},
					},
				},
			},
		},
	}

	if obj.Spec.APITokenSecretRef != "" {
		parts := strings.Split(obj.Spec.APITokenSecretRef, "/")
		secretName := parts[0]
		if len(parts) == 2 {
			secretName = parts[1]
		}
		job.Spec.Template.Spec.Containers[0].Env = append(job.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{
			Name: "RUNE_API_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secretName,
					},
					Key: "token",
				},
			},
		})
	}

	if err := ctrl.SetControllerReference(obj, job, r.Scheme); err != nil {
		return nil, err
	}
	return job, nil
}

func isJobFinished(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func updateStatusFromJob(obj *benchv1alpha1.RuneBenchmark, job *batchv1.Job) bool {
	changed := false

	if job.Status.StartTime == nil {
		return changed
	}

	finished := isJobFinished(job)
	var conditionType batchv1.JobConditionType
	for _, c := range job.Status.Conditions {
		if c.Status == corev1.ConditionTrue {
			conditionType = c.Type
			break
		}
	}

	runRecord := benchv1alpha1.RunRecord{
		RunID:       job.Name,
		SubmittedAt: *job.Status.StartTime,
	}

	if finished {
		if job.Status.CompletionTime != nil {
			runRecord.CompletedAt = *job.Status.CompletionTime
			runRecord.DurationMillis = job.Status.CompletionTime.Sub(job.Status.StartTime.Time).Milliseconds()
		} else {
			now := metav1.Now()
			runRecord.CompletedAt = now
			runRecord.DurationMillis = now.Sub(job.Status.StartTime.Time).Milliseconds()
		}

		if conditionType == batchv1.JobComplete {
			runRecord.Status = "succeeded"
		} else {
			runRecord.Status = "failed"
			runRecord.Error = "Job failed"
		}
	} else {
		runRecord.Status = "running"
	}

	if obj.Status.LastRun.RunID != runRecord.RunID || obj.Status.LastRun.Status != runRecord.Status {
		obj.Status.LastRun = runRecord

		if finished {
			if len(obj.Status.History) > 0 && obj.Status.History[0].RunID == runRecord.RunID {
				obj.Status.History[0] = runRecord
			} else {
				obj.Status.History = append([]benchv1alpha1.RunRecord{runRecord}, obj.Status.History...)
				if len(obj.Status.History) > 20 {
					obj.Status.History = obj.Status.History[:20]
				}
			}

			if conditionType == batchv1.JobFailed {
				obj.Status.ConsecutiveFailures++
				obj.Status.Conditions = upsertCondition(obj.Status.Conditions, benchv1alpha1.ConditionReady(metav1.ConditionFalse, "RunFailed", "Job failed", obj.Generation))
			} else if conditionType == batchv1.JobComplete {
				obj.Status.ConsecutiveFailures = 0
				now := metav1.Now()
				obj.Status.LastSuccessfulTime = &now
				obj.Status.Conditions = upsertCondition(obj.Status.Conditions, benchv1alpha1.ConditionReady(metav1.ConditionTrue, "RunSucceeded", "workflow run succeeded", obj.Generation))
			}
		}
		changed = true
	}

	return changed
}

func (r *RuneBenchmarkReconciler) syncActiveSchedules(ctx context.Context) error {
	var list benchv1alpha1.RuneBenchmarkList
	if err := r.List(ctx, &list); err != nil {
		return err
	}
	active := 0
	for _, item := range list.Items {
		if !item.Spec.Suspend {
			active++
		}
	}
	metrics.ActiveSchedules.Set(float64(active))
	return nil
}

func upsertCondition(conditions []metav1.Condition, cond metav1.Condition) []metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == cond.Type {
			conditions[i] = cond
			return conditions
		}
	}
	return append(conditions, cond)
}

func nextFromCron(spec string, from time.Time) (time.Time, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(spec)
	if err != nil {
		return time.Time{}, err
	}
	return schedule.Next(from), nil
}

func (r *RuneBenchmarkReconciler) checkInfrastructureRef(ctx context.Context, obj *benchv1alpha1.RuneBenchmark) (bool, string, error) {
	ref := obj.Spec.InfrastructureRef
	if ref == nil {
		return true, "", nil
	}
	if ref.APIVersion == "" || ref.Kind == "" || ref.Name == "" {
		return false, "infrastructureRef must set apiVersion, kind, and name", nil
	}
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return false, fmt.Sprintf("invalid infrastructureRef apiVersion %q: %v", ref.APIVersion, err), err
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gv.WithKind(ref.Kind))
	ns := ref.Namespace
	if ns == "" {
		ns = obj.Namespace
	}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, u); err != nil {
		return false, fmt.Sprintf("cannot fetch infrastructureRef %s/%s: %v", ref.Kind, ref.Name, err), err
	}
	synced, ready := infrastructureConditions(u)
	if !synced || !ready {
		return false, fmt.Sprintf(
			"infrastructureRef %s %s/%s not ready (Synced=%v Ready=%v)",
			ref.Kind, ns, ref.Name, synced, ready,
		), nil
	}
	return true, "", nil
}

func infrastructureConditions(u *unstructured.Unstructured) (bool, bool) {
	conds, found, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err != nil || !found {
		return false, false
	}
	synced, ready := false, false
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		s, _ := m["status"].(string)
		switch t {
		case "Synced":
			synced = s == "True"
		case "Ready":
			ready = s == "True"
		}
	}
	return synced, ready
}
