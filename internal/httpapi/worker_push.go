package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/push"
	"github.com/fernandezvara/backd/internal/registry"
)

// planAttempts is how many times a planner or a batch is tried before its job ends as failed.
const planAttempts = 5

// runPush runs one job of a push send: planning an audience into batches, planning a batch
// into delivery units, or delivering a unit. None is a function: the worker does it itself.
func (w *Worker) runPush(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) {
	if svc.Settings.Push == nil {
		w.fail(ctx, log, svc, job, "the realm no longer has a push section")
		return
	}
	switch job.Push.Kind {
	case auth.PushPlan:
		w.finishPlanning(ctx, log, svc, job, svc.PlanPushAudience(ctx, job))
	case auth.PushBatch:
		_, err := svc.PlanPushBatch(ctx, job.ID, job.Push.SendID, job.Push.Users, job.Push.Messages, job.RequestID)
		w.finishPlanning(ctx, log, svc, job, err)
	case auth.PushDeliver:
		w.deliverUnit(ctx, log, svc, job)
	default:
		w.fail(ctx, log, svc, job, "unknown push job")
	}
}

// finishPlanning ends a planner or batch job, or tries it again after a growing wait.
func (w *Worker) finishPlanning(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, err error) {
	if err == nil {
		if cerr := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: "ok"}); cerr != nil {
			log.Error("complete the push job", "error", cerr)
			return
		}
		w.fns.metrics.JobFinished(svc.Realm, "push", "ok")
		return
	}
	if failed := job.Failures + 1; failed < planAttempts {
		wait := min(10*time.Second<<(failed-1), 10*time.Minute)
		log.Warn("planning a push failed; it will be tried again", "attempt", failed, "of", planAttempts, "retry_in", wait.String(), "error", err)
		if rerr := svc.RetryJob(ctx, job.ID, wait); rerr != nil {
			log.Error("queue the retry", "error", rerr)
		}
		return
	}
	log.Error("planning a push failed for good", "error", err)
	if cerr := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusError, Message: err.Error()}); cerr != nil {
		log.Error("complete the push job", "error", cerr)
	}
	// The send can't finish its batches: close its books so a waiting caller isn't left hanging.
	if job.Push.Kind == auth.PushBatch {
		_, _ = svc.Store.AddPushSend(ctx, job.Push.SendID, auth.PushDelta{DoneBatch: job.ID}, time.Now())
	}
}

// deliverUnit delivers a unit to its devices through the senders of their platforms and applies
// what each device answered: sent, removed as dead, tried again, or failed.
func (w *Worker) deliverUnit(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job) {
	pj := job.Push
	settings := svc.Settings.Push
	ids := make([]string, 0, len(pj.Recipients))
	for _, r := range pj.Recipients {
		ids = append(ids, r.Devices...)
	}
	found, err := svc.Store.DevicesByIDs(ctx, ids)
	if err != nil {
		w.retryUnit(ctx, log, svc, job, nil, err)
		return
	}
	devices := make(map[string]auth.Device, len(found))
	for _, d := range found {
		devices[d.ID] = d
	}
	// A unit can wait out a retry backoff for hours: a device whose session ended meanwhile (it
	// expired, and the sweep hasn't removed the device yet) gets nothing.
	userIDs := make([]string, len(pj.Recipients))
	for i, r := range pj.Recipients {
		userIDs[i] = r.UserID
	}
	live, err := svc.Store.LiveDevicesOf(ctx, userIDs, svc.Clock())
	if err != nil {
		w.retryUnit(ctx, log, svc, job, nil, err)
		return
	}
	liveIDs := map[string]bool{}
	for _, list := range live {
		for _, d := range list {
			liveIDs[d.ID] = true
		}
	}
	// What this attempt still owes: every device, until something answers for it.
	pending := make(map[string]bool, len(ids))
	var delta auth.PushDelta
	byPlatform := map[string][]push.Delivery{}
	for _, r := range pj.Recipients {
		for _, id := range r.Devices {
			d, ok := devices[id]
			if !ok {
				delta.Invalid++ // removed since the unit was made: signed out, or reported dead
				continue
			}
			if !liveIDs[id] {
				delta.Failed++ // its session ended: nothing is sent to a signed-out device
				continue
			}
			token, err := svc.OpenToken(d.Token)
			if err != nil {
				delta.Failed++
				log.Error("a device's token could not be opened", "device", id, "error", err)
				continue
			}
			msg := pj.Messages[r.Locale]
			byPlatform[d.Platform] = append(byPlatform[d.Platform], push.Delivery{UserID: r.UserID, DeviceID: id, Token: token, Message: msg})
			pending[id] = true
		}
	}
	var wholesale error
	rctx, cancel := context.WithTimeout(ctx, settings.Delivery.Timeout)
	defer cancel()
	for _, platform := range registry.Platforms {
		list := byPlatform[platform]
		if len(list) == 0 {
			continue
		}
		pp := settings.Platforms[platform]
		sender := w.pushSenders[pp.Provider]
		if sender == nil {
			for _, d := range list {
				delta.Failed++
				delete(pending, d.DeviceID)
			}
			log.Warn("no sender for the push provider", "provider", pp.Provider, "platform", platform, "devices", len(list))
			continue
		}
		cfg, err := w.pushConfig(rctx, svc, platform, pp)
		if err != nil {
			wholesale = errors.Join(wholesale, err)
			continue
		}
		results, err := sender.Send(rctx, push.Request{Realm: svc.Realm, SendID: pj.SendID, Attempt: job.Failures + 1, Platform: platform, Provider: pp.Provider, Config: cfg, Parallelism: settings.Delivery.Parallelism, Deliveries: list})
		if err != nil {
			wholesale = errors.Join(wholesale, err)
			continue
		}
		answered := make(map[string]string, len(results))
		for _, r := range results {
			answered[r.DeviceID] = r.Status
		}
		for _, d := range list {
			switch answered[d.DeviceID] {
			case push.StatusSent:
				delta.Sent++
				delete(pending, d.DeviceID)
			case push.StatusInvalid:
				delta.Invalid++
				delete(pending, d.DeviceID)
				if err := svc.Store.DeleteDeviceByID(ctx, d.DeviceID); err != nil {
					log.Warn("could not remove a dead device", "error", err)
				}
			case push.StatusRetry:
				// stays pending
			default:
				delta.Failed++ // failed, or no answer at all
				delete(pending, d.DeviceID)
			}
		}
	}
	if len(pending) > 0 {
		if failed := job.Failures + 1; failed < settings.Delivery.RetryAttempts {
			// Only the devices still owed are sent again.
			left := make([]auth.PushRecipient, 0, len(pj.Recipients))
			for _, r := range pj.Recipients {
				var keep []string
				for _, id := range r.Devices {
					if pending[id] {
						keep = append(keep, id)
					}
				}
				if len(keep) > 0 {
					left = append(left, auth.PushRecipient{UserID: r.UserID, Locale: r.Locale, Devices: keep})
				}
			}
			w.applyPush(ctx, log, svc, job, delta, false)
			w.retryUnit(ctx, log, svc, job, left, wholesale)
			return
		}
		delta.Failed += len(pending) // out of attempts
		if wholesale != nil {
			log.Error("delivering a push unit failed for good", "error", wholesale)
		}
	}
	w.applyPush(ctx, log, svc, job, delta, true)
	if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: "ok"}); err != nil {
		log.Error("complete the push job", "error", err)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "push", "ok")
}

