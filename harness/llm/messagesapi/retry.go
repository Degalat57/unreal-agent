package messagesapi

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/primitives"
)

func retryableResponseError(err *APIError, headers http.Header) bool {
	switch headers.Get("X-Should-Retry") {
	case "true":
		return true
	case "false":
		return false
	}
	if err.StatusCode >= 200 && err.StatusCode < 300 {
		switch err.Type {
		case "rate_limit_error", "overloaded_error", "api_error", "timeout_error":
			return true
		default:
			return false
		}
	}
	return err.StatusCode == 408 || err.StatusCode == 409 || err.StatusCode == 429 || err.StatusCode >= 500
}

func responseRetryDelay(policy primitives.RemoteRetryPolicy, attempt int, headers http.Header, now time.Time, jitter float64) time.Duration {
	delay := policy.Backoff(attempt)
	delay -= time.Duration(float64(delay/5) * jitter)
	// The server hint is a floor, not a ceiling: the server knows when the
	// request can succeed, while MaxBackoff only bounds blind local retries.
	return max(retryAfterHint(headers, now), delay)
}

func retryAfterHint(headers http.Header, now time.Time) time.Duration {
	for _, hint := range []struct {
		value string
		unit  time.Duration
	}{{headers.Get("Retry-After-Ms"), time.Millisecond}, {headers.Get("Retry-After"), time.Second}} {
		if number, err := strconv.ParseFloat(strings.TrimSpace(hint.value), 64); err == nil && number > 0 && !math.IsInf(number, 0) {
			// Without the old MaxBackoff clamp the product can exceed the
			// Duration range; converting such a float is undefined, so saturate.
			nanos := number * float64(hint.unit)
			if nanos >= float64(math.MaxInt64) {
				return time.Duration(math.MaxInt64)
			}
			return time.Duration(nanos)
		}
	}
	if deadline, err := http.ParseTime(headers.Get("Retry-After")); err == nil && deadline.After(now) {
		return deadline.Sub(now) // saturates at the Duration range
	}
	return 0
}
