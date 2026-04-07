// SPDX-License-Identifier: Apache-2.0
package v1alpha1

import (
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
	// BackendType is the LLM backend type (e.g., "ollama", "k8s-inference").
	// +kubebuilder:default="ollama"
	BackendType    string `json:"backendType,omitempty"`
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

	// Vast.ai provisioning options (ollama-instance, benchmark)
	VastAI             bool    `json:"vastai,omitempty"`
	TemplateHash       string  `json:"templateHash,omitempty"`
	MinDPH             float64 `json:"minDph,omitempty"`
	MaxDPH             float64 `json:"maxDph,omitempty"`
	Reliability        float64 `json:"reliability,omitempty"`
	VastAIStopInstance bool    `json:"vastaiStopInstance,omitempty"`

	// Agent to run for agentic-agent workflow (e.g. holmes, k8sgpt)
	Agent string `json:"agent,omitempty"`
	// When true, demands SLSA L3 signed provenance before execution
	AttestationRequired bool `json:"attestationRequired,omitempty"`
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
