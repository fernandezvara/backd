package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// JobStat is the queue of one kind of job in one realm.
type JobStat struct {
	Kind string // function, schedule, email, erase or check
	// Queued jobs wait for a worker, Running ones hold a lease, Waiting ones
	// wait out the delay of a retry.
	Queued, Running, Waiting int64
	// OldestQueued is when the longest-waiting queued job was created; zero
	// without queued jobs.
	OldestQueued time.Time
}

// JobKind names a job's kind from what it holds: the label values are this
// closed list.
func JobKind(email, erase, check, scheduled bool) string {
	switch {
	case email:
		return "email"
	case erase:
		return "erase"
	case check:
		return "check"
	case scheduled:
		return "schedule"
	}
	return "function"
}

// jobSnapshot is what the last refresh found. The gauges are read from it at
// scrape time, so a scrape never queries the database.
type jobSnapshot struct {
	mu             sync.RWMutex
	stats          map[string][]JobStat // by realm
	needsAttention map[string]int64     // erase jobs that failed, by realm
	refreshed      time.Time
}

type jobsCollector struct {
	snap                                 *jobSnapshot
	jobs, oldest, attention, refreshedAt *prometheus.Desc
}

func newJobsCollector(snap *jobSnapshot) *jobsCollector {
	d := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName(Namespace, "", name), help, labels, nil)
	}
	return &jobsCollector{
		snap:        snap,
		jobs:        d("jobs", "Jobs not done yet, by realm, kind and state (queued, running or waiting for a retry). Refreshed every 15 seconds, not on scrape.", "realm", "kind", "state"),
		oldest:      d("jobs_oldest_wait_seconds", "Age of the longest-waiting queued job, by realm and kind (as of now, from the last refresh).", "realm", "kind"),
		attention:   d("erase_needs_attention", "Erase jobs that used up their attempts and need an administrator, by realm.", "realm"),
		refreshedAt: d("jobs_refreshed_timestamp_seconds", "When the job gauges were last refreshed (0: never)."),
	}
}

func (c *jobsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.jobs
	ch <- c.oldest
	ch <- c.attention
	ch <- c.refreshedAt
}

func (c *jobsCollector) Collect(ch chan<- prometheus.Metric) {
	c.snap.mu.RLock()
	defer c.snap.mu.RUnlock()
	now := time.Now()
	for realm, stats := range c.snap.stats {
		for _, s := range stats {
			ch <- prometheus.MustNewConstMetric(c.jobs, prometheus.GaugeValue, float64(s.Queued), realm, s.Kind, "queued")
			ch <- prometheus.MustNewConstMetric(c.jobs, prometheus.GaugeValue, float64(s.Running), realm, s.Kind, "running")
			ch <- prometheus.MustNewConstMetric(c.jobs, prometheus.GaugeValue, float64(s.Waiting), realm, s.Kind, "waiting")
			var wait float64
			if !s.OldestQueued.IsZero() {
				wait = max(now.Sub(s.OldestQueued).Seconds(), 0)
			}
			ch <- prometheus.MustNewConstMetric(c.oldest, prometheus.GaugeValue, wait, realm, s.Kind)
		}
	}
	for realm, n := range c.snap.needsAttention {
		ch <- prometheus.MustNewConstMetric(c.attention, prometheus.GaugeValue, float64(n), realm)
	}
	var at float64
	if !c.snap.refreshed.IsZero() {
		at = float64(c.snap.refreshed.Unix())
	}
	ch <- prometheus.MustNewConstMetric(c.refreshedAt, prometheus.GaugeValue, at)
}

// SetJobs replaces what the job gauges report for realm.
func (m *Metrics) SetJobs(realm string, stats []JobStat, needsAttention int64) {
	if m == nil {
		return
	}
	// Every kind is always reported, so a queue that empties reads 0 rather
	// than disappearing.
	have := map[string]bool{}
	for _, s := range stats {
		have[s.Kind] = true
	}
	for _, k := range []string{"function", "schedule", "email", "erase", "check"} {
		if !have[k] {
			stats = append(stats, JobStat{Kind: k})
		}
	}
	m.jobs.mu.Lock()
	defer m.jobs.mu.Unlock()
	m.jobs.stats[realm] = stats
	m.jobs.needsAttention[realm] = needsAttention
	m.jobs.refreshed = time.Now()
}

// JobFinished counts a job a worker finished, by kind and the result's status.
func (m *Metrics) JobFinished(realm, kind, status string) {
	if m != nil {
		m.jobsDone.WithLabelValues(realm, kind, status).Inc()
	}
}

// JobRetried counts a failed attempt that was queued again.
func (m *Metrics) JobRetried(realm, kind string) {
	if m != nil {
		m.jobsRetried.WithLabelValues(realm, kind).Inc()
	}
}

// JobLeaseExpired counts a job claimed again after its worker's lease ran out.
func (m *Metrics) JobLeaseExpired(realm, kind string) {
	if m != nil {
		m.jobsExpired.WithLabelValues(realm, kind).Inc()
	}
}

// WatchJobs refreshes the job gauges every interval until ctx ends. refresh
// reads the realms' queues (one realm failing must not hide the others: it
// returns what it could read). Errors are counted, and the last values stay.
func (m *Metrics) WatchJobs(ctx context.Context, interval time.Duration, refresh func(context.Context) (map[string]RealmJobs, error)) {
	if m == nil {
		return
	}
	run := func() {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		got, err := refresh(rctx)
		for realm, r := range got {
			m.SetJobs(realm, r.Stats, r.NeedsAttention)
		}
		if err != nil && ctx.Err() == nil {
			m.refreshErrors.Inc()
		}
	}
	run()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// RealmJobs is what one realm's refresh found.
type RealmJobs struct {
	Stats          []JobStat
	NeedsAttention int64
}

// JobSource is a store that can summarize its job queue; the MongoDB auth
// store is one.
type JobSource interface {
	JobStats(ctx context.Context, now time.Time) ([]JobStat, error)
	EraseNeedsAttention(ctx context.Context) (int64, error)
}
