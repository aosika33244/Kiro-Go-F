package proxy

import (
	"kiro-go/config"
	"strings"
	"testing"
	"time"
)

func TestPromptCacheTrackerComputeAndUpdate(t *testing.T) {
	tracker := newPromptCacheTracker(time.Hour)
	longSystem := strings.Repeat("You are a helpful coding assistant with deep knowledge of Go, Rust, Python, and TypeScript. ", 80)
	req := &ClaudeRequest{
		Model: "claude-sonnet-4.5",
		System: []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": longSystem,
				"cache_control": map[string]interface{}{
					"type": "ephemeral",
				},
			},
		},
		Messages: []ClaudeMessage{{Role: "user", Content: "hello world"}},
	}

	profile := tracker.BuildClaudeProfile(req, 120)
	if profile == nil {
		t.Fatalf("expected cache profile to be built")
	}

	first := tracker.Compute("acct-1", profile)
	if first.CacheCreationInputTokens <= 0 {
		t.Fatalf("expected first request to create cache tokens, got %+v", first)
	}
	if first.CacheReadInputTokens != 0 {
		t.Fatalf("expected first request to have zero cache reads, got %+v", first)
	}

	tracker.Update("acct-1", profile)
	second := tracker.Compute("acct-1", profile)
	if second.CacheReadInputTokens <= 0 {
		t.Fatalf("expected repeated request to read cache tokens, got %+v", second)
	}
	if second.CacheCreationInputTokens != 0 {
		t.Fatalf("expected repeated request to avoid cache creation, got %+v", second)
	}
}

func TestBuildClaudeUsageMapIncludesCacheFields(t *testing.T) {
	usage := promptCacheUsage{
		CacheCreationInputTokens:   30,
		CacheReadInputTokens:       20,
		CacheCreation5mInputTokens: 10,
		CacheCreation1hInputTokens: 20,
	}

	m := buildClaudeUsageMap(100, 50, usage, true)

	if got := m["input_tokens"]; got != 50 {
		t.Fatalf("expected billed input tokens 50, got %#v", got)
	}
	if got := m["cache_creation_input_tokens"]; got != 30 {
		t.Fatalf("expected cache creation tokens 30, got %#v", got)
	}
	if got := m["cache_read_input_tokens"]; got != 20 {
		t.Fatalf("expected cache read tokens 20, got %#v", got)
	}
	creation, ok := m["cache_creation"].(map[string]int)
	if !ok {
		t.Fatalf("expected typed cache creation map, got %#v", m["cache_creation"])
	}
	if creation["ephemeral_5m_input_tokens"] != 10 || creation["ephemeral_1h_input_tokens"] != 20 {
		t.Fatalf("unexpected ttl breakdown: %#v", creation)
	}
}

// TestPromptCacheStableAcrossBillingHeaderDrift verifies that Claude Code's
// per-request "x-anthropic-billing-header: cc_version=...; cch=...;" system
// block (whose content drifts on every request) does not break cache hits.
// The tracker should ignore that metadata when fingerprinting cached prefixes.
func TestPromptCacheStableAcrossBillingHeaderDrift(t *testing.T) {
	tracker := newPromptCacheTracker(time.Hour)
	mainSystem := strings.Repeat("You are a helpful coding assistant with deep knowledge of Go, Rust, Python, and TypeScript. ", 80)

	build := func(billingHdr string) *ClaudeRequest {
		return &ClaudeRequest{
			Model: "claude-sonnet-4.5",
			System: []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": billingHdr,
				},
				map[string]interface{}{
					"type": "text",
					"text": mainSystem,
					"cache_control": map[string]interface{}{
						"type": "ephemeral",
					},
				},
			},
			Messages: []ClaudeMessage{{Role: "user", Content: "hello world"}},
		}
	}

	req1 := build("x-anthropic-billing-header: cc_version=2.1.87.1; cch=aaaa;")
	profile1 := tracker.BuildClaudeProfile(req1, 2048)
	if profile1 == nil {
		t.Fatalf("profile1 should be built")
	}
	first := tracker.Compute("acct-1", profile1)
	if first.CacheReadInputTokens != 0 {
		t.Fatalf("expected no cache read on first request, got %+v", first)
	}
	tracker.Update("acct-1", profile1)

	req2 := build("x-anthropic-billing-header: cc_version=2.1.87.42; cch=bbbb; padding=xxyyzz;")
	profile2 := tracker.BuildClaudeProfile(req2, 2048)
	if profile2 == nil {
		t.Fatalf("profile2 should be built")
	}
	second := tracker.Compute("acct-1", profile2)
	if second.CacheReadInputTokens == 0 {
		t.Fatalf("expected cache read after billing header drift, got %+v", second)
	}
}

