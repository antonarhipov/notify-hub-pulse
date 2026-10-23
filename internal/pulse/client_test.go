package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientContract(t *testing.T) {
	want := Notification{"alice@example.com", "email", "Welcome", "Hello Alice"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/demo/api/notify" {
			t.Errorf("unexpected route: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Error("missing JSON headers")
		}
		var got Notification
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&got); err != nil {
			t.Error(err)
		}
		if got != want {
			t.Errorf("payload = %+v, want %+v", got, want)
		}
		fmt.Fprint(w, `{"success":true,"message":"accepted","notificationId":"demo-id"}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL+"/demo/", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Send(context.Background(), want); err != nil {
		t.Fatal(err)
	}
}

func TestClientFailuresAreNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"rejected", 400, `{"success":false}`},
		{"delivery failed", 502, `{"success":false}`},
		{"history unavailable", 503, `{"success":false}`},
		{"redirect", 302, ""},
		{"malformed JSON", 200, "oops"},
		{"unconfirmed", 200, `{"success":false,"notificationId":"id"}`},
		{"missing ID", 200, `{"success":true}`},
		{"trailing JSON", 200, `{"success":true,"notificationId":"id"}{}`},
		{"oversized", 200, strings.Repeat(" ", 65537)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, time.Second)
			defer client.Close()
			if err := client.Send(context.Background(), Notification{}); err == nil {
				t.Fatal("failure accepted")
			}
			if calls.Load() != 1 {
				t.Errorf("made %d calls, want exactly 1", calls.Load())
			}
		})
	}
}

func TestClientTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload Notification
		_ = json.NewDecoder(r.Body).Decode(&payload)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, 20*time.Millisecond)
	defer client.Close()
	if err := client.Send(context.Background(), Notification{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Send(ctx, Notification{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestClientInvalidConfiguration(t *testing.T) {
	for _, base := range []string{"", "localhost:8080", "ftp://host", "http://", "http://host?x=y", "http://host?", "http://host/#fragment", "http://user:pass@host"} {
		if c, err := NewClient(base, time.Second); err == nil {
			c.Close()
			t.Errorf("accepted %q", base)
		}
	}
	if _, err := NewClient("http://localhost", 0); err == nil {
		t.Error("accepted zero timeout")
	}
}
