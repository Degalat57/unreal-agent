package messagesapi

import (
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/primitives"
)

func TestRetryDelay(t *testing.T) {
	policy := primitives.DefaultRemoteRequest("test", "test", "http://example.invalid").RetryPolicy
	now := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		headers http.Header
		attempt int
		want    time.Duration
	}{
		{http.Header{"Retry-After": {"2.5"}}, 1, 2500 * time.Millisecond},
		{http.Header{"Retry-After-Ms": {"2500"}, "Retry-After": {"5"}}, 1, 2500 * time.Millisecond},
		{http.Header{"Retry-After": {"120"}}, 1, 2 * time.Minute},
		{http.Header{"Retry-After": {"120"}}, 20, 2 * time.Minute},
		{http.Header{"Retry-After": {now.Add(10 * time.Second).Format(http.TimeFormat)}}, 1, 10 * time.Second},
		{http.Header{"Retry-After": {now.Add(time.Hour).Format(http.TimeFormat)}}, 1, time.Hour},
		// Hints below the local backoff do not shorten it.
		{http.Header{"Retry-After-Ms": {"1"}}, 1, 2 * time.Second},
		{http.Header{"Retry-After": {"1"}}, 3, 8 * time.Second},
		{http.Header{"Retry-After": {now.Add(time.Second).Format(http.TimeFormat)}}, 20, policy.MaxBackoff},
		{http.Header{"Retry-After": {now.Add(-time.Minute).Format(http.TimeFormat)}}, 1, 2 * time.Second},
		{http.Header{"Retry-After": {"invalid"}}, 1, 2 * time.Second},
		{http.Header{"Retry-After": {"NaN"}}, 1, 2 * time.Second},
		{http.Header{"Retry-After": {"+Inf"}}, 1, 2 * time.Second},
		{http.Header{"Retry-After": {"0"}}, 1, 2 * time.Second},
		{http.Header{"Retry-After": {"-5"}}, 1, 2 * time.Second},
		// Hints beyond the Duration range saturate instead of overflowing.
		{http.Header{"Retry-After": {"9223372036854775807"}}, 1, time.Duration(math.MaxInt64)},
		{http.Header{"Retry-After-Ms": {"1e300"}}, 1, time.Duration(math.MaxInt64)},
		{http.Header{"Retry-After": {time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat)}}, 1, time.Duration(math.MaxInt64)},
		{nil, 2, 4 * time.Second},
		{nil, 20, policy.MaxBackoff},
	} {
		if got := responseRetryDelay(policy, test.attempt, test.headers, now, 0); got != test.want {
			t.Fatalf("delay=%s want=%s headers=%v attempt=%d", got, test.want, test.headers, test.attempt)
		}
	}
}

// Jitter shortens only the local backoff: a winning server hint is exact, and a
// losing hint still bounds the jittered backoff from below.
func TestRetryDelayJitterNeverShortensHint(t *testing.T) {
	policy := primitives.DefaultRemoteRequest("test", "test", "http://example.invalid").RetryPolicy
	now := time.Now()
	for _, jitter := range []float64{0.25, 0.5, 0.99} {
		if got := responseRetryDelay(policy, 1, http.Header{"Retry-After": {"45"}}, now, jitter); got != 45*time.Second {
			t.Fatalf("jitter %g changed the hint: %s", jitter, got)
		}
		got := responseRetryDelay(policy, 1, http.Header{"Retry-After-Ms": {"1700"}}, now, jitter)
		if got < 1700*time.Millisecond || got > 2*time.Second {
			t.Fatalf("jitter %g delay = %s, want [1.7s, 2s]", jitter, got)
		}
	}
}
