package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/push"
	"github.com/fernandezvara/backd/internal/registry"
)

// Sending pushes (design/push-notifications.md §7 to §9). A send is a record plus jobs: a
// planner (for an audience) creates batches of up to PushBatchSize users; a batch applies the
// limits, resolves devices and creates delivery units of up to PushUnitSize users; a unit is
// delivered by a worker. Every job has a deterministic id, so one that runs again creates
// nothing twice.

// PushOrigin is the origin of the jobs of a send.
const PushOrigin = "backd:push"

// The kinds of push job.
const (
	PushPlan    = "plan"
	PushBatch   = "batch"
	PushDeliver = "deliver"
)

// Sizes of what a send is split into.
const (
	PushBatchSize = 1000
	PushUnitSize  = 100
)

const (
	pushDatabase = "_push"
	pushFunction = "send"
)

// PushAudience is who a send reaches when it names no users.
type PushAudience struct {
	All           bool
	Roles         []string // any of
	EmailVerified *bool
	Locale        string
	CreatedBefore time.Time
	CreatedAfter  time.Time
}

// PushRecipient is a user of a delivery unit with the devices to deliver to.
type PushRecipient struct {
	UserID  string
	Locale  string
	Devices []string // device ids: tokens are loaded when the unit is delivered
}

// PushJob is what a push job carries.
type PushJob struct {
	Kind   string
	SendID string
	// Messages are the message in each language of the realm.
	Messages map[string]push.Message
	// Batch: the users. Plan: the audience, the last user id it paged to and the next batch.
	Users     []string
	Audience  *PushAudience
	After     string
	NextBatch int
	// Deliver: who gets it, and how many users of the unit's chunk were not delivered to.
	Recipients []PushRecipient
	Limited    int
	NoDevices  int
}

// PushSend is the record of one send: totals, never content.
type PushSend struct {
	ID        string
	Kind      string // the template kind, or push.RawKind
	Sender    string // function:<database>/<name>, or admin:<id>
	CreatedAt time.Time
	ExpiresAt time.Time
	// Users is the number of users named; -1 when the send has an audience.
	Users int
	// Planned says every batch exists (Batches is final).
	Planned bool
	Batches int
	// Units is the delivery units made so far (final once Planned and every batch is done).
	Units       int
	BatchesDone int
	UnitsDone   int
	// Totals, by what became of each user or device.
	Queued    int // users handed to delivery
	Limited   int // users over their limits
	NoDevices int // users with no device to reach
	Sent      int // devices
	Invalid   int // devices removed as dead
	Failed    int // devices not delivered to
	// CompletedAt is zero until everything is done.
	CompletedAt time.Time
}

// PushDelta is what to add to a send's totals. DoneBatch and DoneUnit name the batch or unit
// that finished: it is counted once, however many times the same update runs.
type PushDelta struct {
	Units, Queued, Limited, NoDevices int
	Sent, Invalid, Failed             int
	DoneBatch, DoneUnit               string
}

// PushUserFilter selects users for an audience page.
type PushUserFilter = PushAudience

// PushSendFilter selects sends to list. Sender is "function:<database>/<name>" or "admin:<id>";
// zero fields match everything.
type PushSendFilter struct {
	Kind, Sender string
	Since, Until time.Time
	Limit, Skip  int
}

var (
	// ErrPushSendNotFound: no send has that id.
	ErrPushSendNotFound = errors.New("push send not found")
)

// PushSendInput is a send, validated and rendered by the caller.
type PushSendInput struct {
	Kind     string
	Sender   string
	Messages map[string]push.Message
	Users    []string
	Audience *PushAudience
}

// PushStarted is what starting a send reports.
type PushStarted struct {
	SendID string
	// Users is the number named (-1 for an audience); Jobs the jobs created.
	Users, Jobs int
	// Known says Queued, Limited and NoDevices are final: a send that fits one batch is
	// planned at once.
	Known                     bool
	Queued, Limited, NoDevice int
}

func (s *Users) pushSendRetention() time.Duration {
	if p := s.Settings.Push; p != nil && p.SendRetention > 0 {
		return p.SendRetention
	}
	return registry.DefaultPushSendRetention
}