func TestPromptCacheStableWhenBillingHeaderAppearsOrDisappears(t *testing.T) {
	tracker := newPromptCacheTracker(time.Hour)
	mainSystem := strings.Repeat("You are a helpful coding assistant with deep knowledge of Go, Rust, Python, and TypeScript. ", 80)

	build := func(includeBilling bool) *ClaudeRequest {
		system := []interface{}{}
		if includeBilling {
			system = append(system, map[string]interface{}{
				"type": "text",
				"text": "x-anthropic-billing-header: cc_version=2.1.87.1; cch=aaaa;",
			})
		}
		system = append(system, map[string]interface{}{
			"type": "text",
			"text": mainSystem,
			"cache_control": map[string]interface{}{
				"type": "ephemeral",
			},
		})
		return &ClaudeRequest{
			Model:    "claude-sonnet-4.5",
			System:   system,
			Messages: []ClaudeMessage{{Role: "user", Content: "hello world"}},
		}
	}

	withBilling := tracker.BuildClaudeProfile(build(true), 2048)
	if withBilling == nil {
		t.Fatalf("profile with billing header should be built")
	}
	tracker.Update("acct-1", withBilling)

	withoutBilling := tracker.BuildClaudeProfile(build(false), 2048)
	if withoutBilling == nil {
		t.Fatalf("profile without billing header should be built")
	}
	result := tracker.Compute("acct-1", withoutBilling)
	if result.CacheReadInputTokens == 0 {
		t.Fatalf("expected cache read when billing header disappears, got %+v", result)
	}
}

func TestCanonicalCacheValueIgnoresPositionKeys(t *testing.T) {
	first := canonicalizeCacheValue(stripCachePositionKeys(map[string]interface{}{
		"kind":         "system",
		"system_index": 0,
		"block": map[string]interface{}{
			"type": "text",
			"text": "stable",
		},
	}))
	second := canonicalizeCacheValue(stripCachePositionKeys(map[string]interface{}{
		"kind":         "system",
		"system_index": 1,
		"block": map[string]interface{}{
			"type": "text",
			"text": "stable",
		},
	}))
	if first != second {
		t.Fatalf("expected position keys to be ignored, got %q vs %q", first, second)
	}
}

func TestCanonicalCacheValuePreservesSemanticPositionKeys(t *testing.T) {
	first := canonicalizeCacheValue(map[string]interface{}{
		"kind": "system",
		"block": map[string]interface{}{
			"type":        "text",
			"text":        "stable",
			"block_index": 1,
		},
	})
	second := canonicalizeCacheValue(map[string]interface{}{
		"kind": "system",
		"block": map[string]interface{}{
			"type":        "text",
			"text":        "stable",
			"block_index": 2,
		},
	})
	if first == second {
		t.Fatalf("expected semantic block_index fields to remain fingerprinted")
	}
}

