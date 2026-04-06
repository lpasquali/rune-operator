// SPDX-License-Identifier: Apache-2.0
package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestConditionReadyFields(t *testing.T) {
	c := ConditionReady(metav1.ConditionTrue, "Ok", "all good", 7)
	if c.Type != "Ready" || c.Status != metav1.ConditionTrue || c.Reason != "Ok" || c.Message != "all good" || c.ObservedGeneration != 7 {
		t.Fatalf("unexpected condition: %+v", c)
	}
	if c.LastTransitionTime.IsZero() {
		t.Fatalf("expected transition time to be set")
	}
}

func TestAddToSchemeAndDeepCopy(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme failed: %v", err)
	}

	now := metav1.NewTime(time.Now().UTC())
	success := metav1.NewTime(time.Now().UTC())
	rb := &RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: "ns"},
		Spec:       RuneBenchmarkSpec{Workflow: "wf", Question: "q", Agent: "holmes", AttestationRequired: true},

		Status: RuneBenchmarkStatus{
			LastScheduleTime:   &now,
			LastSuccessfulTime: &success,
			LastRun:            RunRecord{RunID: "id", SubmittedAt: now, CompletedAt: now},
			History:            []RunRecord{{RunID: "h1", SubmittedAt: now}},
			Conditions:         []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}},
		},
	}

	copyRB := rb.DeepCopy()
	if copyRB == nil || copyRB.Name != rb.Name || copyRB.Status.LastRun.RunID != "id" {
		t.Fatalf("unexpected deep copy result: %+v", copyRB)
	}
	if copyRB.Spec.Agent != "holmes" {
		t.Fatalf("expected Agent to be copied, got %q", copyRB.Spec.Agent)
	}
	if !copyRB.Spec.AttestationRequired {
		t.Fatalf("expected AttestationRequired to be copied as true")
	}

	obj := rb.DeepCopyObject()
	if obj == nil {
		t.Fatalf("expected DeepCopyObject result")
	}

	list := &RuneBenchmarkList{Items: []RuneBenchmark{*rb}}
	copyList := list.DeepCopy()
	if copyList == nil || len(copyList.Items) != 1 {
		t.Fatalf("unexpected list deep copy result: %+v", copyList)
	}
	if list.DeepCopyObject() == nil {
		t.Fatalf("expected list DeepCopyObject result")
	}

	statusCopy := &RuneBenchmarkStatus{}
	rb.Status.DeepCopyInto(statusCopy)
	if statusCopy.LastScheduleTime == nil || len(statusCopy.History) != 1 {
		t.Fatalf("unexpected status deepcopy: %+v", statusCopy)
	}

	recordCopy := &RunRecord{}
	rb.Status.LastRun.DeepCopyInto(recordCopy)
	if recordCopy.RunID != "id" {
		t.Fatalf("unexpected record deepcopy: %+v", recordCopy)
	}
}

func TestDeepCopyNilReceivers(t *testing.T) {
	var rb *RuneBenchmark
	if rb.DeepCopy() != nil || rb.DeepCopyObject() != nil {
		t.Fatalf("expected nil deep copy/object for nil RuneBenchmark")
	}

	var list *RuneBenchmarkList
	if list.DeepCopy() != nil || list.DeepCopyObject() != nil {
		t.Fatalf("expected nil deep copy/object for nil RuneBenchmarkList")
	}
}

func TestRuneBenchmarkStatusDeepCopyIntoWithNilFields(t *testing.T) {
	in := RuneBenchmarkStatus{}
	out := RuneBenchmarkStatus{History: []RunRecord{{RunID: "x"}}}
	in.DeepCopyInto(&out)
	if out.LastScheduleTime != nil || out.LastSuccessfulTime != nil {
		t.Fatalf("expected nil times after deepcopy, got %+v", out)
	}
	if out.History != nil || out.Conditions != nil {
		t.Fatalf("expected nil slices after deepcopy from zero input, got %+v", out)
	}
}
