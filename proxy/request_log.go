package proxy

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RequestLogEntry is a single API request record kept in memory for the admin
// panel's live request log. It is intentionally self-contained (no pointers into
// config) so snapshots can be serialized without holding any lock.
type RequestLogEntry struct {
	Seq          int64  `json:"seq"`       // Monotonic sequence number for incremental polling.
	Timestamp    int64  `json:"timestamp"` // Unix milliseconds when the request completed.
	APIKeyID     string `json:"apiKeyId,omitempty"`
	APIKeyName   string `json:"apiKeyName,omitempty"`
	AccountID    string `json:"accountId,omitempty"`
	AccountEmail string `json:"accountEmail,omitempty"`
	Model        string `json:"model,omitempty"`
	ClientIP     string `json:"clientIp,omitempty"`
	Success      bool   `json:"success"`

	InputTokens         int     `json:"inputTokens"`
	OutputTokens        int     `json:"outputTokens"`
	CacheReadTokens     int     `json:"cacheReadTokens"`
	CacheCreationTokens int     `json:"cacheCreationTokens"`
	Credits             float64 `json:"credits"`
	DurationMs          int64   `json:"durationMs"`

	// Error holds the failure message; empty on success. Surfaced in the UI as an
	// expandable detail row.
	Error string `json:"error,omitempty"`
}

// requestLogBuffer is a fixed-capacity ring buffer of RequestLogEntry. It drops
// the oldest entries once full. All access is mutex-guarded; Add is O(1) and is
// safe to call from request handlers. Entries are never persisted — the buffer
// is cleared on restart by design (see the "in-memory ring" storage choice).
type requestLogBuffer struct {
	mu      sync.Mutex
	entries []RequestLogEntry
	size    int   // Number of valid entries currently stored (<= cap).
	next    int   // Index where the next entry will be written.
	seq     int64 // Last assigned sequence number (monotonic, never reset).
}

func newRequestLogBuffer(capacity int) *requestLogBuffer {
	if capacity <= 0 {
		capacity = 500
	}
	return &requestLogBuffer{
		entries: make([]RequestLogEntry, capacity),
	}
}

// Add appends an entry, assigning it the next sequence number and timestamp
// (when the caller left Timestamp zero). Oldest entries are overwritten once the
// ring is full.
func (b *requestLogBuffer) Add(e RequestLogEntry) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e.Seq = b.seq
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().UnixMilli()
	}
	b.entries[b.next] = e
	b.next = (b.next + 1) % len(b.entries)
	if b.size < len(b.entries) {
		b.size++
	}
}

// Snapshot returns stored entries in chronological order (oldest first). When
// sinceSeq > 0, only entries with Seq > sinceSeq are returned (incremental
// polling). When limit > 0, only the most recent `limit` matching entries are
// returned. The returned slice is a copy and safe to use without locking.
func (b *requestLogBuffer) Snapshot(sinceSeq int64, limit int) []RequestLogEntry {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]RequestLogEntry, 0, b.size)
	// Walk from oldest to newest. The oldest valid entry is at `next` when full,
	// otherwise at index 0.
	start := 0
	if b.size == len(b.entries) {
		start = b.next
	}
	for i := 0; i < b.size; i++ {
		e := b.entries[(start+i)%len(b.entries)]
		if sinceSeq > 0 && e.Seq <= sinceSeq {
			continue
		}
		out = append(out, e)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// LatestSeq returns the highest sequence number assigned so far (0 if empty).
func (b *requestLogBuffer) LatestSeq() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// clientIPFromRequest extracts the best-effort client IP, preferring proxy
// forwarding headers (X-Forwarded-For's first hop, then X-Real-IP) before
// falling back to the connection's RemoteAddr. The port is stripped when present.
func clientIPFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First entry is the original client; the rest are intermediary proxies.
		first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
		if first != "" {
			return first
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
