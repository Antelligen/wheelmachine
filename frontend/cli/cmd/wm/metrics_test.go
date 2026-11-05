package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wheelmachine/frontend/cli/api"
)

func f(v float64) *float64 { return &v }

// Two minutes of a machine as the API gives them: one it ran through, and the first after it woke, which covers none.
func twoMinutes(at time.Time) []api.MetricsPoint {
	gib := float64(1 << 30)
	return []api.MetricsPoint{
		{At: at, CPUPercent: f(12.5), MemoryBytes: f(gib / 2), MemoryLimitBytes: f(4 * gib), DiskReadBps: f(1536), DiskWriteBps: f(300),
			NetRxBps: f(10240), NetTxBps: f(2048)},
		{At: at.Add(time.Minute), MemoryBytes: f(gib)},
	}
}

func TestTopPrintsTheLatestNumbers(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StateRunning)
	at := time.Date(2026, 10, 23, 12, 3, 0, 0, time.UTC)
	h.api.Metrics = map[string][]api.MetricsPoint{web.ID: twoMinutes(at)}
	r := h.wm("", "top", "web").must(t, 0)
	// the newest minute that covers some running, with its memory and rates
	for _, want := range []string{web.ID, "2026-10-23 12:03 UTC", "12.5% of 2", "512.0 MiB of 4.0 GiB", "1.5 KiB/s read, 300 B/s written",
		"10.0 KiB/s in, 2.0 KiB/s out"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("no %q in\n%s", want, r.stdout)
		}
	}
	asked, _ := h.api.Last("GET", "/v1/machines/"+web.ID+"/metrics")
	if asked.Query.Get("since") != "3m" {
		t.Fatalf("asked %v", asked.Query)
	}
	var got api.MachineMetrics
	if err := json.Unmarshal([]byte(h.wm("", "top", "web", "--json").must(t, 0).stdout), &got); err != nil || len(got.Points) != 1 || *got.Points[0].CPUPercent != 12.5 {
		t.Fatalf("%+v %v", got, err)
	}
	// a machine with no numbers yet says so, and is not an error
	idle := h.api.AddMachine("idle", api.StatePaused)
	if r := h.wm("", "top", idle.ID).must(t, 0); !strings.Contains(r.stdout, "No numbers for "+idle.ID) {
		t.Fatalf("%q", r.stdout)
	}
	h.wm("", "top").must(t, 2)
	h.wm("", "top", "nobody").must(t, 1)
}

func TestMetricsPrintsStepByStep(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StateRunning)
	at := time.Date(2026, 10, 23, 12, 3, 0, 0, time.UTC)
	h.api.Metrics = map[string][]api.MetricsPoint{web.ID: twoMinutes(at)}
	r := h.wm("", "metrics", "web", "--since", "6h", "--step", "5m").must(t, 0)
	rows := lines(r.stdout)
	if len(rows) != 3 || !strings.HasPrefix(rows[0], "TIME (UTC)") || !strings.HasPrefix(rows[1], "12:03") || !strings.Contains(rows[1], "12.5%") {
		t.Fatalf("%s", r.stdout)
	}
	if !strings.HasPrefix(rows[2], "12:04") || strings.Count(rows[2], "-") < 5 || !strings.Contains(rows[2], "1.0 GiB") {
		t.Fatalf("a step that covers none has no rates:\n%s", r.stdout)
	}
	asked, _ := h.api.Last("GET", "/v1/machines/"+web.ID+"/metrics")
	if asked.Query.Get("since") != "6h" || asked.Query.Get("step") != "5m" {
		t.Fatalf("asked %v", asked.Query)
	}
	var got api.MachineMetrics
	if err := json.Unmarshal([]byte(h.wm("", "metrics", web.ID, "--json").must(t, 0).stdout), &got); err != nil || len(got.Points) != 2 || got.StepS != 60 {
		t.Fatalf("%+v %v", got, err)
	}
	if asked, _ := h.api.Last("GET", "/v1/machines/"+web.ID+"/metrics"); asked.Query.Get("since") != "1h" {
		t.Fatalf("the last hour when not said: %v", asked.Query)
	}
	if r := h.wm("", "metrics", "web", "--step", "45s").must(t, 1); !strings.Contains(r.stderr, "step is whole minutes") {
		t.Fatalf("%q", r.stderr)
	}
	if r := h.wm("", "-h").must(t, 0); !strings.Contains(r.stdout, "top        a machine's latest numbers") {
		t.Fatalf("%s", r.stdout)
	}
}
