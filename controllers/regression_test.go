package controllers

import (
	"testing"
	"time"
)

func TestRegressionMaxInt32Fallback(t *testing.T) {
	if got := maxInt32(0, 42); got != 42 {
		t.Fatalf("expected fallback 42, got %d", got)
	}
}

func TestRegressionMaxInt32Value(t *testing.T) {
	if got := maxInt32(17, 42); got != 17 {
		t.Fatalf("expected 17, got %d", got)
	}
}

func TestRegressionNextFromCron(t *testing.T) {
	from := time.Date(2026, 4, 2, 10, 7, 0, 0, time.UTC)
	next, err := nextFromCron("*/15 * * * *", from)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !next.After(from) {
		t.Fatalf("expected next time after source")
	}
	if next.Minute()%15 != 0 {
		t.Fatalf("expected quarter-hour boundary, got minute %d", next.Minute())
	}
}
