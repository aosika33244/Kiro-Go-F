package proxy

import (
	"net/http"
	"testing"
)

func TestRequestLogBufferAddAndSnapshot(t *testing.T) {
	b := newRequestLogBuffer(3)

	for i := 0; i < 2; i++ {
		b.Add(RequestLogEntry{Model: "m", Success: true})
	}

	all := b.Snapshot(0, 0)
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
	if all[0].Seq != 1 || all[1].Seq != 2 {
		t.Fatalf("expected seq 1,2 got %d,%d", all[0].Seq, all[1].Seq)
	}
	if all[0].Timestamp == 0 {
		t.Fatalf("expected timestamp to be auto-filled")
	}
}

func TestRequestLogBufferRingEviction(t *testing.T) {
	b := newRequestLogBuffer(3)
	for i := 0; i < 5; i++ {
		b.Add(RequestLogEntry{})
	}
	all := b.Snapshot(0, 0)
	if len(all) != 3 {
		t.Fatalf("expected ring to cap at 3, got %d", len(all))
	}
	// Oldest two (seq 1,2) should have been evicted; chronological order preserved.
	if all[0].Seq != 3 || all[1].Seq != 4 || all[2].Seq != 5 {
		t.Fatalf("expected seq 3,4,5 got %d,%d,%d", all[0].Seq, all[1].Seq, all[2].Seq)
	}
}

func TestRequestLogBufferIncrementalSnapshot(t *testing.T) {
	b := newRequestLogBuffer(10)
	for i := 0; i < 5; i++ {
		b.Add(RequestLogEntry{})
	}
	// Only entries after seq 3.
	got := b.Snapshot(3, 0)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries after seq 3, got %d", len(got))
	}
	if got[0].Seq != 4 || got[1].Seq != 5 {
		t.Fatalf("expected seq 4,5 got %d,%d", got[0].Seq, got[1].Seq)
	}
	if b.LatestSeq() != 5 {
		t.Fatalf("expected LatestSeq 5, got %d", b.LatestSeq())
	}
}

func TestRequestLogBufferLimit(t *testing.T) {
	b := newRequestLogBuffer(10)
	for i := 0; i < 8; i++ {
		b.Add(RequestLogEntry{})
	}
	got := b.Snapshot(0, 3)
	if len(got) != 3 {
		t.Fatalf("expected limit 3, got %d", len(got))
	}
	// Limit keeps the most recent entries.
	if got[0].Seq != 6 || got[2].Seq != 8 {
		t.Fatalf("expected seq 6..8, got %d..%d", got[0].Seq, got[2].Seq)
	}
}

func TestClientIPFromRequest(t *testing.T) {
	tests := []struct {
		name string
		set  func(*http.Request)
		addr string
		want string
	}{
		{
			name: "x-forwarded-for first hop",
			set:  func(r *http.Request) { r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1") },
			addr: "10.0.0.1:5555",
			want: "203.0.113.7",
		},
		{
			name: "x-real-ip fallback",
			set:  func(r *http.Request) { r.Header.Set("X-Real-IP", "198.51.100.9") },
			addr: "10.0.0.1:5555",
			want: "198.51.100.9",
		},
		{
			name: "remote addr strips port",
			set:  func(r *http.Request) {},
			addr: "192.0.2.4:6789",
			want: "192.0.2.4",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest("POST", "/v1/messages", nil)
			r.RemoteAddr = tc.addr
			tc.set(r)
			if got := clientIPFromRequest(r); got != tc.want {
				t.Fatalf("clientIPFromRequest = %q, want %q", got, tc.want)
			}
		})
	}
}
