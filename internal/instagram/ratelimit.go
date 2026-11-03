package instagram

import (
	"context"
	"log"
	"time"
)

const maxRetries = 3

// MaxRateLimitWait caps how long we are willing to sleep for a single
// "please wait" response before giving up and surfacing the error.
const MaxRateLimitWait = 2 * time.Minute

// WithRetry invokes fn, backing off when Instagram says to slow down and
// retrying a dropped connection once. Login, challenge and IP problems are
// returned immediately: retrying those only makes Instagram more
// suspicious of the account.
func WithRetry(ctx context.Context, fn func() error) error {
	rateWait := 60 * time.Second
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		var wait time.Duration
		switch bridgeKind(err) {
		case "rate_limited":
			if rateWait > MaxRateLimitWait {
				return err
			}
			log.Printf("[RateLimit] instagram asked to slow down — waiting %s before retry", rateWait)
			wait = rateWait
			rateWait *= 2
		case "network":
			if attempt > 0 {
				return err
			}
			wait = 3 * time.Second
		default:
			return err
		}

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return lastErr
}

// IsAuthError reports whether err means the account can't act until
// someone logs it in again (wrong password, security check, ended session).
func IsAuthError(err error) bool {
	switch bridgeKind(err) {
	case "bad_password", "challenge", "login_required":
		return true
	}
	return false
}
