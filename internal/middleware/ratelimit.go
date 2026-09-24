package middleware

import (
	"math"
	"sync"
	"time"

	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/gin-gonic/gin"
)

// RateLimiter is a per-client token bucket, applied per named route group.
//
// Why this exists: several endpoints here tell an anonymous caller whether an
// account exists — /login answers differently for a known and an unknown
// email — and several more write a database row for every anonymous POST
// (contact, quote, newsletter, public reviews). Unlimited, the first is a
// credential-stuffing oracle and the second is free storage for anyone with
// a loop. Neither needs a sophisticated defence; both need *a* defence.
//
// SCOPE: the counters live in this process. One instance, one set of limits —
// which is what this service runs as today. Behind more than one replica the
// effective limit multiplies by the replica count, and that is the moment to
// move the buckets into Redis (REDIS_ADDR is already configured, just unused).
// Being per-instance is a weaker limit, not a broken one; it is recorded here
// so the weakening is a decision rather than a surprise.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	stop    chan struct{}
	once    sync.Once
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter starts a limiter and its eviction janitor. Call Close on
// shutdown. Without eviction the bucket map grows by one entry per distinct
// client IP and never shrinks, which turns the defence into the memory leak
// it was meant to prevent.
func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{
		buckets: make(map[string]*bucket),
		stop:    make(chan struct{}),
	}
	go rl.janitor()
	return rl
}

// Close stops the janitor goroutine.
func (rl *RateLimiter) Close() {
	rl.once.Do(func() { close(rl.stop) })
}

func (rl *RateLimiter) janitor() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-rl.stop:
			return
		case now := <-t.C:
			rl.mu.Lock()
			for k, b := range rl.buckets {
				// Idle for long enough that the bucket has certainly
				// refilled to full; recreating it costs one allocation.
				if now.Sub(b.last) > 15*time.Minute {
					delete(rl.buckets, k)
				}
			}
			rl.mu.Unlock()
		}
	}
}

// Limit returns middleware allowing perMinute requests per client IP on
// average, tolerating a burst of burst requests. name namespaces the counters
// so two limited groups do not share one allowance.
//
// The IP comes from gin's ClientIP, which honours X-Forwarded-For. That is
// correct behind the platform proxy this service runs on and spoofable if it
// is ever exposed directly — check gin's trusted-proxy setting before moving
// this service off a managed platform.
func (rl *RateLimiter) Limit(name string, perMinute, burst int) gin.HandlerFunc {
	if perMinute < 1 {
		perMinute = 1
	}
	if burst < 1 {
		burst = 1
	}
	refillPerSec := float64(perMinute) / 60.0
	capacity := float64(burst)

	return func(c *gin.Context) {
		key := name + "|" + c.ClientIP()
		now := time.Now()

		rl.mu.Lock()
		b, ok := rl.buckets[key]
		if !ok {
			b = &bucket{tokens: capacity, last: now}
			rl.buckets[key] = b
		}
		// Lazy refill: no background work per bucket, just arithmetic on the
		// gap since this client was last seen.
		b.tokens = math.Min(capacity, b.tokens+now.Sub(b.last).Seconds()*refillPerSec)
		b.last = now

		if b.tokens < 1 {
			retryAfter := int(math.Ceil((1 - b.tokens) / refillPerSec))
			rl.mu.Unlock()
			if retryAfter < 1 {
				retryAfter = 1
			}
			_ = c.Error(apierr.RateLimited(retryAfter))
			c.Abort()
			return
		}
		b.tokens--
		rl.mu.Unlock()

		c.Next()
	}
}
