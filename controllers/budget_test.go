// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	benchv1alpha1 "github.com/lpasquali/rune-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type faultyBody struct{}

func (f faultyBody) Read(p []byte) (n int, err error) {
	return 0, errors.New("read error")
}
func (f faultyBody) Close() error { return nil }

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testCheckBudget(t *testing.T) {
	ctx := context.Background()
	maxCost := resource.MustParse("10.0")
	spec := benchv1alpha1.RuneBenchmarkSpec{
		Agent: "holmes",
		Model: "m1",
		Budget: benchv1alpha1.Budget{
			MaxCostUSD: &maxCost,
			GPU:        "rtx4090",
		},
	}

	// Case 1: Within budget
	serverOk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"total_cost_usd": 5.0}`))
	}))
	defer serverOk.Close()

	err := checkBudget(ctx, serverOk.URL, spec, http.DefaultClient, "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	// Case 2: Exceeds budget
	serverOver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"total_cost_usd": 15.0}`))
	}))
	defer serverOver.Close()

	err = checkBudget(ctx, serverOver.URL, spec, http.DefaultClient, "")
	if err == nil {
		t.Fatal("expected error for budget exceeded, got nil")
	}

	// Case 3: API error
	serverError := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`internal error`))
	}))
	defer serverError.Close()

	err = checkBudget(ctx, serverError.URL, spec, http.DefaultClient, "")
	if err == nil {
		t.Fatal("expected error for API error, got nil")
	}

	// Case 4: No budget specified
	specNoBudget := benchv1alpha1.RuneBenchmarkSpec{}
	err = checkBudget(ctx, "http://invalid", specNoBudget, http.DefaultClient, "")
	if err != nil {
		t.Fatalf("expected nil error when no budget, got %v", err)
	}

	// Case 5: Bad JSON
	serverBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid-json`))
	}))
	defer serverBadJSON.Close()

	err = checkBudget(ctx, serverBadJSON.URL, spec, http.DefaultClient, "")
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}

	// Case 6: HttpClient.Do error (invalid URL)
	err = checkBudget(ctx, "http://invalid-url-that-does-not-exist-12345", spec, http.DefaultClient, "")
	if err == nil {
		t.Fatal("expected error for HTTP request failed, got nil")
	}

	// Case 7: Token set
	serverToken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"total_cost_usd": 5.0}`))
	}))
	defer serverToken.Close()

	err = checkBudget(ctx, serverToken.URL, spec, http.DefaultClient, "secret-token")
	if err != nil {
		t.Fatalf("expected nil error with token, got %v", err)
	}

	// Case 8: io.ReadAll failure
	faultyClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       faultyBody{},
			}, nil
		}),
	}
	err = checkBudget(ctx, "http://localhost", spec, faultyClient, "")
	if err == nil || !strings.Contains(err.Error(), "failed to read response") {
		t.Fatalf("expected read error, got %v", err)
	}
}

func testGetJobStatus(t *testing.T) {
	ctx := context.Background()

	// Case 1: Success
	serverOk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"running"}`))
	}))
	defer serverOk.Close()

	res, err := getJobStatus(ctx, serverOk.URL, "j1", "t1", http.DefaultClient, "tok")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Status != "running" {
		t.Fatalf("expected status running, got %s", res.Status)
	}

	// Case 2: API error
	serverError := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`not found`))
	}))
	defer serverError.Close()

	_, err = getJobStatus(ctx, serverError.URL, "j1", "t1", http.DefaultClient, "tok")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}

	// Case 3: Bad JSON
	serverBad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{bad`))
	}))
	defer serverBad.Close()

	_, err = getJobStatus(ctx, serverBad.URL, "j1", "t1", http.DefaultClient, "tok")
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}

	// Case 4: Request failed
	_, err = getJobStatus(ctx, "http://invalid-url", "j1", "t1", http.DefaultClient, "tok")
	if err == nil {
		t.Fatal("expected error for invalid URL, got nil")
	}

	// Case 5: io.ReadAll failure
	faultyClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       faultyBody{},
			}, nil
		}),
	}
	_, err = getJobStatus(ctx, "http://localhost", "j1", "t1", faultyClient, "")
	if err == nil {
		t.Fatal("expected read error, got nil")
	}
}

func TestBudgetExtra(t *testing.T) {
	testCheckBudget(t)
	testGetJobStatus(t)
}
