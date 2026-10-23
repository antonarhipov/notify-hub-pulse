package pulse

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type senderFunc func(context.Context, Notification) error

func (f senderFunc) Send(ctx context.Context, n Notification) error { return f(ctx, n) }

func TestWorkerPoolAgainstHTTP(t *testing.T) {
	var active, peak, calls atomic.Int32
	var release sync.Once
	barrier := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old; old = peak.Load() {
			if peak.CompareAndSwap(old, current) {
				break
			}
		}
		calls.Add(1)
		if current == 3 {
			release.Do(func() { close(barrier) })
		}
		select {
		case <-barrier:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{"success":true,"notificationId":"id"}`)
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, time.Second)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	scenario, _ := NewScenario("normal")
	summary, err := Run(ctx, Config{Rate: 1000, Count: 20, Workers: 3}, scenario, client)
	if err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 3 {
		t.Errorf("peak concurrency = %d, want 3", peak.Load())
	}
	if calls.Load() != 20 || summary.Accepted != 20 || summary.Failed != 0 {
		t.Fatalf("calls %d, summary %+v", calls.Load(), summary)
	}
	if summary.Channels["email"].Accepted != 12 || summary.Channels["sms"].Accepted != 4 || summary.Channels["push"].Accepted != 4 {
		t.Fatalf("unexpected channel counts: %+v", summary.Channels)
	}
}

func TestRunPacing(t *testing.T) {
	scenario, _ := NewScenario("normal")
	var starts []time.Time
	sender := senderFunc(func(context.Context, Notification) error { starts = append(starts, time.Now()); return nil })
	summary, err := Run(context.Background(), Config{Rate: 50, Count: 4, Workers: 1}, scenario, sender)
	if err != nil || summary.Accepted != 4 {
		t.Fatalf("summary %+v, err %v", summary, err)
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 18*time.Millisecond {
			t.Fatalf("requests not paced: %s", gap)
		}
	}
}

func TestDurationStopsSchedulingAndDrainsRequests(t *testing.T) {
	scenario, _ := NewScenario("normal")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	sender := senderFunc(func(ctx context.Context, _ Notification) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan Summary, 1)
	go func() {
		summary, err := Run(ctx, Config{Rate: 100, Duration: 30 * time.Millisecond, Workers: 1}, scenario, sender)
		if err != nil {
			t.Error(err)
		}
		done <- summary
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("request never started")
	}
	select {
	case <-done:
		t.Fatal("did not drain in-flight request")
	case <-time.After(80 * time.Millisecond):
	}
	unblock()
	select {
	case summary := <-done:
		if summary.Total() != 1 || summary.Accepted != 1 {
			t.Fatalf("unexpected summary %+v", summary)
		}
	case <-time.After(time.Second):
		t.Fatal("run did not cancel")
	}
}

func TestCancellationStopsWorkers(t *testing.T) {
	scenario, _ := NewScenario("duplicates")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 3)
	sender := senderFunc(func(ctx context.Context, _ Notification) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	done := make(chan Summary, 1)
	go func() {
		summary, err := Run(ctx, Config{Rate: 1000, Count: 100, Workers: 3}, scenario, sender)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected cancellation, got %v", err)
		}
		done <- summary
	}()
	for range 3 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("worker did not start")
		}
	}
	cancel()
	select {
	case summary := <-done:
		if summary.Canceled != 3 || summary.Total() != 3 {
			t.Fatalf("unexpected cancellation counts: %+v", summary)
		}
	case <-time.After(time.Second):
		t.Fatal("workers did not exit")
	}
}

func TestFailedRequestsAreCounted(t *testing.T) {
	scenario, _ := NewScenario("normal")
	sender := senderFunc(func(context.Context, Notification) error { return errors.New("HTTP 503") })
	summary, err := Run(context.Background(), Config{Rate: 1000, Count: 10, Workers: 2}, scenario, sender)
	if err != nil || summary.Total() != 10 || summary.Failed != 10 || len(summary.Errors) != 3 {
		t.Fatalf("summary %+v, err %v", summary, err)
	}
}

func TestParentDeadlineCancelsInFlightRequest(t *testing.T) {
	scenario, _ := NewScenario("normal")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	sender := senderFunc(func(ctx context.Context, _ Notification) error {
		<-ctx.Done()
		return ctx.Err()
	})
	summary, err := Run(ctx, Config{Rate: 100, Count: 100, Workers: 1}, scenario, sender)
	if !errors.Is(err, context.DeadlineExceeded) || summary.Canceled != 1 || summary.Failed != 0 {
		t.Fatalf("summary %+v, err %v", summary, err)
	}
}
