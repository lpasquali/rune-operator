// SPDX-License-Identifier: Apache-2.0
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RuneBenchmarkSpec struct {
	APIBaseURL        string `json:"apiBaseUrl"`
	APITokenSecretRef string `json:"apiTokenSecretRef,omitempty"`
	Tenant            string `json:"tenant,omitempty"`
	Workflow          string `json:"workflow"`
	Question          string `json:"question,omitempty"`
	Model             string `json:"model,omitempty"`
	BackendURL        string `json:"backendUrl,omitempty"`
	// BackendType is the LLM backend type (e.g., "ollama", "bedrock", "k8s-inference").
	// +kubebuilder:default="ollama"
	BackendType string `json:"backendType,omitempty"`
	// Region is required for some backends like "bedrock".
	Region         string `json:"region,omitempty"`
	InsecureTLS    bool   `json:"insecureTls,omitempty"`
	Schedule       string `json:"schedule,omitempty"`
	Suspend        bool   `json:"suspend,omitempty"`
	TimeoutSeconds int32  `json:"timeoutSeconds,omitempty"`
	BackoffSeconds int32  `json:"backoffSeconds,omitempty"`

	// Backend warmup options (agentic-agent, benchmark)
	BackendWarmup               bool  `json:"backendWarmup,omitempty"`
	BackendWarmupTimeoutSeconds int32 `json:"backendWarmupTimeoutSeconds,omitempty"`

	// PollIntervalSeconds is the interval between job status polls (default 5).
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:validation:Maximum=60
	PollIntervalSeconds int32 `json:"pollIntervalSeconds,omitempty"`

	// Kubeconfig path forwarded to agentic-agent and benchmark jobs
	Kubeconfig string `json:"kubeconfig,omitempty"`

	// Provisioning defines optional cloud resource provisioning settings.
	Provisioning *Provisioning `json:"provisioning,omitempty"`

	// CostEstimation configures the pre-flight cost safety gate.
	CostEstimation CostEstimation `json:"costEstimation,omitempty"`

	// Agent to run for agentic-agent workflow (e.g. holmes, k8sgpt)
	Agent string `json:"agent,omitempty"`
	// When true, demands SLSA L3 signed provenance before execution
	AttestationRequired bool `json:"attestationRequired,omitempty"`

	// Budget enforces cost limits on benchmarks.
	Budget Budget `json:"budget,omitempty"`

	// InfrastructureRef optionally references a Crossplane Claim (typically a
	// RuneDatabase or RuneObjectStore managed via rune-charts/crossplane). When
	// set, the reconciler waits for the referenced object to report
	// `type: Synced status: True` AND `type: Ready status: True` before
	// submitting benchmark jobs, and emits an `InfrastructureNotReady` Warning
	// event with 30s requeue otherwise. This prevents races where a benchmark
	// is scheduled before its external dependencies are provisioned.
	//
	// The operator reads the referenced object via the generic controller
	// client using `unstructured.Unstructured`, so no new module dependency on
	// `crossplane-runtime` is pulled in. For GVKs outside
	// `database.infra.rune.ai` and `storage.infra.rune.ai` the cluster admin
	// must grant the operator's ServiceAccount `get` on that group/resource.
	// +optional
	InfrastructureRef *corev1.ObjectReference `json:"infrastructureRef,omitempty"`
}

type Budget struct {
	// Maximum allowed cost in USD for this execution.
	// +kubebuilder:validation:Minimum=0
	MaxCostUSD *resource.Quantity `json:"maxCostUSD,omitempty"`

	// Optional GPU hint for cost estimation (e.g. "rtx4090", "a100").
	GPU string `json:"gpu,omitempty"`
}

// Provisioning encapsulates cloud resource provisioning details.
type Provisioning struct {
	// VastAI provisioning settings.
	VastAI *VastAIProvisioning `json:"vastai,omitempty"`
}

// VastAIProvisioning contains settings for Vast.ai GPU instance provisioning.
type VastAIProvisioning struct {
	// TemplateHash is the Vast.ai template ID.
	TemplateHash string `json:"templateHash"`
	// MinDPH is the minimum dollars per hour.
	MinDPH float64 `json:"minDph,omitempty"`
	// MaxDPH is the maximum dollars per hour.
	MaxDPH float64 `json:"maxDph,omitempty"`
	// Reliability is the minimum reliability score (0.0 to 1.0).
	Reliability float64 `json:"reliability,omitempty"`
	// StopInstance determines if the instance should be destroyed after the run.
	StopInstance bool `json:"stopInstance,omitempty"`
}

// CostEstimation configures the pre-flight cost gate.
// At most one provider flag should be true.
type CostEstimation struct {
	// Cloud providers
	VastAI bool `json:"vastai,omitempty"`
	AWS    bool `json:"aws,omitempty"`
	GCP    bool `json:"gcp,omitempty"`
	Azure  bool `json:"azure,omitempty"`

	// Local hardware estimation
	LocalHardware              bool    `json:"localHardware,omitempty"`
	LocalTDPWatts              float64 `json:"localTdpWatts,omitempty"`
	LocalEnergyRateKWH         float64 `json:"localEnergyRateKwh,omitempty"`
	LocalHardwarePurchasePrice float64 `json:"localHardwarePurchasePrice,omitempty"`
	LocalHardwareLifespanYears float64 `json:"localHardwareLifespanYears,omitempty"`
}

type RunRecord struct {
	RunID          string      `json:"runId,omitempty"`
	SubmittedAt    metav1.Time `json:"submittedAt,omitempty"`
	CompletedAt    metav1.Time `json:"completedAt,omitempty"`
	DurationMillis int64       `json:"durationMillis,omitempty"`
	Status         string      `json:"status,omitempty"`
	Error          string      `json:"error,omitempty"`
	// Result contains the job output as a raw JSON string.
	// +optional
	Result string `json:"result,omitempty"`
}

type RuneBenchmarkStatus struct {
	ObservedGeneration  int64              `json:"observedGeneration,omitempty"`
	LastScheduleTime    *metav1.Time       `json:"lastScheduleTime,omitempty"`
	LastSuccessfulTime  *metav1.Time       `json:"lastSuccessfulTime,omitempty"`
	ConsecutiveFailures int32              `json:"consecutiveFailures,omitempty"`
	LastRun             RunRecord          `json:"lastRun,omitempty"`
	History             []RunRecord        `json:"history,omitempty"`
	Conditions          []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=rbm
// +kubebuilder:printcolumn:name="Workflow",type="string",JSONPath=".spec.workflow"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="LastRun",type="string",JSONPath=".status.lastRun.status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

type RuneBenchmark struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RuneBenchmarkSpec   `json:"spec,omitempty"`
	Status RuneBenchmarkStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type RuneBenchmarkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RuneBenchmark `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RuneBenchmark{}, &RuneBenchmarkList{})
}
