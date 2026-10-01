package service

import (
	"context"
	"time"
)

// Every runs fn once immediately, then once per interval, until ctx is
// cancelled, and returns when it is.
//
// Running first and waiting after is deliberate. A process that restarts at
// 11:50 and waits a full hour before its first check has a blind spot on
// every deploy; checking at start means a restart never delays a warning.
//
// A slow fn delays the next tick rather than overlapping it: time.Ticker drops
// ticks for a slow receiver, so two runs never overlap. That matters less than
// it looks — the notifier is safe to overlap anyway, because it claims before
// it sends — but a scheduler that can pile up runs is a worse default.
func Every(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	fn(ctx)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}