// applyPush adds a unit's answers to the send; done also counts the unit itself, once.
func (w *Worker) applyPush(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, delta auth.PushDelta, done bool) {
	if done {
		delta.DoneUnit = job.ID
	}
	m := w.fns.metrics
	m.PushDeliveries(svc.Realm, "sent", delta.Sent)
	m.PushDeliveries(svc.Realm, "invalid", delta.Invalid)
	m.PushDeliveries(svc.Realm, "failed", delta.Failed)
	m.PushPruned(svc.Realm, "invalid", int64(delta.Invalid))
	if _, err := svc.Store.AddPushSend(ctx, job.Push.SendID, delta, time.Now()); err != nil && !errors.Is(err, auth.ErrPushSendNotFound) {
		log.Warn("could not record what a push unit did", "error", err)
	}
}

// retryUnit queues the unit again after a growing wait, with only the devices still owed.
func (w *Worker) retryUnit(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, left []auth.PushRecipient, cause error) {
	failed := job.Failures + 1
	settings := svc.Settings.Push
	if left == nil {
		left = job.Push.Recipients
	}
	if failed >= settings.Delivery.RetryAttempts {
		// Out of attempts without having been able to try: all that is left failed.
		n := 0
		for _, r := range left {
			n += len(r.Devices)
		}
		log.Error("delivering a push unit failed for good", "error", cause)
		w.applyPush(ctx, log, svc, job, auth.PushDelta{Failed: n}, true)
		if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusError, Message: "the unit could not be delivered"}); err != nil {
			log.Error("complete the push job", "error", err)
		}
		return
	}
	wait := min(settings.Delivery.RetryBackoff<<(failed-1), time.Hour)
	log.Warn("a push unit will be tried again", "attempt", failed, "of", settings.Delivery.RetryAttempts, "retry_in", wait.String(), "error", cause)
	if err := svc.Store.UpdatePushRecipients(ctx, job.ID, left); err != nil {
		log.Error("keep what is left of the unit", "error", err)
	}
	if err := svc.RetryJob(ctx, job.ID, wait); err != nil {
		log.Error("queue the retry", "error", err)
	} else {
		w.fns.metrics.JobRetried(svc.Realm, "push")
	}
}

// pushConfig is a platform's block with the secrets it names read: what a sender needs.
func (w *Worker) pushConfig(ctx context.Context, svc *auth.Users, platform string, pp registry.PushPlatform) (map[string]string, error) {
	cfg := map[string]string{"key_id": pp.KeyID, "team_id": pp.TeamID, "topic": pp.Topic}
	if pp.Sandbox {
		cfg["sandbox"] = "true"
	}
	if p := svc.Settings.Push; p != nil && platform == registry.PlatformWeb {
		cfg["public_key"] = p.WebPublicKey
		cfg["allowed_hosts"] = strings.Join(p.WebEndpointHosts, ",")
	}
	names := pp.SecretNames()
	if len(names) == 0 {
		return cfg, nil
	}
	refs := make([]registry.SecretRef, len(names))
	for i, n := range names {
		refs[i] = registry.SecretRef{Realm: true, Name: n}
	}
	values, missing, err := svc.ResolveSecrets(ctx, refs, "")
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, auth.ErrPushUnavailable
	}
	for field, name := range map[string]string{"credentials": pp.Credentials, "key": pp.Key, "private_key": pp.PrivateKey, "subject": pp.Subject} {
		if name != "" {
			cfg[field] = values["realm."+name]
		}
	}
	return cfg, nil
}