// StartPushSend records a send and starts it. A send of one batch or less is planned here, so
// its answer has the limits applied; a larger one is left to jobs.
func (s *Users) StartPushSend(ctx context.Context, in PushSendInput, requestID string) (PushStarted, error) {
	if s.Settings.Push == nil {
		return PushStarted{}, ErrPushNotConfigured
	}
	now := s.now()
	rec := PushSend{ID: "ps_" + xid.New().String(), Kind: in.Kind, Sender: in.Sender, CreatedAt: now, ExpiresAt: now.Add(s.pushSendRetention()), Users: -1}
	started := PushStarted{SendID: rec.ID, Users: -1}
	switch {
	case in.Audience != nil:
		rec.Planned = false
	default:
		users := dedupe(in.Users)
		rec.Users = len(users)
		started.Users = len(users)
		rec.Batches = (len(users) + PushBatchSize - 1) / PushBatchSize
		rec.Planned = true
		in.Users = users
	}
	if err := s.Store.CreatePushSend(ctx, rec); err != nil {
		return PushStarted{}, err
	}
	switch {
	case in.Audience != nil:
		_, err := s.EnqueueJob(ctx, s.pushJob(rec.ID+"-plan", requestID, PushJob{Kind: PushPlan, SendID: rec.ID, Messages: in.Messages, Audience: in.Audience}))
		started.Jobs = 1
		return started, err
	case len(in.Users) == 0:
		return started, s.finishEmptyPush(ctx, rec.ID)
	case len(in.Users) <= PushBatchSize:
		counts, err := s.PlanPushBatch(ctx, rec.ID+"-b0", rec.ID, in.Users, in.Messages, requestID)
		started.Known, started.Queued, started.Limited, started.NoDevice = true, counts.Queued, counts.Limited, counts.NoDevices
		started.Jobs = counts.Units
		return started, err
	}
	for i := 0; i*PushBatchSize < len(in.Users); i++ {
		users := in.Users[i*PushBatchSize : min((i+1)*PushBatchSize, len(in.Users))]
		_, err := s.EnqueueJobID(ctx, s.pushJob(fmt.Sprintf("%s-b%d", rec.ID, i), requestID, PushJob{Kind: PushBatch, SendID: rec.ID, Messages: in.Messages, Users: users}))
		if err != nil {
			return started, err
		}
		started.Jobs++
	}
	return started, nil
}

func (s *Users) finishEmptyPush(ctx context.Context, sendID string) error {
	_, err := s.Store.AddPushSend(ctx, sendID, PushDelta{}, s.now())
	return err
}

func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// pushJob makes a push job with the id.
func (s *Users) pushJob(id, requestID string, pj PushJob) Job {
	timeout := time.Duration(0)
	if p := s.Settings.Push; p != nil {
		timeout = p.Delivery.Timeout
	}
	if pj.Kind != PushDeliver {
		timeout = 5 * time.Minute
	}
	return Job{
		ID: id, Database: pushDatabase, Function: pushFunction, CallerActor: "system", Origin: PushOrigin,
		TimeoutMS: timeout.Milliseconds(), RequestID: requestID, Push: &pj,
	}
}

// EnqueueJobID is EnqueueJob for a job that has its id already: a job with that id existing
// is not an error (created is false), so work that runs again doesn't make it twice.
func (s *Users) EnqueueJobID(ctx context.Context, j Job) (created bool, err error) {
	now := s.now()
	j.Status = JobQueued
	j.CreatedAt = now
	j.ExpiresAt = now.Add(pendingJobTTL)
	err = s.Store.EnqueueJob(ctx, j)
	if errors.Is(err, ErrJobExists) {
		return false, nil
	}
	return err == nil, err
}

// PushCounts is what planning a batch decided.
type PushCounts struct {
	Queued, Limited, NoDevices int
	// Units is the delivery units of the batch, those made now and those that existed.
	Units int
}

// PlanPushBatch applies the limits, resolves the devices and makes the delivery units of one
// batch of users. Unit ids derive from the batch's, so a batch that runs again continues where
// it was and makes nothing twice; the batch's totals are added once, when it ends.
func (s *Users) PlanPushBatch(ctx context.Context, batchID, sendID string, users []string, messages map[string]push.Message, requestID string) (PushCounts, error) {
	var total PushCounts
	for i := 0; i*PushUnitSize < len(users); i++ {
		chunk := users[i*PushUnitSize : min((i+1)*PushUnitSize, len(users))]
		unitID := fmt.Sprintf("%s-u%d", batchID, i)
		if existing, found, err := s.Store.GetJob(ctx, unitID); err != nil {
			return total, err
		} else if found && existing.Push != nil {
			// Made by an earlier run, which was charged for it: its counts are in the job.
			total.Units++
			total.Queued += len(existing.Push.Recipients)
			total.Limited += existing.Push.Limited
			total.NoDevices += existing.Push.NoDevices
			continue
		}
		unit, c, err := s.planUnit(ctx, sendID, chunk, messages)
		if err != nil {
			return total, err
		}
		total.Limited += c.Limited
		total.NoDevices += c.NoDevices
		s.Metrics.PushUsers(s.Realm, "limited", c.Limited)
		s.Metrics.PushUsers(s.Realm, "no_devices", c.NoDevices)
		if len(unit.Recipients) == 0 {
			continue // nobody to deliver to: no unit
		}
		if _, err := s.EnqueueJobID(ctx, s.pushJob(unitID, requestID, unit)); err != nil {
			return total, err
		}
		total.Queued += len(unit.Recipients)
		s.Metrics.PushUsers(s.Realm, "queued", len(unit.Recipients))
		total.Units++
	}
	if _, err := s.Store.AddPushSend(ctx, sendID, PushDelta{Units: total.Units, Queued: total.Queued, Limited: total.Limited, NoDevices: total.NoDevices, DoneBatch: batchID}, s.now()); err != nil {
		return total, err
	}
	return total, nil
}