// TestPromptCacheImplicitBreakpointAtMessageEnd verifies that once any
// explicit cache_control breakpoint has been seen, subsequent message-end
// boundaries act as implicit breakpoints. This allows multi-turn conversations
// to hit earlier stored prefix fingerprints even when the newest messages
// lack explicit cache_control.
func TestPromptCacheImplicitBreakpointAtMessageEnd(t *testing.T) {
	tracker := newPromptCacheTracker(time.Hour)
	systemText := strings.Repeat("You are a helpful coding assistant with deep knowledge of Go, Rust, Python, and TypeScript. ", 80)

	baseSystem := []interface{}{
		map[string]interface{}{
			"type": "text",
			"text": systemText,
			"cache_control": map[string]interface{}{
				"type": "ephemeral",
			},
		},
	}

	// Round 1: single user message.
	req1 := &ClaudeRequest{
		Model:    "claude-sonnet-4.5",
		System:   baseSystem,
		Messages: []ClaudeMessage{{Role: "user", Content: "question one"}},
	}
	profile1 := tracker.BuildClaudeProfile(req1, 2048)
	if profile1 == nil {
		t.Fatalf("profile1 should be built")
	}
	tracker.Update("acct-1", profile1)

	// Round 2: conversation continues with new messages. The latest user
	// message has no explicit cache_control; it should still hit the stored
	// prefix via the implicit message-end breakpoint.
	req2 := &ClaudeRequest{
		Model:  "claude-sonnet-4.5",
		System: baseSystem,
		Messages: []ClaudeMessage{
			{Role: "user", Content: "question one"},
			{Role: "assistant", Content: "answer one"},
			{Role: "user", Content: "follow-up question"},
		},
	}
	profile2 := tracker.BuildClaudeProfile(req2, 4096)
	if profile2 == nil {
		t.Fatalf("profile2 should be built")
	}
	result := tracker.Compute("acct-1", profile2)
	if result.CacheReadInputTokens == 0 {
		t.Fatalf("expected cache read via implicit message-end breakpoint, got %+v", result)
	}
}

// buildLongCacheRequest is a small helper for scope-focused tests: it produces a
// request with a single long, cache_control-tagged system block so a stable
// breakpoint above the min-cacheable threshold is guaranteed.
func buildLongCacheRequest() *ClaudeRequest {
	longSystem := strings.Repeat("You are a helpful coding assistant with deep knowledge of Go, Rust, Python, and TypeScript. ", 80)
	return &ClaudeRequest{
		Model: "claude-sonnet-4.5",
		System: []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": longSystem,
				"cache_control": map[string]interface{}{
					"type": "ephemeral",
				},
			},
		},
		Messages: []ClaudeMessage{{Role: "user", Content: "hello world"}},
	}
}

// TestPromptCacheScopeIsolation verifies that cache state is keyed by scope, not
// account: a prefix stored under scope A must not be readable under scope B. This
// is the core guarantee that lets the same logical caller (API key) hit its own
// cache regardless of which round-robin account served the prior turn.
func TestPromptCacheScopeIsolation(t *testing.T) {
	tracker := newPromptCacheTracker(time.Hour)
	profile := tracker.BuildClaudeProfile(buildLongCacheRequest(), 2048)
	if profile == nil {
		t.Fatalf("expected profile to be built")
	}

	// Store under scope "key-A".
	tracker.Update("key-A", profile)

	// A different scope must see no cache read (creation only).
	other := tracker.Compute("key-B", profile)
	if other.CacheReadInputTokens != 0 {
		t.Fatalf("expected zero cache read for a different scope, got %+v", other)
	}

	// The original scope still hits its own cache.
	same := tracker.Compute("key-A", profile)
	if same.CacheReadInputTokens <= 0 {
		t.Fatalf("expected cache read within the same scope, got %+v", same)
	}
}

