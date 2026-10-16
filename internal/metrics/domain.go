package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/fernandezvara/backd/internal/changefeed"
)

// domain holds the collectors of accounts, functions and jobs. Realm, database
// and function names come from the configuration, and every other label from
// a closed list in the code.
type domain struct {
	sessionsStarted *prometheus.CounterVec
	sessionsEnded   *prometheus.CounterVec
	rateLimited     *prometheus.CounterVec
	idConflicts     *prometheus.CounterVec
	aggregations    *prometheus.CounterVec
	aggregateTime   *prometheus.HistogramVec

	fnCalls    *prometheus.CounterVec
	fnTime     *prometheus.HistogramVec
	fnRefusals *prometheus.CounterVec
	fnReplays  *prometheus.CounterVec
	execErrors *prometheus.CounterVec

	jobsDone, jobsRetried, jobsExpired    *prometheus.CounterVec
	refreshErrors                         prometheus.Counter
	egress                                *prometheus.CounterVec
	execRuns                              *prometheus.CounterVec
	execTime                              prometheus.Histogram
	execRunning                           prometheus.Gauge
	jobs                                  *jobSnapshot
	pushUsers, pushDeliveries, pushPruned *prometheus.CounterVec

	feedOpen, feedWatched, feedLag *prometheus.GaugeVec
	feedReopened, feedEvents       *prometheus.CounterVec

	rtStreams                                                    *prometheus.GaugeVec
	rtEvents, rtResets, rtRefused, rtChecks                      *prometheus.CounterVec
	trigEvents, trigEnqueued, trigDeduped, trigGaps, trigDropped *prometheus.CounterVec
	trigBacklog                                                  *prometheus.GaugeVec
	trigHandoffs                                                 prometheus.Counter
	trigLag, trigWatermark, trigLease, trigEpoch                 prometheus.Gauge
}

