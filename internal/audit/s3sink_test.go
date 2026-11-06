package audit

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeUploader struct {
	mu   sync.Mutex
	fail bool
	objs map[string][]byte
}

func (f *fakeUploader) Put(_ context.Context, key string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("s3 down")
	}
	if f.objs == nil {
		f.objs = map[string][]byte{}
	}
	f.objs[key] = body
	return nil
}

func decode(t *testing.T, body []byte) []Event {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

func ev(path string) Event {
	return Event{Time: time.Date(2026, 10, 25, 16, 4, 0, 0, time.UTC), Path: path, Method: "GET", Status: 200}
}

func TestBatchSinkSplitsBatchesAndKeysByHour(t *testing.T) {
	up := &fakeUploader{}
	s := &BatchSink{Up: up, Host: "pod-1", BatchSize: 2}
	for _, p := range []string{"/a", "/b", "/c"} {
		s.Write(ev(p))
	}
	s.Flush(context.Background())

	if len(up.objs) != 2 {
		t.Fatalf("objects = %d, want 2", len(up.objs))
	}
	total := 0
	for key, body := range up.objs {
		if !strings.HasPrefix(key, "events/dt=2026-10-25/hour=16/pod-1-") || !strings.HasSuffix(key, ".jsonl.gz") {
			t.Errorf("key = %q", key)
		}
		total += len(decode(t, body))
	}
	if total != 3 {
		t.Errorf("events written = %d, want 3", total)
	}
}

func TestBatchSinkRetriesFailedUploadWithSameKey(t *testing.T) {
	up := &fakeUploader{fail: true}
	s := &BatchSink{Up: up, Host: "pod-1"}
	s.Write(ev("/a"))
	s.Flush(context.Background())
	if len(up.objs) != 0 {
		t.Fatal("upload should have failed")
	}

	up.fail = false
	s.Write(ev("/b"))
	s.Flush(context.Background())
	if len(up.objs) != 2 {
		t.Fatalf("objects after recovery = %d, want the retried batch plus the new one", len(up.objs))
	}
}

func TestBatchSinkDropsPastBufferWithoutBlocking(t *testing.T) {
	up := &fakeUploader{}
	s := &BatchSink{Up: up, Host: "pod-1", BatchSize: 100, MaxBuffered: 2}
	for i := 0; i < 5; i++ {
		s.Write(ev("/a"))
	}
	s.Flush(context.Background())
	got := 0
	for _, body := range up.objs {
		got += len(decode(t, body))
	}
	if got != 2 {
		t.Fatalf("events kept = %d, want 2", got)
	}
}

func TestBatchSinkFlushesOnShutdown(t *testing.T) {
	up := &fakeUploader{}
	s := &BatchSink{Up: up, Host: "pod-1", Interval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	s.Write(ev("/a"))
	cancel()
	<-done
	if len(up.objs) != 1 {
		t.Fatalf("objects = %d, want the pending batch flushed on shutdown", len(up.objs))
	}
}
