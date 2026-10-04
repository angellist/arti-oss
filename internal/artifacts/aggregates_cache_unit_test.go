package artifacts

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func labelsResp(label string) AggregatesResponse {
	return AggregatesResponse{Labels: []LabelCount{{Label: label, Count: 1}}}
}

func TestAggregatesCache_ColdMissLoadsSynchronously(t *testing.T) {
	c := &aggregatesCache{ttl: time.Hour, entries: map[string]*aggregatesEntry{}}
	got, err := c.get(context.Background(), "a@x.com", func(context.Context) (AggregatesResponse, error) {
		return labelsResp("v1"), nil
	})
	if err != nil || got.Labels[0].Label != "v1" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestAggregatesCache_ColdMissErrorIsReturnedAndNotCached(t *testing.T) {
	c := &aggregatesCache{ttl: time.Hour, entries: map[string]*aggregatesEntry{}}
	_, err := c.get(context.Background(), "a@x.com", func(context.Context) (AggregatesResponse, error) {
		return AggregatesResponse{}, errors.New("boom")
	})
	if err == nil {
		t.Fatal("want error")
	}
	if len(c.entries) != 0 {
		t.Fatalf("error was cached: %+v", c.entries)
	}
}

func TestAggregatesCache_StaleEntryServedImmediatelyAndRefreshedOnce(t *testing.T) {
	c := &aggregatesCache{ttl: time.Minute, entries: map[string]*aggregatesEntry{
		"a@x.com": {at: time.Now().Add(-time.Hour), cached: labelsResp("old")},
	}}
	var calls atomic.Int32
	release := make(chan struct{})
	load := func(context.Context) (AggregatesResponse, error) {
		calls.Add(1)
		<-release
		return labelsResp("new"), nil
	}

	for range 5 {
		got, err := c.get(context.Background(), "a@x.com", load)
		if err != nil || got.Labels[0].Label != "old" {
			t.Fatalf("stale read = %+v, %v; want old served without waiting", got, err)
		}
	}
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for {
		got, _ := c.get(context.Background(), "a@x.com", load)
		if got.Labels[0].Label == "new" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh never landed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("load calls = %d, want 1", n)
	}
}

func TestAggregatesCache_FailedRefreshKeepsOldValueAndRetries(t *testing.T) {
	c := &aggregatesCache{ttl: time.Minute, entries: map[string]*aggregatesEntry{
		"a@x.com": {at: time.Now().Add(-time.Hour), cached: labelsResp("old")},
	}}
	var calls atomic.Int32
	failing := func(context.Context) (AggregatesResponse, error) {
		calls.Add(1)
		return AggregatesResponse{}, errors.New("db down")
	}

	if got, _ := c.get(context.Background(), "a@x.com", failing); got.Labels[0].Label != "old" {
		t.Fatalf("got %+v", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		refreshing := c.entries["a@x.com"].refreshing
		c.mu.Unlock()
		if !refreshing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refreshing flag never cleared")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got, _ := c.get(context.Background(), "a@x.com", failing); got.Labels[0].Label != "old" {
		t.Fatalf("old value lost after failed refresh: %+v", got)
	}
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 2 {
		t.Fatalf("load calls = %d, want 2 (retry after failure)", n)
	}
}

func TestAggregatesCache_IsPerCaller(t *testing.T) {
	c := &aggregatesCache{ttl: time.Hour, entries: map[string]*aggregatesEntry{
		"a@x.com": {at: time.Now(), cached: labelsResp("a-only")},
	}}
	got, _ := c.get(context.Background(), "b@x.com", func(context.Context) (AggregatesResponse, error) {
		return labelsResp("b"), nil
	})
	if got.Labels[0].Label != "b" {
		t.Fatalf("caller b saw %+v", got)
	}
}
