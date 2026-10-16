package main

import (
	"testing"
	"time"
)

func TestBatchPlan(t *testing.T) {
	for _, tc := range []struct {
		rate     float64
		size     int
		interval time.Duration
	}{
		{0, 1, time.Second},
		{1, 1, time.Second},
		{14, 14, time.Second},
		{50, 50, time.Second},
		{100, 50, 500 * time.Millisecond},
	} {
		if size, interval := batchPlan(tc.rate); size != tc.size || interval != tc.interval {
			t.Errorf("batchPlan(%v) = %d, %v; want %d, %v", tc.rate, size, interval, tc.size, tc.interval)
		}
	}
}
