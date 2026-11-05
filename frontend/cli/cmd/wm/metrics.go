package main

import (
	"context"
	"fmt"
	"strconv"

	"wheelmachine/frontend/cli/api"
)

// What a machine used, from its points: the latest numbers (wm top), or step by step (wm metrics).

func (a *app) top(ctx context.Context, args []string) error {
	fs := a.flags("top", "wm top <machine>", "The latest numbers of a machine: its CPUs (a share of its own size), "+
		"its memory and the most it may hold, and its disk and network, over the last minute it ran. Its numbers are kept "+
		"for a day.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which machine"); err != nil {
		return err
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	m, err := c.Metrics(ctx, id, api.MetricsParams{Since: "3m"})
	if err != nil {
		return err
	}
	// the newest step that covers some running, with the newest memory
	var latest *api.MetricsPoint
	for i := len(m.Points) - 1; i >= 0 && latest == nil; i-- {
		if m.Points[i].CPUPercent != nil {
			latest = &m.Points[i]
		}
	}
	if latest == nil && len(m.Points) > 0 {
		latest = &m.Points[len(m.Points)-1]
	}
	if a.json {
		one := []api.MetricsPoint{}
		if latest != nil {
			one = append(one, *latest)
		}
		m.Points = one
		return a.printJSON(m)
	}
	if latest == nil {
		fmt.Fprintf(a.stdout, "No numbers for %s in the last minutes: a machine has numbers only while it runs.\n", id)
		return nil
	}
	d := a.detail()
	d.line("machine", id)
	d.line("minute", latest.At.UTC().Format("2006-01-02 15:04 UTC"))
	d.line("CPUs", share(latest.CPUPercent, m.CPUs))
	d.line("memory", ofLimit(latest.MemoryBytes, latest.MemoryLimitBytes))
	d.line("disk", pair(latest.DiskReadBps, "read", latest.DiskWriteBps, "written"))
	d.line("network", pair(latest.NetRxBps, "in", latest.NetTxBps, "out"))
	return d.flush()
}

func (a *app) metrics(ctx context.Context, args []string) error {
	fs := a.flags("metrics", "wm metrics <machine> [--since 1h] [--step 1m] [--json]", "What a machine used, step by "+
		"step: its CPUs (a share of its own size), memory, disk and network. A point a minute is kept, for up to "+
		"the last day: a step it was paused through has no point, and its numbers can start over at any time.")
	since := fs.String("since", "1h", "from when: how long ago (15m, 6h, 1d) or a time (2026-10-23T09:00:00Z)")
	step := fs.String("step", "", "how long each point covers: whole minutes, 1m to 1h (1m when left out)")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which machine"); err != nil {
		return err
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	m, err := c.Metrics(ctx, id, api.MetricsParams{Since: *since, Step: *step})
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(m)
	}
	if len(m.Points) == 0 {
		fmt.Fprintf(a.stdout, "No numbers for %s since then: a machine has numbers only while it runs, for up to a day.\n", id)
		return nil
	}
	t := a.table("TIME (UTC)", "CPU", "MEMORY", "DISK READ", "DISK WRITE", "NET IN", "NET OUT")
	day := m.Points[0].At.UTC().Format("2006-01-02") != m.Points[len(m.Points)-1].At.UTC().Format("2006-01-02")
	for _, p := range m.Points {
		at := p.At.UTC().Format("15:04")
		if day {
			at = p.At.UTC().Format("01-02 15:04")
		}
		t.row(at, percent(p.CPUPercent), ofLimit(p.MemoryBytes, p.MemoryLimitBytes), rate(p.DiskReadBps), rate(p.DiskWriteBps),
			rate(p.NetRxBps), rate(p.NetTxBps))
	}
	return t.flush()
}

func percent(p *float64) string {
	if p == nil {
		return "-"
	}
	return strconv.FormatFloat(*p, 'f', 1, 64) + "%"
}

// share is a machine's CPUs in use: 12.5% of 2.
func share(p, cpus *float64) string {
	if p == nil {
		return "-"
	}
	if cpus == nil {
		return percent(p)
	}
	return percent(p) + " of " + num(*cpus)
}

func ofLimit(used, limit *float64) string {
	switch {
	case used == nil:
		return "-"
	case limit == nil:
		return humanBytes(*used)
	}
	return humanBytes(*used) + " of " + humanBytes(*limit)
}

func rate(bps *float64) string {
	if bps == nil {
		return "-"
	}
	return humanBytes(*bps) + "/s"
}

func pair(a *float64, aName string, b *float64, bName string) string {
	if a == nil && b == nil {
		return "-"
	}
	return rate(a) + " " + aName + ", " + rate(b) + " " + bName
}
