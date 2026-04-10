// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

var setupControllerWithManager = func(mgr ctrl.Manager, r *RuneBenchmarkReconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&benchv1alpha1.RuneBenchmark{}).
		Complete(r)
}

var jsonMarshal = json.Marshal

// +kubebuilder:rbac:groups=bench.rune.ai,resources=runebenchmarks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bench.rune.ai,resources=runebenchmarks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bench.rune.ai,resources=runebenchmarks/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch;update

func (r *RuneBenchmarkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if mgr == nil {
		return fmt.Errorf("manager is nil")
	}
	r.Recorder = mgr.GetEventRecorderFor("rune-benchmark-controller")
	return setupControllerWithManager(mgr, r)
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

	now := metav1.Now()
	obj.Status.ObservedGeneration = obj.Generation
	obj.Status.LastScheduleTime = &now

	timeout := time.Duration(maxInt32(obj.Spec.TimeoutSeconds, 120)) * time.Second
	start := time.Now()
	run, err := r.executeBenchmark(ctx, obj, timeout)
	duration := time.Since(start)

	run.DurationMillis = duration.Milliseconds()
	run.CompletedAt = metav1.Now()
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
	}
	obj.Status.LastRun = run
	obj.Status.History = append([]benchv1alpha1.RunRecord{run}, obj.Status.History...)
	if len(obj.Status.History) > 20 {
		obj.Status.History = obj.Status.History[:20]
	}

	if err != nil {
		obj.Status.ConsecutiveFailures++
		failReason := "RunFailed"
		if errors.Is(err, ErrBudgetExceeded) {
			failReason = "BudgetExceeded"
		}
		obj.Status.Conditions = upsertCondition(obj.Status.Conditions, benchv1alpha1.ConditionReady(metav1.ConditionFalse, failReason, err.Error(), obj.Generation))
		r.Recorder.Eventf(obj, "Warning", failReason, "workflow run failed: %v", err)
		logger.Error(err, "run failed")
		metrics.ReconcileTotal.WithLabelValues("error").Inc()
		if statusErr := r.Status().Update(ctx, obj); statusErr != nil {
			logger.Error(statusErr, "failed to update RuneBenchmark status after run failure")
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: time.Duration(maxInt32(obj.Spec.BackoffSeconds, 60)) * time.Second}, nil
	}

	obj.Status.ConsecutiveFailures = 0
	successTime := metav1.Now()
	obj.Status.LastSuccessfulTime = &successTime
	obj.Status.Conditions = upsertCondition(obj.Status.Conditions, benchv1alpha1.ConditionReady(metav1.ConditionTrue, "RunSucceeded", "workflow run succeeded", obj.Generation))
	r.Recorder.Event(obj, "Normal", "RunSucceeded", "workflow run succeeded")
	metrics.ReconcileTotal.WithLabelValues("success").Inc()
	metrics.RunDurationMillis.Observe(float64(duration.Milliseconds()))

	if err := r.Status().Update(ctx, obj); err != nil {
		return ctrl.Result{}, err
	}

	requeueAfter := 10 * time.Minute
	if strings.TrimSpace(obj.Spec.Schedule) != "" {
		next, schedErr := nextFromCron(obj.Spec.Schedule, now.Time)
		if schedErr == nil {
			requeueAfter = time.Until(next)
		}
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func buildPayload(spec benchv1alpha1.RuneBenchmarkSpec) map[string]any {
	switch spec.Workflow {
	case "agentic-agent":
		p := map[string]any{
			"question":               spec.Question,
			"model":                  spec.Model,
			"backend_url":            spec.BackendURL,
			"backend_type":           spec.BackendType,
			"backend_warmup":         spec.BackendWarmup,
			"backend_warmup_timeout": int(spec.BackendWarmupTimeoutSeconds),
			"kubeconfig":             spec.Kubeconfig,
		}
		if spec.Agent != "" {
			p["agent"] = spec.Agent
		}
		return p
	case "ollama-instance", "llm-instance":
		return map[string]any{
			"vastai":        spec.VastAI,
			"template_hash": spec.TemplateHash,
			"min_dph":       spec.MinDPH,
			"max_dph":       spec.MaxDPH,
			"reliability":   spec.Reliability,
			"backend_url":   spec.BackendURL,
			"backend_type":  spec.BackendType,
		}
	case "benchmark":
		return map[string]any{
			"vastai":                 spec.VastAI,
			"template_hash":          spec.TemplateHash,
			"min_dph":                spec.MinDPH,
			"max_dph":                spec.MaxDPH,
			"reliability":            spec.Reliability,
			"backend_url":            spec.BackendURL,
			"backend_type":           spec.BackendType,
			"question":               spec.Question,
			"model":                  spec.Model,
			"backend_warmup":         spec.BackendWarmup,
			"backend_warmup_timeout": int(spec.BackendWarmupTimeoutSeconds),
			"kubeconfig":             spec.Kubeconfig,
			"vastai_stop_instance":   spec.VastAIStopInstance,
			"attestation_required":   spec.AttestationRequired,
		}
	default:
		// Unknown workflow kind — forward what we have; the API server will reject with a clear error.
		return map[string]any{
			"workflow":     spec.Workflow,
			"question":     spec.Question,
			"model":        spec.Model,
			"backend_url":  spec.BackendURL,
			"backend_type": spec.BackendType,
		}
	}
}

// estimateConfidenceThreshold is the minimum confidence score required from the
// cost estimation endpoint before a VastAI job may proceed.
const estimateConfidenceThreshold = 0.95

// defaultPollInterval is the default interval between job status polls.
const defaultPollInterval = 5

// estimateResponse is the expected JSON structure from POST /v1/estimates.
type estimateResponse struct {
	ConfidenceScore float64 `json:"confidence_score"`
}

// jobStatusResponse is the expected JSON from GET /v1/jobs/{job_id}.
type jobStatusResponse struct {
	Status  string          `json:"status"`
	Error   string          `json:"error,omitempty"`
	Message string          `json:"message,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

// getJobStatus polls the Rune API for the actual status of a submitted job.
func getJobStatus(ctx context.Context, apiBase string, jobID string, tenant string, httpClient *http.Client, token string) (*jobStatusResponse, error) {
	url := strings.TrimRight(apiBase, "/") + "/v1/jobs/" + jobID

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if tenant != "" {
		req.Header.Set("X-Tenant-ID", tenant)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result jobStatusResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return &result, nil
}

// maxInt32 returns a if positive, otherwise returns fallback b.
func maxInt32(a, b int32) int32 {
	if a > 0 {
		return a
	}
	return b
}

// checkCostEstimate performs a fail-closed pre-flight cost estimation gate.
// If any cost provider is enabled (VastAI, AWS, GCP, Azure, LocalHardware),
// it calls the /v1/estimates endpoint and verifies confidence >= 0.95.
// For backward compatibility, if no explicit cost estimation is configured but
// spec.VastAI is true, the gate fires automatically.
func checkCostEstimate(ctx context.Context, apiBase string, spec benchv1alpha1.RuneBenchmarkSpec, httpClient *http.Client, token string) error {
	ce := spec.CostEstimation

	// Backward compat: if no explicit costEstimation but vastai provisioning is on, gate it
	if !ce.VastAI && !ce.AWS && !ce.GCP && !ce.Azure && !ce.LocalHardware {
		if spec.VastAI {
			ce.VastAI = true
		} else {
			return nil
		}
	}

	estimatePayload := map[string]any{
		// Provider flags
		"vastai":         ce.VastAI,
		"aws":            ce.AWS,
		"gcp":            ce.GCP,
		"azure":          ce.Azure,
		"local_hardware": ce.LocalHardware,
		// Price bounds
		"template_hash": spec.TemplateHash,
		"min_dph":       spec.MinDPH,
		"max_dph":       spec.MaxDPH,
		"reliability":   spec.Reliability,
		// Local hardware params
		"local_tdp_watts":               ce.LocalTDPWatts,
		"local_energy_rate_kwh":         ce.LocalEnergyRateKWH,
		"local_hardware_purchase_price": ce.LocalHardwarePurchasePrice,
		"local_hardware_lifespan_years": ce.LocalHardwareLifespanYears,
		// Run context
		"model":                      spec.Model,
		"estimated_duration_seconds": int(spec.TimeoutSeconds),
	}
	body, err := jsonMarshal(estimatePayload)
	if err != nil {
		return fmt.Errorf("cost estimate: failed to marshal request: %w", err)
	}

	url := strings.TrimRight(apiBase, "/") + "/v1/estimates"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cost estimate: failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("cost estimate: HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("cost estimate: failed to read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("cost estimate: API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed estimateResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return fmt.Errorf("cost estimate: failed to parse response: %w", err)
	}
	if parsed.ConfidenceScore < estimateConfidenceThreshold {
		return fmt.Errorf("cost estimate: confidence %.2f below threshold %.2f", parsed.ConfidenceScore, estimateConfidenceThreshold)
	}

	return nil
}

// ErrBudgetExceeded is returned when the finops estimate (cost_high_usd when present, else projected_cost_usd) exceeds spec.budget.maxCostUSD.
var ErrBudgetExceeded = errors.New("budget exceeded")

// finopsSimulateResponse is the JSON body from GET /v1/finops/simulate (see rune/rune_bench/metrics/pricing.py).
type finopsSimulateResponse struct {
	ProjectedCostUSD float64  `json:"projected_cost_usd"`
	CostHighUSD      *float64 `json:"cost_high_usd,omitempty"`
}

// effectiveBudgetUSD returns the USD amount to compare against maxCostUSD: prefer cost_high_usd
// (stricter upper bound) when present, else projected_cost_usd.
func effectiveBudgetUSD(p finopsSimulateResponse) (value float64, basis string) {
	if p.CostHighUSD != nil {
		return *p.CostHighUSD, "cost_high_usd"
	}
	return p.ProjectedCostUSD, "projected_cost_usd"
}

func finopsSimulateQuery(spec benchv1alpha1.RuneBenchmarkSpec) url.Values {
	v := url.Values{}
	if spec.Model != "" {
		v.Set("model", spec.Model)
	}
	switch spec.Workflow {
	case "agentic-agent":
		if spec.Agent != "" {
			v.Set("agent", spec.Agent)
		}
	case "benchmark":
		if spec.TemplateHash != "" {
			v.Set("suite", spec.TemplateHash)
		}
	}
	return v
}

// checkBudget calls GET /v1/finops/simulate and blocks the run when the effective estimate
// (cost_high_usd when present, otherwise projected_cost_usd) exceeds spec.budget.maxCostUSD.
func checkBudget(ctx context.Context, apiBase string, spec benchv1alpha1.RuneBenchmarkSpec, httpClient *http.Client, token string) error {
	if spec.Budget.MaxCostUSD == nil {
		return nil
	}
	maxVal := spec.Budget.MaxCostUSD.AsApproximateFloat64()
	if maxVal < 0 {
		return fmt.Errorf("budget check: maxCostUSD must be >= 0")
	}

	raw := strings.TrimRight(apiBase, "/") + "/v1/finops/simulate"
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("budget check: invalid API base: %w", err)
	}
	u.RawQuery = finopsSimulateQuery(spec).Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("budget check: failed to build request: %w", err)
	}
	if spec.Tenant != "" {
		req.Header.Set("X-Tenant-ID", spec.Tenant)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("budget check: HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("budget check: failed to read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("budget check: API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed finopsSimulateResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("budget check: failed to parse response: %w", err)
	}
	effective, basis := effectiveBudgetUSD(parsed)
	if effective > maxVal {
		return fmt.Errorf("%w: %s %.4f USD exceeds max %.4f USD", ErrBudgetExceeded, basis, effective, maxVal)
	}
	return nil
}

func (r *RuneBenchmarkReconciler) executeBenchmark(ctx context.Context, obj *benchv1alpha1.RuneBenchmark, timeout time.Duration) (benchv1alpha1.RunRecord, error) {
	record := benchv1alpha1.RunRecord{SubmittedAt: metav1.Now(), Status: "submitted"}

	payload := buildPayload(obj.Spec)
	body, err := jsonMarshal(payload)
	if err != nil {
		return record, fmt.Errorf("failed to marshal request payload: %w", err)
	}

	clientHTTP := &http.Client{Timeout: timeout}
	if obj.Spec.InsecureTLS {
		// #nosec G402 -- explicit opt-in for lab/dev endpoints via RuneBenchmark.spec.insecureTLS
		if baseTransport, ok := http.DefaultTransport.(*http.Transport); ok {
			transport := baseTransport.Clone()
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			clientHTTP.Transport = transport
		}
	}

	requestURL := strings.TrimRight(obj.Spec.APIBaseURL, "/") + "/v1/jobs/" + obj.Spec.Workflow
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return record, err
	}
	req.Header.Set("Content-Type", "application/json")
	if obj.Spec.Tenant != "" {
		req.Header.Set("X-Tenant-ID", obj.Spec.Tenant)
	}

	// Deterministic idempotency key: same resource + generation + schedule = same key.
	// Retries of the same reconciliation produce the same key (safe retry).
	// New generation or new schedule = new key (new job).
	scheduleTime := ""
	if obj.Status.LastScheduleTime != nil {
		scheduleTime = obj.Status.LastScheduleTime.UTC().Format(time.RFC3339)
	}
	idempotencyKey := fmt.Sprintf("%s/%s/%d/%s",
		obj.Namespace, obj.Name, obj.Generation, scheduleTime)
	req.Header.Set("Idempotency-Key", idempotencyKey)

	token, tokenErr := r.readToken(ctx, obj)
	if tokenErr != nil {
		return record, tokenErr
	}

	if err := checkCostEstimate(ctx, obj.Spec.APIBaseURL, obj.Spec, clientHTTP, token); err != nil {
		return record, err
	}
	if err := checkBudget(ctx, obj.Spec.APIBaseURL, obj.Spec, clientHTTP, token); err != nil {
		return record, err
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := clientHTTP.Do(req)
	if err != nil {
		return record, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return record, fmt.Errorf("failed to read RUNE API response body: %w", err)
	}
	if resp.StatusCode >= 300 {
		return record, fmt.Errorf("rune api returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed map[string]any
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return record, fmt.Errorf("failed to parse RUNE API response as JSON: %w", err)
	}
	jobID, _ := parsed["job_id"].(string)
	record.RunID = jobID

	if jobID == "" {
		// No job_id returned — can't poll, treat submission as success
		record.Status = "succeeded"
		return record, nil
	}

	// Poll for actual completion
	pollInterval := time.Duration(maxInt32(obj.Spec.PollIntervalSeconds, int32(defaultPollInterval))) * time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	log := log.FromContext(ctx)
	for {
		select {
		case <-ctx.Done():
			record.Status = "failed"
			record.Error = "timeout waiting for job completion"
			return record, fmt.Errorf("job %s: poll timeout", jobID)
		case <-ticker.C:
		}

		status, pollErr := getJobStatus(ctx, obj.Spec.APIBaseURL, jobID, obj.Spec.Tenant, clientHTTP, token)
		if pollErr != nil {
			log.Info("job poll error (will retry)", "jobId", jobID, "error", pollErr)
			continue
		}

		switch status.Status {
		case "succeeded", "success", "completed":
			record.Status = "succeeded"
			if len(status.Result) > 0 {
				record.Result = string(status.Result)
			}
			return record, nil
		case "failed", "error":
			record.Status = "failed"
			record.Error = status.Message
			if status.Error != "" {
				record.Error = status.Error
			}
			return record, fmt.Errorf("job %s failed: %s", jobID, record.Error)
		case "cancelled":
			record.Status = "failed"
			record.Error = "job cancelled"
			return record, fmt.Errorf("job %s was cancelled", jobID)
		default:
			// "queued", "running", "accepted" — keep polling
			continue
		}
	}
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

func (r *RuneBenchmarkReconciler) readToken(ctx context.Context, obj *benchv1alpha1.RuneBenchmark) (string, error) {
	ref := strings.TrimSpace(obj.Spec.APITokenSecretRef)
	if ref == "" {
		return "", nil
	}
	parts := strings.Split(ref, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("apiTokenSecretRef must be namespace/name")
	}
	if parts[0] != obj.Namespace {
		return "", fmt.Errorf("apiTokenSecretRef namespace must match resource namespace")
	}
	sec := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: obj.Namespace, Name: parts[1]}, sec); err != nil {
		return "", err
	}
	if v, ok := sec.Data["token"]; ok {
		return strings.TrimSpace(string(v)), nil
	}
	return "", fmt.Errorf("token key not found in secret")
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