// planUnit decides who of a chunk of users gets the push.
func (s *Users) planUnit(ctx context.Context, sendID string, chunk []string, messages map[string]push.Message) (PushJob, PushCounts, error) {
	var c PushCounts
	unit := PushJob{Kind: PushDeliver, SendID: sendID, Messages: map[string]push.Message{}}
	users, err := s.Store.UsersByIDs(ctx, chunk)
	if err != nil {
		return unit, c, err
	}
	byID := make(map[string]User, len(users))
	for _, u := range users {
		byID[u.ID] = u
	}
	silent := false
	for _, m := range messages {
		silent = m.Silent
		break
	}
	devices, err := s.Store.LiveDevicesOf(ctx, chunk, s.now())
	if err != nil {
		return unit, c, err
	}
	for _, id := range chunk {
		u, ok := byID[id]
		var ids []string
		for _, d := range devices[id] {
			if silent && d.Platform == registry.PlatformWeb {
				continue // browsers require a visible notification
			}
			ids = append(ids, d.ID)
		}
		if !ok || u.Disabled || !u.ErasedAt.IsZero() || len(ids) == 0 {
			c.NoDevices++
			continue
		}
		allowed, err := s.pushUserAllowed(ctx, id)
		if err != nil {
			return unit, c, err
		}
		if !allowed {
			c.Limited++
			continue
		}
		locale := u.Locale
		if _, ok := messages[locale]; !ok {
			locale, _ = s.Settings.Languages()
		}
		unit.Recipients = append(unit.Recipients, PushRecipient{UserID: id, Locale: locale, Devices: ids})
		unit.Messages[locale] = messages[locale]
	}
	unit.Limited, unit.NoDevices = c.Limited, c.NoDevices
	return unit, c, nil
}

// pushUserAllowed counts a push against the user's limits and says whether it is within them.
// Silent pushes count, and nobody is exempt.
func (s *Users) pushUserAllowed(ctx context.Context, userID string) (bool, error) {
	l := s.Settings.Push.Limits
	for _, w := range []struct {
		key    string
		limit  int
		window time.Duration
	}{{"push:user:" + userID + ":m", l.PerMinute, time.Minute}, {"push:user:" + userID + ":d", l.PerDay, 24 * time.Hour}} {
		err := s.RateLimit(ctx, w.key, w.limit, w.window)
		var th *ThrottledError
		switch {
		case errors.As(err, &th):
			return false, nil
		case err != nil:
			return false, err
		}
	}
	return true, nil
}

// PlanPushAudience pages an audience into batches. It records its position in the job, so a
// planner that runs again continues where it stopped; the batches' ids make a repeat harmless.
func (s *Users) PlanPushAudience(ctx context.Context, job Job) error {
	pj := job.Push
	next, after := pj.NextBatch, pj.After
	for {
		ids, err := s.Store.AudienceUserIDs(ctx, *pj.Audience, after, PushBatchSize)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return s.Store.SetPushPlanned(ctx, pj.SendID, next, s.now())
		}
		batch := s.pushJob(fmt.Sprintf("%s-b%d", pj.SendID, next), job.RequestID, PushJob{Kind: PushBatch, SendID: pj.SendID, Messages: pj.Messages, Users: ids})
		if _, err := s.EnqueueJobID(ctx, batch); err != nil {
			return err
		}
		next++
		after = ids[len(ids)-1]
		if err := s.Store.SetPushPlanPosition(ctx, job.ID, after, next); err != nil {
			return err
		}
		if len(ids) < PushBatchSize {
			return s.Store.SetPushPlanned(ctx, pj.SendID, next, s.now())
		}
	}
}

// PushSends lists the realm's sends, newest first.
func (s *Users) PushSends(ctx context.Context, f PushSendFilter) ([]PushSend, bool, error) {
	if s.Settings.Push == nil {
		return nil, false, ErrPushNotConfigured
	}
	return s.Store.ListPushSends(ctx, f)
}

// PushSendByID returns a send's record.
func (s *Users) PushSendByID(ctx context.Context, id string) (PushSend, error) {
	return s.Store.PushSendByID(ctx, id)
}
