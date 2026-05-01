package v1alpha1

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDeepCopy(t *testing.T) {
	q := resource.MustParse("100")
	now := metav1.Now()

	benchmark := &RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-benchmark",
		},
		Spec: RuneBenchmarkSpec{
			Schedule: "* * * * *",
			Suspend:  true,
			InfrastructureRef: &corev1.ObjectReference{
				Name: "test-infra",
			},
			Provisioning: &Provisioning{
				VastAI: &VastAIProvisioning{
					TemplateHash: "abc",
				},
			},
			Budget: Budget{
				MaxCostUSD: &q,
			},
		},
		Status: RuneBenchmarkStatus{
			LastScheduleTime:   &now,
			LastSuccessfulTime: &now,
			History: []RunRecord{
				{
					SubmittedAt: now,
					CompletedAt: now,
					RunID:       "job-1",
				},
			},
			Conditions: []metav1.Condition{
				{
					Type:   "Ready",
					Status: "True",
				},
			},
		},
	}

	copied := benchmark.DeepCopy()
	if copied.Name != benchmark.Name {
		t.Errorf("Expected Name %v, got %v", benchmark.Name, copied.Name)
	}

	copiedObj := benchmark.DeepCopyObject()
	if copiedObj == nil {
		t.Error("DeepCopyObject returned nil")
	}

	list := &RuneBenchmarkList{
		Items: []RuneBenchmark{*benchmark},
	}
	copiedList := list.DeepCopy()
	if len(copiedList.Items) != 1 {
		t.Error("Expected 1 item in list")
	}

	copiedListObj := list.DeepCopyObject()
	if copiedListObj == nil {
		t.Error("DeepCopyObject returned nil for list")
	}

	var nilBenchmark *RuneBenchmark
	if nilBenchmark.DeepCopy() != nil {
		t.Error("Expected nil from DeepCopy of nil RuneBenchmark")
	}
	if nilBenchmark.DeepCopyObject() != nil {
		t.Error("Expected nil from DeepCopyObject of nil RuneBenchmark")
	}

	var nilList *RuneBenchmarkList
	if nilList.DeepCopy() != nil {
		t.Error("Expected nil from DeepCopy of nil RuneBenchmarkList")
	}
	if nilList.DeepCopyObject() != nil {
		t.Error("Expected nil from DeepCopyObject of nil RuneBenchmarkList")
	}

	var nilSpec *RuneBenchmarkSpec
	if nilSpec.DeepCopy() != nil {
		t.Error("Expected nil from DeepCopy of nil RuneBenchmarkSpec")
	}
}
