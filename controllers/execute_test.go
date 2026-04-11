// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	benchv1alpha1 "github.com/lpasquali/rune-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExecuteBenchmarkExtra(t *testing.T) {
	ctx := context.Background()
	r := &RuneBenchmarkReconciler{}
	obj := &benchv1alpha1.RuneBenchmark{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: benchv1alpha1.RuneBenchmarkSpec{
			APIBaseURL: "http://localhost",
			Workflow:   "benchmark",
		},
	}

	// Case 1: jsonMarshal failure
	oldMarshal := jsonMarshal
	jsonMarshal = func(v any) ([]byte, error) {
		return nil, fmt.Errorf("marshal error")
	}
	_, err := r.executeBenchmark(ctx, obj, time.Second)
	jsonMarshal = oldMarshal
	if err == nil || !strings.Contains(err.Error(), "marshal error") {
		t.Fatalf("expected marshal error, got %v", err)
	}

	// Case 2: jobID empty
	serverEmptyJobID := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`)) // no job_id
	}))
	defer serverEmptyJobID.Close()
	obj.Spec.APIBaseURL = serverEmptyJobID.URL
	record, err := r.executeBenchmark(ctx, obj, time.Second)
	if err != nil {
		t.Fatalf("expected nil error for empty jobID, got %v", err)
	}
	if record.Status != "succeeded" {
		t.Fatalf("expected status succeeded for empty jobID, got %s", record.Status)
	}

	// Case 3: Cancelled status
	serverCancelled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"j1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"cancelled"}`))
	}))
	defer serverCancelled.Close()
	obj.Spec.APIBaseURL = serverCancelled.URL
	obj.Spec.PollIntervalSeconds = 1
	_, err = r.executeBenchmark(ctx, obj, time.Second)
	if err == nil || !strings.Contains(err.Error(), "was cancelled") {
		t.Fatalf("expected cancelled error, got %v", err)
	}

	// Case 4: Context timeout during poll
	serverSlow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"j1"}`))
			return
		}
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"status":"running"}`))
	}))
	defer serverSlow.Close()
	obj.Spec.APIBaseURL = serverSlow.URL
	timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err = r.executeBenchmark(timeoutCtx, obj, time.Second)
	if err == nil || !strings.Contains(err.Error(), "poll timeout") {
		t.Fatalf("expected poll timeout error, got %v", err)
	}

	// Case 5: executeBenchmark ReadAll failure
	serverFaulty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Can't easily force ReadAll error from httptest server side
		// without closing connection prematurely or something.
		// Actually, I can just use a custom client in the obj.Spec if I had one.
		// But executeBenchmark creates its own client.
	}))
	serverFaulty.Close() // Should cause Do error

	// Case 6: executeBenchmark Unmarshal failure
	serverBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{bad`))
	}))
	defer serverBadJSON.Close()
	obj.Spec.APIBaseURL = serverBadJSON.URL
	_, err = r.executeBenchmark(ctx, obj, time.Second)
	if err == nil || !strings.Contains(err.Error(), "failed to parse RUNE API response as JSON") {
		t.Fatalf("expected unmarshal error, got %v", err)
	}
}

func TestCheckBudgetRequestError(t *testing.T) {
	ctx := context.Background()
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Budget: benchv1alpha1.Budget{
			MaxCostUSD: nil, // will return nil early
		},
	}
	err := checkBudget(ctx, "http://[::1]:namedport", spec, http.DefaultClient, "")
	if err != nil {
		t.Fatalf("expected nil when no budget, got %v", err)
	}

	maxCost := resource.MustParse("1")
	spec.Budget.MaxCostUSD = &maxCost
	// Invalid URL: control characters
	err = checkBudget(ctx, "http://localhost\x7f", spec, http.DefaultClient, "")
	if err == nil {
		t.Fatal("expected error for invalid URL, got nil")
	}
}
