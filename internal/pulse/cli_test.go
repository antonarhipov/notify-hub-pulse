package pulse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCLIRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"unknown"}, {"run", "--unknown"}, {"run", "extra"},
		{"run", "--scenario", "invalid"}, {"run", "--count", "0"}, {"run", "--count", "-1"},
		{"run", "--duration", "0s"}, {"run", "--duration", "-1s"},
		{"run", "--count", "1", "--duration", "1s"},
		{"run", "--rate", "0"}, {"run", "--rate", "-1"}, {"run", "--rate", "NaN"},
		{"run", "--rate", "1e-300"},
		{"run", "--rate", "Inf"}, {"run", "--rate", "1e10"}, {"run", "--workers", "0"},
		{"run", "--timeout", "0s"}, {"run", "--base-url", "invalid"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), args, &stdout, &stderr)
			if code != 2 || stderr.Len() == 0 {
				t.Fatalf("code %d, stderr %s", code, &stderr)
			}
		})
	}
}

func TestCLIDryRun(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		count int
	}{
		{[]string{"run", "--scenario", "duplicates", "--count", "3", "--dry-run"}, 3},
		{[]string{"run", "--scenario", "duplicates", "--dry-run"}, 100},
		{[]string{"run", "--rate", "0.5", "--duration", "3s", "--dry-run"}, 2},
	} {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), tc.args, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("code %d, stderr %s", code, &stderr)
		}
		lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
		if len(lines) != tc.count {
			t.Fatalf("got %d payloads, want %d", len(lines), tc.count)
		}
		for _, line := range lines {
			var n Notification
			if err := json.Unmarshal([]byte(line), &n); err != nil {
				t.Fatal(err)
			}
			if n.Recipient == "" || n.Body == "" {
				t.Fatalf("empty payload: %+v", n)
			}
		}
	}
}

func TestCLIRequestSummaryAndExitCode(t *testing.T) {
	for _, tc := range []struct {
		status, code int
		summary      string
	}{
		{200, 0, "attempted 3 · accepted 3 · failed 0"},
		{503, 1, "attempted 3 · accepted 0 · failed 3"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			fmt.Fprint(w, `{"success":true,"notificationId":"id"}`)
		}))
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"run", "--base-url", server.URL, "--count", "3", "--rate", "1000"}, &stdout, &stderr)
		server.Close()
		if code != tc.code || !strings.Contains(stdout.String(), tc.summary) {
			t.Fatalf("code %d, stdout %s, stderr %s", code, &stdout, &stderr)
		}
	}
}

func TestCLICancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := Execute(ctx, []string{"run", "--count", "100"}, &stdout, &stderr); code != 130 {
		t.Fatalf("code %d", code)
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("output unavailable") }

func TestCLIOutputFailure(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"run", "--count", "1", "--dry-run"}} {
		var stderr bytes.Buffer
		if code := Execute(context.Background(), args, failedWriter{}, &stderr); code != 1 {
			t.Fatalf("output failure returned %d", code)
		}
	}
}
