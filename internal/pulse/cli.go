package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"time"
)

const usage = `NotifyHub Pulse · Go traffic simulator

Usage: pulse run [flags]

Scenarios: normal (60% email / 20% SMS / 20% push), campaign (90% email),
           duplicates (repeated payment reminders with small wording changes).

Examples:
  pulse run --scenario normal --rate 5 --duration 30s
  pulse run --scenario campaign --rate 30 --duration 10s
  pulse run --scenario duplicates --count 100
  pulse run --scenario duplicates --count 3 --dry-run

Run "pulse run --help" for flags. Traffic goes to http://localhost:8080 by default.
`

// output remembers the first write failure without cluttering the CLI flow.
type output struct {
	writer io.Writer
	err    error
}

func (o *output) printf(format string, args ...any) {
	if o.err == nil {
		_, o.err = fmt.Fprintf(o.writer, format, args...)
	}
}

// Execute returns 0 for success, 1 for request/run failures, 2 for invalid
// arguments, and 130 for cancellation.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	out, diagnostic := output{writer: stdout}, output{writer: stderr}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		out.printf("%s", usage)
		if out.err != nil {
			return 1
		}
		return 0
	}
	if args[0] != "run" {
		diagnostic.printf("unknown command %q\n%s", args[0], usage)
		return 2
	}
	flags := flag.NewFlagSet("pulse run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("scenario", "normal", "normal, campaign, or duplicates")
	baseURL := flags.String("base-url", "http://localhost:8080", "NotifyHub base URL")
	rate := flags.Float64("rate", 5, "total requests per second across all workers (duplicates defaults to 30)")
	duration := flags.Duration("duration", 30*time.Second, "submission window; mutually exclusive with --count")
	count := flags.Int("count", 0, "number of requests (duplicates defaults to 100)")
	workers := flags.Int("workers", 4, "maximum concurrent HTTP requests")
	timeout := flags.Duration("timeout", 5*time.Second, "timeout per HTTP request")
	dryRun := flags.Bool("dry-run", false, "print planned payloads as NDJSON immediately without HTTP requests")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	invalid := func(err error) int { diagnostic.printf("%v\n", err); return 2 }
	if flags.NArg() != 0 {
		return invalid(fmt.Errorf("unexpected positional arguments: %v", flags.Args()))
	}
	explicit := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if explicit["count"] && explicit["duration"] {
		return invalid(fmt.Errorf("--count and --duration are mutually exclusive"))
	}
	if explicit["count"] && *count <= 0 {
		return invalid(fmt.Errorf("count must be positive"))
	}
	if *name == "duplicates" {
		if !explicit["rate"] {
			*rate = 30
		}
		if !explicit["count"] && !explicit["duration"] {
			*count = 100
		}
	}
	if *count > 0 {
		*duration = 0
	}
	config := Config{Rate: *rate, Duration: *duration, Count: *count, Workers: *workers}
	if err := config.Validate(); err != nil {
		return invalid(err)
	}
	scenario, err := NewScenario(*name)
	if err != nil {
		return invalid(err)
	}
	client, err := NewClient(*baseURL, *timeout)
	if err != nil {
		return invalid(err)
	}
	defer client.Close()
	if *dryRun {
		planned := *count
		if planned == 0 {
			value := math.Ceil(duration.Seconds() * *rate)
			if value >= float64(int(^uint(0)>>1)) {
				return invalid(fmt.Errorf("dry-run has too many payloads; use --count"))
			}
			planned = int(value)
		}
		encoder := json.NewEncoder(stdout)
		for index := 0; index < planned; index++ {
			if ctx.Err() != nil {
				return 130
			}
			if err := encoder.Encode(scenario.Notification(index)); err != nil {
				diagnostic.printf("write preview: %v\n", err)
				return 1
			}
		}
		return 0
	}
	diagnostic.printf("Pulse · %s → %s · %.2f req/s · %d workers\n", *name, *baseURL, *rate, *workers)
	summary, err := Run(ctx, config, scenario, client)
	out.printf("Completed in %s · attempted %d · accepted %d · failed %d · canceled %d\n",
		summary.Elapsed.Round(time.Millisecond), summary.Total(), summary.Accepted, summary.Failed, summary.Canceled)
	for _, channel := range summary.ChannelNames() {
		c := summary.Channels[channel]
		out.printf("  %-5s  accepted %d · failed %d · canceled %d\n", channel, c.Accepted, c.Failed, c.Canceled)
	}
	if summary.Total() > 0 {
		out.printf("HTTP latency · mean %s · max %s\n", (summary.Latency / time.Duration(summary.Total())).Round(time.Millisecond), summary.MaxLatency.Round(time.Millisecond))
	}
	for _, message := range summary.Errors {
		diagnostic.printf("%s\n", message)
	}
	if err != nil {
		diagnostic.printf("Run stopped: %v\n", err)
		return 130
	}
	if summary.Failed > 0 {
		return 1
	}
	out.printf("Refresh Insights using Java history to load these notifications.\n")
	if out.err != nil {
		diagnostic.printf("write summary: %v\n", out.err)
		return 1
	}
	return 0
}