// TestPromptCacheSameScopeAcrossAccounts is the regression test for the original
// bug: with round-robin, turn 1 ran on account A and turn 2 on account B. When
// the cache scope follows the API key (not the account), the second turn must
// still read the cache the first turn created.
func TestPromptCacheSameScopeAcrossAccounts(t *testing.T) {
	tracker := newPromptCacheTracker(time.Hour)
	profile := tracker.BuildClaudeProfile(buildLongCacheRequest(), 2048)
	if profile == nil {
		t.Fatalf("expected profile to be built")
	}

	// Both turns use the same scope even though physical accounts differ.
	scope := cacheScope("api-key-123")

	first := tracker.Compute(scope, profile)
	if first.CacheReadInputTokens != 0 {
		t.Fatalf("expected no cache read on first turn, got %+v", first)
	}
	tracker.Update(scope, profile)

	second := tracker.Compute(scope, profile)
	if second.CacheReadInputTokens <= 0 {
		t.Fatalf("expected cache read on second turn (same scope), got %+v", second)
	}
}

// TestCacheScopeFallback verifies the empty-key fallback to the shared scope.
func TestCacheScopeFallback(t *testing.T) {
	if got := cacheScope(""); got != sharedCacheScope {
		t.Fatalf("expected empty key ID to map to %q, got %q", sharedCacheScope, got)
	}
	if got := cacheScope("key-1"); got != "key-1" {
		t.Fatalf("expected non-empty key ID to pass through, got %q", got)
	}
}

func TestComputeCacheHitRate(t *testing.T) {
	tests := []struct {
		name string
		e    config.ApiKeyEntry
		want float64
	}{
		{"no usage", config.ApiKeyEntry{}, 0},
		{
			"all read",
			config.ApiKeyEntry{CacheReadTokens: 100},
			1,
		},
		{
			"mixed",
			config.ApiKeyEntry{CacheReadTokens: 80, CacheCreationTokens: 10, UncachedInputTokens: 10},
			0.8,
		},
		{
			"no read",
			config.ApiKeyEntry{CacheCreationTokens: 50, UncachedInputTokens: 50},
			0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := computeCacheHitRate(tc.e); got != tc.want {
				t.Fatalf("computeCacheHitRate = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCacheHitRateAnchoredToEstimateSpace locks the fix for hit-rate dilution:
// when input_tokens is anchored to cacheProfile.TotalInputTokens (the same
// byte-estimate space the cache math uses), the second-call hit rate
// read / input must respect the 85% cap and the billed (uncached) input must
// stay non-negative. Previously input_tokens came from the upstream
// context-percentage reverse-estimate, a coarser/larger space, which diluted
// read / input below the intended cap.
func TestCacheHitRateAnchoredToEstimateSpace(t *testing.T) {
	tracker := newPromptCacheTracker(time.Hour)
	profile := tracker.BuildClaudeProfile(buildLongCacheRequest(), 2048)
	if profile == nil {
		t.Fatalf("expected profile to be built")
	}

	// First call establishes the cache (creation only).
	first := tracker.Compute("anchor-key", profile)
	if first.CacheReadInputTokens != 0 {
		t.Fatalf("expected no read on first call, got %+v", first)
	}
	tracker.Update("anchor-key", profile)

	// Second identical call hits the cache.
	second := tracker.Compute("anchor-key", profile)
	if second.CacheReadInputTokens <= 0 {
		t.Fatalf("expected cache read on second call, got %+v", second)
	}

	// Anchor input to the same estimate space the cache math used.
	input := profile.TotalInputTokens
	if input <= 0 {
		t.Fatalf("expected positive TotalInputTokens, got %d", input)
	}

	// Billed (uncached) input must be non-negative — no underflow/truncation.
	billed := billedClaudeInputTokens(input, second)
	if billed < 0 {
		t.Fatalf("billed input must be non-negative, got %d", billed)
	}

	// Hit rate read / input must respect the 85% cap (with a small tolerance for
	// rounding) and must not be diluted far below it.
	rate := float64(second.CacheReadInputTokens) / float64(input)
	if rate > 0.90 {
		t.Fatalf("hit rate %.3f exceeds the 85%% cap (read=%d input=%d)", rate, second.CacheReadInputTokens, input)
	}
	if rate < 0.50 {
		t.Fatalf("hit rate %.3f looks diluted — input not anchored to estimate space (read=%d input=%d)", rate, second.CacheReadInputTokens, input)
	}
}
