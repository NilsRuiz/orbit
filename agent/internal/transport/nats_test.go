package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/nats-io/nats.go"
)

func TestAsyncErrorHandler(t *testing.T) {
	tests := []struct {
		name        string
		sub         *nats.Subscription
		err         error
		wantSubject any // nil: no "subject" attribute
	}{
		{
			name: "connection-level error has no subject",
			sub:  nil,
			err:  nats.ErrPermissionViolation,
		},
		{
			name:        "subscription error carries its subject",
			sub:         &nats.Subscription{Subject: "orbit.edge.sat-1.status"},
			err:         nats.ErrConsumerNotActive,
			wantSubject: "orbit.edge.sat-1.status",
		},
		{
			name:        "wrapped error is logged verbatim",
			sub:         &nats.Subscription{Subject: "x"},
			err:         errors.Join(errors.New("context"), nats.ErrSlowConsumer),
			wantSubject: "x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			asyncErrorHandler(slog.New(slog.NewJSONHandler(&buf, nil)))(nil, tt.sub, tt.err)

			var rec map[string]any
			if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
				t.Fatalf("not a single JSON record: %q", buf.String())
			}
			if rec["level"] != "WARN" || rec["msg"] != "nats async error" {
				t.Errorf("level/msg = %v/%v", rec["level"], rec["msg"])
			}
			if rec["err"] != tt.err.Error() {
				t.Errorf("err = %v, want %q", rec["err"], tt.err.Error())
			}
			if rec["subject"] != tt.wantSubject {
				t.Errorf("subject = %v, want %v", rec["subject"], tt.wantSubject)
			}
			// A subscription that is not bound to a connection has no readable drop count.
			if _, ok := rec["dropped"]; ok {
				t.Errorf("unexpected dropped attribute: %v", rec["dropped"])
			}
		})
	}
}
