package pulse

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

type Config struct {
	Rate     float64
	Duration time.Duration // Zero for a count-limited run.
	Count    int           // Zero for a duration-limited run.
	Workers  int
}

func (c Config) Validate() error {
	if math.IsNaN(c.Rate) || math.IsInf(c.Rate, 0) || c.Rate <= 0 || c.Rate > float64(time.Second) {
		return fmt.Errorf("rate must be finite, positive, and no greater than 1e9 requests/second")
	}
	if float64(time.Second)/c.Rate >= float64(math.MaxInt64) {
		return fmt.Errorf("rate is too small to represent its pacing interval")
	}
	if c.Workers <= 0 {
		return fmt.Errorf("workers must be positive")
	}
	if c.Count < 0 || c.Duration < 0 || (c.Count == 0) == (c.Duration == 0) {
		return fmt.Errorf("choose either a positive count or a positive duration")
	}
	return nil
}

type Counts struct{ Accepted, Failed, Canceled int }

func (c Counts) Total() int { return c.Accepted + c.Failed + c.Canceled }

type Summary struct {
	Counts
	Elapsed    time.Duration
	Channels   map[string]Counts
	Errors     []string
	Latency    time.Duration
	MaxLatency time.Duration
}

func (s Summary) ChannelNames() []string {
	names := make([]string, 0, len(s.Channels))
	for name := range s.Channels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type result struct {
	channel string
	err     error
	latency time.Duration
}

// Run bounds in-flight requests with an unbuffered jobs channel and a worker pool.
// Rate is a best-effort ceiling; slow HTTP requests apply backpressure. No retries.
// Duration ends scheduling; already-started requests drain using the caller's context.
func Run(ctx context.Context, config Config, scenario Scenario, sender Sender) (Summary, error) {
	if err := config.Validate(); err != nil {
		return Summary{}, err
	}
	started := time.Now()
	var scheduleCtx context.Context
	var cancel context.CancelFunc
	if config.Duration > 0 {
		scheduleCtx, cancel = context.WithTimeout(ctx, config.Duration)
	} else {
		scheduleCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	jobs := make(chan Notification)
	results := make(chan result, config.Workers)
	var workers sync.WaitGroup
	for range config.Workers {
		workers.Go(func() {
			for notification := range jobs {
				if ctx.Err() != nil {
					return
				}
				before := time.Now()
				err := sender.Send(ctx, notification)
				results <- result{notification.Channel, err, time.Since(before)}
			}
		})
	}
	go func() {
		defer close(jobs)
		// The first request starts immediately. Reset after each handoff so a
		// stalled service never causes a catch-up burst of queued timer ticks.
		timer := time.NewTimer(0)
		defer timer.Stop()
		interval := time.Duration(float64(time.Second) / config.Rate)
		for index := 0; config.Count == 0 || index < config.Count; index++ {
			select {
			case <-scheduleCtx.Done():
				return
			case <-timer.C:
			}
			if scheduleCtx.Err() != nil {
				return
			}
			select {
			case <-scheduleCtx.Done():
				return
			case jobs <- scenario.Notification(index):
			}
			timer.Reset(interval)
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	summary := Summary{Channels: make(map[string]Counts)}
	for outcome := range results {
		counts := summary.Channels[outcome.channel]
		switch {
		case outcome.err == nil:
			summary.Accepted++
			counts.Accepted++
		case errors.Is(outcome.err, context.Canceled) || (ctx.Err() != nil && errors.Is(outcome.err, ctx.Err())):
			summary.Canceled++
			counts.Canceled++
		default:
			summary.Failed++
			counts.Failed++
			if len(summary.Errors) < 3 {
				summary.Errors = append(summary.Errors, outcome.err.Error())
			}
		}
		summary.Channels[outcome.channel] = counts
		summary.Latency += outcome.latency
		summary.MaxLatency = max(summary.MaxLatency, outcome.latency)
	}
	summary.Elapsed = time.Since(started)
	return summary, ctx.Err()
}
