package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// domain holds the collectors of accounts, functions and jobs. Realm, database
// and function names come from the configuration, and every other label from
// a closed list in the code.
type domain struct {
	sessionsStarted *prometheus.CounterVec
	sessionsEnded   *prometheus.CounterVec
	rateLimited     *prometheus.CounterVec

	fnCalls    *prometheus.CounterVec
	fnTime     *prometheus.HistogramVec
	fnRefusals *prometheus.CounterVec
	fnReplays  *prometheus.CounterVec
	execErrors *prometheus.CounterVec

	jobsDone, jobsRetried, jobsExpired *prometheus.CounterVec
	refreshErrors                      prometheus.Counter
	egress                             *prometheus.CounterVec
	execRuns                           *prometheus.CounterVec
	execTime                           prometheus.Histogram
	execRunning                        prometheus.Gauge
	jobs                               *jobSnapshot
}

func newDomain() domain {
	counter := func(name, help string, labels ...string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: name, Help: help}, labels)
	}
	snap := &jobSnapshot{stats: map[string][]JobStat{}, needsAttention: map[string]int64{}}
	return domain{
		jobs:            snap,
		jobsDone:        counter("jobs_completed_total", "Jobs a worker finished, by realm, kind (function, schedule, email, erase) and the result's status. Email and erase outcomes are here: kind email or erase.", "realm", "kind", "status"),
		egress:          counter("egress_requests_total", "Requests to the egress proxy, by decision: allowed, denied_credentials, denied_host or denied_address.", "outcome"),
		execRuns:        counter("executor_runs_total", "Function runs the executor handled, by the status they ended with (busy: refused at its process limit).", "status"),
		execTime:        prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: Namespace, Name: "executor_run_duration_seconds", Help: "How long function runs took in the executor.", Buckets: DurationBuckets}),
		execRunning:     prometheus.NewGauge(prometheus.GaugeOpts{Namespace: Namespace, Name: "executor_running", Help: "Function processes running now."}),
		jobsRetried:     counter("jobs_retried_total", "Failed attempts queued again, by realm and kind.", "realm", "kind"),
		jobsExpired:     counter("jobs_expired_leases_total", "Jobs claimed again because the worker holding them stopped answering, by realm and kind.", "realm", "kind"),
		refreshErrors:   prometheus.NewCounter(prometheus.CounterOpts{Namespace: Namespace, Name: "metrics_refresh_errors_total", Help: "Times refreshing the job gauges failed (the last values stay)."}),
		sessionsStarted: counter("sessions_created_total", "Sessions started (sign-up and login), by realm.", "realm"),
		sessionsEnded: counter("sessions_ended_total",
			"Sessions ended on purpose, by realm and reason (logout, logout_all, revoked, password_changed, password_reset, password_set, email_changed, email_reverted, disabled, deactivated, erased, deleted). Sessions that expire are not counted.",
			"realm", "reason"),
		rateLimited: counter("rate_limited_total",
			"Times a limit refused or delayed someone: login (the throttle), function (a function's rate_limit) and email_<scope> (the email limits).", "realm", "scope"),
		fnCalls: counter("function_invocations_total",
			"Function runs that ended, by realm, function, mode and the executor's terminal status.", "realm", "function", "mode", "status"),
		fnTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "function_duration_seconds",
			Help: "How long function runs took, by realm and function.", Buckets: DurationBuckets,
		}, []string{"realm", "function"}),
		fnRefusals: counter("function_refusals_total",
			"Calls refused before running, by reason: rate_limit, concurrency, idempotency_conflict, idempotency_reused.", "realm", "function", "reason"),
		fnReplays: counter("function_idempotent_replays_total",
			"Calls answered from a stored result because their Idempotency-Key was used before.", "realm", "function"),
		execErrors: counter("executor_errors_total",
			"Calls that failed because the executor couldn't be used, by kind (unavailable).", "kind"),
	}
}

func (d domain) collectors() []prometheus.Collector {
	return []prometheus.Collector{newJobsCollector(d.jobs), d.jobsDone, d.jobsRetried, d.jobsExpired, d.refreshErrors, d.egress, d.execRuns, d.execTime, d.execRunning, d.sessionsStarted, d.sessionsEnded, d.rateLimited, d.fnCalls, d.fnTime, d.fnRefusals, d.fnReplays, d.execErrors}
}

// SessionStarted counts a new session.
func (m *Metrics) SessionStarted(realm string) {
	if m != nil {
		m.sessionsStarted.WithLabelValues(realm).Inc()
	}
}

// SessionsEnded counts n sessions ended for a reason.
func (m *Metrics) SessionsEnded(realm, reason string, n int64) {
	if m != nil && n > 0 {
		m.sessionsEnded.WithLabelValues(realm, reason).Add(float64(n))
	}
}

// RateLimited counts a refusal by a limit.
func (m *Metrics) RateLimited(realm, scope string) {
	if m != nil {
		m.rateLimited.WithLabelValues(realm, scope).Inc()
	}
}

// FunctionRan records a function run that ended: mode is sync, async or
// webhook; status is the executor's terminal status.
func (m *Metrics) FunctionRan(realm, function, mode, status string, took time.Duration) {
	if m == nil {
		return
	}
	m.fnCalls.WithLabelValues(realm, function, mode, status).Inc()
	m.fnTime.WithLabelValues(realm, function).Observe(took.Seconds())
}

// FunctionRefused records a call refused before it ran.
func (m *Metrics) FunctionRefused(realm, function, reason string) {
	if m != nil {
		m.fnRefusals.WithLabelValues(realm, function, reason).Inc()
	}
}

// FunctionReplayed records a call answered from a stored idempotent result.
func (m *Metrics) FunctionReplayed(realm, function string) {
	if m != nil {
		m.fnReplays.WithLabelValues(realm, function).Inc()
	}
}

// ExecutorError records a call that failed because the executor couldn't be used.
func (m *Metrics) ExecutorError(kind string) {
	if m != nil {
		m.execErrors.WithLabelValues(kind).Inc()
	}
}

// EgressRequest counts a request to the egress proxy by what it decided:
// allowed, denied_credentials, denied_host or denied_address. Host names are
// never labels.
func (m *Metrics) EgressRequest(outcome string) {
	if m != nil {
		m.egress.WithLabelValues(outcome).Inc()
	}
}

// ExecutorStarted and ExecutorFinished bracket one function process.
func (m *Metrics) ExecutorStarted() {
	if m != nil {
		m.execRunning.Inc()
	}
}

// ExecutorFinished records a function process that ended (or a call the
// executor refused as busy), with the status it ended with.
func (m *Metrics) ExecutorFinished(status string, took time.Duration, ran bool) {
	if m == nil {
		return
	}
	if ran {
		m.execRunning.Dec()
	}
	m.execRuns.WithLabelValues(status).Inc()
	m.execTime.Observe(took.Seconds())
}