func newDomain() domain {
	counter := func(name, help string, labels ...string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: name, Help: help}, labels)
	}
	gauge := func(name, help string, labels ...string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: Namespace, Name: name, Help: help}, labels)
	}
	snap := &jobSnapshot{stats: map[string][]JobStat{}, needsAttention: map[string]int64{}, serialWaiting: map[string]int64{}}
	return domain{
		jobs:            snap,
		rtStreams:       gauge("realtime_streams", "Realtime streams open on this instance, by realm.", "realm"),
		rtEvents:        counter("realtime_events_total", "Events sent to realtime streams, by realm and event (created, updated, removed).", "realm", "event"),
		rtResets:        counter("realtime_resets_total", "Resets sent to realtime streams, by realm and reason: history_expired, access_changed, slow_consumer, preimage_missing or config_changed.", "realm", "reason"),
		rtChecks:        counter("realtime_subscription_checks_total", "Subscriptions a change was evaluated against, by realm and kind: indexed (found by document id or an equality key) or scanned (no key, asked on every change). A growing scanned share means subscriptions without an equality on a grouping field.", "realm", "kind"),
		rtRefused:       counter("realtime_refused_total", "Streams refused at open, by realm and reason: instance, user or rate.", "realm", "reason"),
		trigEvents:      counter("trigger_events_total", "Changes a trigger considered, by realm, collection, trigger and outcome: matched (a job was made), no_match (its condition was false), missing_preimage (the image its condition needs was not there) or error.", "realm", "collection", "trigger", "outcome"),
		trigEnqueued:    counter("trigger_jobs_enqueued_total", "Jobs the trigger dispatcher queued, by realm.", "realm"),
		trigDeduped:     counter("trigger_jobs_deduplicated_total", "Jobs the queue collapsed because the same change had been queued before (a replay), by realm.", "realm"),
		trigGaps:        counter("trigger_gaps_total", "Stretches of changes the dispatcher recorded as gaps, by realm and reason (window, preimage_missing).", "realm", "reason"),
		trigDropped:     counter("trigger_dropped_total", "Events that matched a trigger and were not queued, by realm, collection, trigger and reason: chain (deeper than max_chain) or burst (a chained loop above max_chained_per_minute).", "realm", "collection", "trigger", "reason"),
		trigBacklog:     gauge("trigger_backlog", "Unfinished (queued, waiting or running) trigger jobs of a realm, as the dispatcher last counted them; its ceiling is triggers.max_backlog.", "realm"),
		trigHandoffs:    prometheus.NewCounter(prometheus.CounterOpts{Namespace: Namespace, Name: "trigger_lease_acquired_total", Help: "Times this instance took the trigger dispatcher's lease."}),
		trigLag:         prometheus.NewGauge(prometheus.GaugeOpts{Namespace: Namespace, Name: "trigger_dispatch_lag_seconds", Help: "Seconds from the newest change a batch carried to its jobs being queued."}),
		trigWatermark:   prometheus.NewGauge(prometheus.GaugeOpts{Namespace: Namespace, Name: "trigger_watermark_timestamp_seconds", Help: "When this instance last stored the dispatcher's position; its age is what to alert on."}),
		trigLease:       prometheus.NewGauge(prometheus.GaugeOpts{Namespace: Namespace, Name: "trigger_lease_held", Help: "1 while this instance holds the trigger dispatcher's lease."}),
		trigEpoch:       prometheus.NewGauge(prometheus.GaugeOpts{Namespace: Namespace, Name: "trigger_lease_epoch", Help: "The epoch of the lease this instance last held."}),
		feedOpen:        gauge("changefeed_open", "1 while a change-stream consumer (realtime, dispatcher) has its stream open, 0 while it is idle or closed.", "consumer"),
		feedWatched:     gauge("changefeed_watched_namespaces", "Collections a consumer's open stream watches.", "consumer"),
		feedLag:         gauge("changefeed_lag_seconds", "Seconds between the cluster time of the last change a consumer read and reading it.", "consumer"),
		feedReopened:    counter("changefeed_reopens_total", "Times a consumer's stream was opened again, by reason: set_changed (a reload changed what is watched), error, or history_lost (the history no longer reached its position).", "consumer", "reason"),
		feedEvents:      counter("changefeed_events_total", "Changes a consumer read, by operation (created, updated, removed) and source (app collection or system).", "consumer", "op", "source"),
		pushUsers:       counter("push_users_total", "Users a push send reached the planning of, by realm and outcome: queued (handed to delivery), limited (over their per-user limit) or no_devices.", "realm", "outcome"),
		pushDeliveries:  counter("push_deliveries_total", "Devices a delivery unit answered for, by realm and status: sent, invalid (removed as dead) or failed.", "realm", "status"),
		pushPruned:      counter("push_devices_pruned_total", "Devices removed from the registry, by realm and reason: invalid (the provider said the token is dead), stale (not seen for stale_after), session (their session ended unnoticed), cap (above max_per_user), moved (a token another user registered) or rotated (every web device, when the VAPID key was rotated).", "realm", "reason"),
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
		idConflicts: counter("id_conflicts_total",
			"Creates of a document with a custom id that already exists (409 id_taken), by realm, database and collection.", "realm", "database", "collection"),
		aggregations: counter("aggregations_total",
			"Aggregations asked of a collection, by realm, database, collection and outcome: ok, too_expensive (422), invalid (400) or error.", "realm", "database", "collection", "outcome"),
		aggregateTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "aggregation_duration_seconds",
			Help: "How long aggregations that reached the database took, by realm, database and collection.", Buckets: DurationBuckets,
		}, []string{"realm", "database", "collection"}),
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
	return []prometheus.Collector{newJobsCollector(d.jobs), d.jobsDone, d.jobsRetried, d.jobsExpired, d.refreshErrors, d.egress, d.execRuns, d.execTime, d.execRunning, d.sessionsStarted, d.sessionsEnded, d.rateLimited, d.idConflicts, d.aggregations, d.aggregateTime, d.fnCalls, d.fnTime, d.fnRefusals, d.fnReplays, d.execErrors, d.pushUsers, d.pushDeliveries, d.pushPruned, d.rtStreams, d.rtEvents, d.rtResets, d.rtRefused, d.rtChecks, d.feedOpen, d.feedWatched, d.feedLag, d.feedReopened, d.feedEvents, d.trigEvents, d.trigEnqueued, d.trigDeduped, d.trigGaps, d.trigDropped, d.trigBacklog, d.trigHandoffs, d.trigLag, d.trigWatermark, d.trigLease, d.trigEpoch}
}

// PushUsers counts n users by what planning a push did with them.
func (m *Metrics) PushUsers(realm, outcome string, n int) {
	if m != nil && n > 0 {
		m.pushUsers.WithLabelValues(realm, outcome).Add(float64(n))
	}
}

// PushDeliveries counts n devices by the status a delivery unit gave them.
func (m *Metrics) PushDeliveries(realm, status string, n int) {
	if m != nil && n > 0 {
		m.pushDeliveries.WithLabelValues(realm, status).Add(float64(n))
	}
}

