package audit

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Uploader stores one object. The production implementation is S3.
type Uploader interface {
	Put(ctx context.Context, key string, body []byte) error
}

// BatchSink buffers events and writes them as gzipped JSONL objects, keyed
// events/dt=YYYY-MM-DD/hour=HH/<host>-<unix-nanos>.jsonl.gz.
//
// Write never blocks: past MaxBuffered pending events, new events are dropped
// and counted, and the count is logged at the next flush. A failed upload is
// retried on the next tick with the same object key.
type BatchSink struct {
	Up          Uploader
	Host        string
	Interval    time.Duration
	BatchSize   int
	MaxBuffered int
	Log         *slog.Logger

	mu      sync.Mutex
	pending []Event
	retry   []pendingObject
	dropped int
	kick    chan struct{}
	once    sync.Once
}

type pendingObject struct {
	key  string
	body []byte
}

func (s *BatchSink) init() {
	s.once.Do(func() {
		s.kick = make(chan struct{}, 1)
		if s.Interval <= 0 {
			s.Interval = time.Minute
		}
		if s.BatchSize <= 0 {
			s.BatchSize = 5000
		}
		if s.MaxBuffered <= 0 {
			s.MaxBuffered = 200000
		}
		if s.Log == nil {
			s.Log = slog.Default()
		}
	})
}

func (s *BatchSink) Write(ev Event) {
	s.init()
	s.mu.Lock()
	if len(s.pending) >= s.MaxBuffered {
		s.dropped++
		s.mu.Unlock()
		return
	}
	s.pending = append(s.pending, ev)
	full := len(s.pending) >= s.BatchSize
	s.mu.Unlock()
	if full {
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
}

// Run flushes on every tick and when a batch fills, and once more when ctx is
// cancelled so a clean shutdown loses nothing.
func (s *BatchSink) Run(ctx context.Context) {
	s.init()
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			s.Flush(fctx)
			cancel()
			return
		case <-t.C:
			s.Flush(ctx)
		case <-s.kick:
			s.Flush(ctx)
		}
	}
}

// Flush uploads everything pending, including earlier failed objects.
func (s *BatchSink) Flush(ctx context.Context) {
	s.init()
	s.mu.Lock()
	batch := s.pending
	s.pending = nil
	retry := s.retry
	s.retry = nil
	dropped := s.dropped
	s.dropped = 0
	s.mu.Unlock()

	if dropped > 0 {
		s.Log.Error("audit events dropped: buffer full", "dropped", dropped)
	}
	objs := retry
	for len(batch) > 0 {
		n := min(len(batch), s.BatchSize)
		obj, err := s.encode(batch[:n])
		if err != nil {
			s.Log.Error("audit batch encode failed", "err", err, "events", n)
		} else {
			objs = append(objs, obj)
		}
		batch = batch[n:]
	}
	var failed []pendingObject
	for _, o := range objs {
		if err := s.Up.Put(ctx, o.key, o.body); err != nil {
			s.Log.Error("audit batch upload failed", "err", err, "key", o.key)
			failed = append(failed, o)
		}
	}
	if len(failed) > 0 {
		limit := max(1, s.MaxBuffered/s.BatchSize)
		if over := len(failed) - limit; over > 0 {
			s.Log.Error("audit batches dropped: upload backlog full", "objects", over)
			failed = failed[over:]
		}
		s.mu.Lock()
		s.retry = append(failed, s.retry...)
		s.mu.Unlock()
	}
}

func (s *BatchSink) encode(evs []Event) (pendingObject, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	enc := json.NewEncoder(zw)
	for _, ev := range evs {
		if err := enc.Encode(ev); err != nil {
			return pendingObject{}, err
		}
	}
	if err := zw.Close(); err != nil {
		return pendingObject{}, err
	}
	ts := evs[0].Time.UTC()
	key := fmt.Sprintf("events/dt=%s/hour=%02d/%s-%d.jsonl.gz",
		ts.Format("2006-01-02"), ts.Hour(), s.Host, time.Now().UnixNano())
	return pendingObject{key: key, body: buf.Bytes()}, nil
}
