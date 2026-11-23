package transport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// syncBuffer is a goroutine-safe log sink: NATS async callbacks log from their own goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// records returns the JSON log records written so far.
func (b *syncBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(b.buf.String()))
	for sc.Scan() {
		var r map[string]any
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("log line is not JSON: %q", sc.Text())
		}
		out = append(out, r)
	}
	return out
}

// TestAsyncErrorsAreLogged checks that asynchronous NATS errors, which nats.go reports only
// through the error callback, end up in the agent's structured logs (#31).
func TestAsyncErrorsAreLogged(t *testing.T) {
	srv := startServer(t)
	var logs syncBuffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))

	n, err := Connect(srv.ClientURL(), "sat-1", "", "", log)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	// A subscription that can hold a single pending message and never drains: the second
	// message makes it a slow consumer, reported asynchronously.
	const subject = "orbit.test.slow"
	block := make(chan struct{})
	defer close(block)
	sub, err := n.nc.Subscribe(subject, func(*nats.Msg) { <-block })
	if err != nil {
		t.Fatal(err)
	}
	if err := sub.SetPendingLimits(1, -1); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		if err := n.nc.Publish(subject, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := n.nc.Flush(); err != nil {
		t.Fatal(err)
	}

	logged := eventually(t, 5*time.Second, func() bool {
		for _, r := range logs.records(t) {
			if r["level"] == "WARN" && r["msg"] == "nats async error" &&
				r["subject"] == subject && strings.Contains(r["err"].(string), "slow consumer") &&
				r["dropped"] != nil {
				return true
			}
		}
		return false
	})
	if !logged {
		t.Fatalf("slow consumer error not logged; logs:\n%s", logs.buf.String())
	}
}