// PushPruned counts n devices removed from the registry for a reason.
func (m *Metrics) PushPruned(realm, reason string, n int64) {
	if m != nil && n > 0 {
		m.pushPruned.WithLabelValues(realm, reason).Add(float64(n))
	}
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

// DuplicateID counts a create that asked for an id that exists.
func (m *Metrics) DuplicateID(realm, database, collection string) {
	if m != nil {
		m.idConflicts.WithLabelValues(realm, database, collection).Inc()
	}
}

// AggregateRan records an aggregation: its outcome and, when it reached the database, how long
// it took.
func (m *Metrics) AggregateRan(realm, database, collection, outcome string, took time.Duration) {
	if m == nil {
		return
	}
	m.aggregations.WithLabelValues(realm, database, collection, outcome).Inc()
	if took > 0 {
		m.aggregateTime.WithLabelValues(realm, database, collection).Observe(took.Seconds())
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

// FeedState records whether a change-stream consumer has its stream open and how many
// collections it watches.
func (m *Metrics) FeedState(consumer string, open bool, watched int) {
	if m == nil {
		return
	}
	v := 0.0
	if open {
		v = 1
	}
	m.feedOpen.WithLabelValues(consumer).Set(v)
	m.feedWatched.WithLabelValues(consumer).Set(float64(watched))
}

// FeedReopened counts a stream opened again.
func (m *Metrics) FeedReopened(consumer, reason string) {
	if m != nil {
		m.feedReopened.WithLabelValues(consumer, reason).Inc()
	}
}

// FeedEvent counts a change a consumer read.
func (m *Metrics) FeedEvent(consumer string, op changefeed.Op, system bool) {
	if m == nil {
		return
	}
	source := "app"
	if system {
		source = "system"
	}
	m.feedEvents.WithLabelValues(consumer, string(op), source).Inc()
}

// FeedLag records how far behind the cluster a consumer is reading.
func (m *Metrics) FeedLag(consumer string, lag time.Duration) {
	if m != nil {
		m.feedLag.WithLabelValues(consumer).Set(lag.Seconds())
	}
}

// TriggerObserver is the dispatcher's view of the metrics (triggers.Observer). A nil one
// does nothing.
type TriggerObserver struct{ m *Metrics }

// TriggerObserver returns the observer for the trigger dispatcher; nil when metrics are off.
func (m *Metrics) TriggerObserver() *TriggerObserver {
	if m == nil {
		return nil
	}
	return &TriggerObserver{m}
}

// Event counts a change a trigger considered.
func (o *TriggerObserver) Event(realm, collection, trigger, outcome string) {
	if o != nil {
		o.m.trigEvents.WithLabelValues(realm, collection, trigger, outcome).Inc()
	}
}

// Enqueued counts the jobs queued and the replays collapsed.
func (o *TriggerObserver) Enqueued(realm string, created, duplicates int) {
	if o == nil {
		return
	}
	if created > 0 {
		o.m.trigEnqueued.WithLabelValues(realm).Add(float64(created))
	}
	if duplicates > 0 {
		o.m.trigDeduped.WithLabelValues(realm).Add(float64(duplicates))
	}
}

// Lag records the time from a change to its jobs being queued.
func (o *TriggerObserver) Lag(d time.Duration) {
	if o != nil {
		o.m.trigLag.Set(d.Seconds())
	}
}

// Watermark records when the position was last stored.
func (o *TriggerObserver) Watermark(at time.Time) {
	if o != nil {
		o.m.trigWatermark.Set(float64(at.Unix()))
	}
}

// Lease records whether this instance holds the lease.
func (o *TriggerObserver) Lease(held bool, epoch int64) {
	if o == nil {
		return
	}
	v := 0.0
	if held {
		v = 1
		o.m.trigHandoffs.Inc()
	}
	o.m.trigLease.Set(v)
	o.m.trigEpoch.Set(float64(epoch))
}

// Dropped counts an event that matched and was not queued.
func (o *TriggerObserver) Dropped(realm, collection, trigger, reason string) {
	if o != nil {
		o.m.trigDropped.WithLabelValues(realm, collection, trigger, reason).Inc()
	}
}

// Backlog records a realm's unfinished trigger jobs.
func (o *TriggerObserver) Backlog(realm string, n int) {
	if o != nil {
		o.m.trigBacklog.WithLabelValues(realm).Set(float64(n))
	}
}

// Gap counts a gap recorded.
func (o *TriggerObserver) Gap(realm, reason string) {
	if o != nil {
		o.m.trigGaps.WithLabelValues(realm, reason).Inc()
	}
}

// RealtimeObserver reports realtime streams.
type RealtimeObserver struct{ m *Metrics }

// Realtime returns the observer for the realtime hub; nil on a nil Metrics.
func (m *Metrics) Realtime() *RealtimeObserver {
	if m == nil {
		return nil
	}
	return &RealtimeObserver{m}
}

func (o *RealtimeObserver) StreamOpened(realm string) {
	if o != nil {
		o.m.rtStreams.WithLabelValues(realm).Inc()
	}
}

func (o *RealtimeObserver) StreamClosed(realm string) {
	if o != nil {
		o.m.rtStreams.WithLabelValues(realm).Dec()
	}
}

func (o *RealtimeObserver) Event(realm, event string) {
	if o != nil {
		o.m.rtEvents.WithLabelValues(realm, event).Inc()
	}
}

func (o *RealtimeObserver) Reset(realm, reason string) {
	if o != nil {
		o.m.rtResets.WithLabelValues(realm, reason).Inc()
	}
}

func (o *RealtimeObserver) Refused(realm, reason string) {
	if o != nil {
		o.m.rtRefused.WithLabelValues(realm, reason).Inc()
	}
}

func (o *RealtimeObserver) Checks(realm, kind string, n int) {
	if o != nil && n > 0 {
		o.m.rtChecks.WithLabelValues(realm, kind).Add(float64(n))
	}
}
