package mongodb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/jsonnum"
	"github.com/fernandezvara/backd/internal/metrics"
)

type jobResultDoc struct {
	Status     string `bson:"status"`
	Output     any    `bson:"output,omitempty"`
	Code       string `bson:"code,omitempty"`
	Message    string `bson:"message,omitempty"`
	Details    any    `bson:"details,omitempty"`
	DurationMS int64  `bson:"duration_ms"`
	// HTTPStatus is the function's own chosen status when Code is set
	// (roadmap F12's idempotent replay needs it); unused otherwise.
	HTTPStatus int `bson:"http_status,omitempty"`
}

type emailJobDoc struct {
	Kind       string          `bson:"kind"`
	UserID     string          `bson:"user_id"`
	Locale     string          `bson:"locale,omitempty"`
	RedirectTo string          `bson:"redirect_to,omitempty"`
	To         string          `bson:"to,omitempty"`
	Notice     bool            `bson:"notice,omitempty"`
	Invitation string          `bson:"invitation_id,omitempty"`
	Custom     *customEmailDoc `bson:"custom,omitempty"`
}

type customEmailDoc struct {
	Function string         `bson:"function"`
	To       []string       `bson:"to"`
	CC       []string       `bson:"cc,omitempty"`
	BCC      []string       `bson:"bcc,omitempty"`
	Data     map[string]any `bson:"data,omitempty"`
}

type jobDoc struct {
	ID             string        `bson:"_id"`
	Database       string        `bson:"database"`
	Function       string        `bson:"function"`
	Input          any           `bson:"input"`
	CallerActor    string        `bson:"caller_actor"`
	CallerUserID   string        `bson:"caller_user_id,omitempty"`
	CallerKeyHash  string        `bson:"caller_key_hash,omitempty"`
	Scheduled      bool          `bson:"scheduled,omitempty"`
	ActsAsFunction bool          `bson:"acts_as_function,omitempty"`
	Origin         string        `bson:"origin,omitempty"`
	Email          *emailJobDoc  `bson:"email,omitempty"`
	Erase          *eraseJobDoc  `bson:"erase,omitempty"`
	ParentID       string        `bson:"parent_id,omitempty"`
	Depth          int32         `bson:"depth,omitempty"`
	Status         string        `bson:"status"`
	Attempts       int32         `bson:"attempts"`
	Failures       int32         `bson:"failures,omitempty"`
	NextAttemptAt  *time.Time    `bson:"next_attempt_at,omitempty"`
	TimeoutMS      int64         `bson:"timeout_ms"`
	RequestID      string        `bson:"request_id,omitempty"`
	LeaseOwner     string        `bson:"lease_owner,omitempty"`
	LeaseExpires   *time.Time    `bson:"lease_expires,omitempty"`
	CreatedAt      time.Time     `bson:"created_at"`
	CompletedAt    *time.Time    `bson:"completed_at,omitempty"`
	ExpiresAt      time.Time     `bson:"expires_at"`
	Result         *jobResultDoc `bson:"result,omitempty"`
}

type eraseJobDoc struct {
	UserID string           `bson:"user_id"`
	Email  string           `bson:"email,omitempty"`
	Counts map[string]int64 `bson:"counts,omitempty"`
}

func eraseFromDoc(d *eraseJobDoc) *auth.EraseJob {
	if d == nil {
		return nil
	}
	return &auth.EraseJob{UserID: d.UserID, Email: d.Email, Counts: d.Counts}
}

func eraseToDoc(e *auth.EraseJob) *eraseJobDoc {
	if e == nil {
		return nil
	}
	return &eraseJobDoc{UserID: e.UserID, Email: e.Email, Counts: e.Counts}
}

func (s *AuthStore) jobs() *mongo.Collection { return s.db.Collection(JobsCollection) }

// decodeJSONAny turns a JSON value into a plain Go value (json.Number
// normalized to int64/float64), suitable for BSON encoding directly:
// the driver marshals map[string]any, []any and the scalar types as-is.
func decodeJSONAny(raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return jsonnum.Normalize(v), nil
}

// encodeJSONAny is decodeJSONAny's reverse: a value read back from
// MongoDB (bson.D/bson.A for documents/arrays, since it was decoded into
// an `any` field) into JSON. fromBSONValue normalizes it to
// map[string]any/[]any first, which encoding/json marshals directly.
func encodeJSONAny(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(fromBSONValue(v))
	if err != nil {
		return nil
	}
	return data
}

func jobResultToDoc(r *auth.JobResult) (*jobResultDoc, error) {
	if r == nil {
		return nil, nil
	}
	output, err := decodeJSONAny(r.Output)
	if err != nil {
		return nil, err
	}
	details, err := decodeJSONAny(r.Details)
	if err != nil {
		return nil, err
	}
	return &jobResultDoc{Status: r.Status, Output: output, Code: r.Code, Message: r.Message, Details: details, DurationMS: r.DurationMS, HTTPStatus: r.HTTPStatus}, nil
}

func jobResultFromDoc(d *jobResultDoc) *auth.JobResult {
	if d == nil {
		return nil
	}
	return &auth.JobResult{Status: d.Status, Output: encodeJSONAny(d.Output), Code: d.Code, Message: d.Message, Details: encodeJSONAny(d.Details), DurationMS: d.DurationMS, HTTPStatus: d.HTTPStatus}
}

func emailJobFromDoc(d *emailJobDoc) *auth.EmailJob {
	if d == nil {
		return nil
	}
	return &auth.EmailJob{Kind: d.Kind, UserID: d.UserID, Locale: d.Locale, RedirectTo: d.RedirectTo, To: d.To, Notice: d.Notice, InvitationID: d.Invitation, Custom: customFromDoc(d.Custom)}
}

func customFromDoc(d *customEmailDoc) *auth.CustomEmail {
	if d == nil {
		return nil
	}
	return &auth.CustomEmail{Function: d.Function, To: d.To, CC: d.CC, BCC: d.BCC, Data: d.Data}
}

func customToDoc(c *auth.CustomEmail) *customEmailDoc {
	if c == nil {
		return nil
	}
	return &customEmailDoc{Function: c.Function, To: c.To, CC: c.CC, BCC: c.BCC, Data: c.Data}
}

func emailJobToDoc(e *auth.EmailJob) *emailJobDoc {
	if e == nil {
		return nil
	}
	return &emailJobDoc{Kind: e.Kind, UserID: e.UserID, Locale: e.Locale, RedirectTo: e.RedirectTo, To: e.To, Notice: e.Notice, Invitation: e.InvitationID, Custom: customToDoc(e.Custom)}
}

func jobFromDoc(d jobDoc) auth.Job {
	j := auth.Job{
		ID: d.ID, Database: d.Database, Function: d.Function, Input: encodeJSONAny(d.Input),
		CallerActor: d.CallerActor, CallerUserID: d.CallerUserID, CallerKeyHash: d.CallerKeyHash, Scheduled: d.Scheduled,
		ActsAsFunction: d.ActsAsFunction, Email: emailJobFromDoc(d.Email), Erase: eraseFromDoc(d.Erase), Origin: d.Origin, ParentID: d.ParentID, Depth: int(d.Depth),
		TimeoutMS: d.TimeoutMS, RequestID: d.RequestID,
		Status: d.Status, Attempts: int(d.Attempts), CreatedAt: d.CreatedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC(),
		Result: jobResultFromDoc(d.Result),
	}
	j.Failures = int(d.Failures)
	if d.NextAttemptAt != nil {
		j.NextAttemptAt = d.NextAttemptAt.UTC()
	}
	if d.CompletedAt != nil {
		j.CompletedAt = d.CompletedAt.UTC()
	}
	return j
}

// EnqueueJob inserts a new job, queued for a worker to claim.
func (s *AuthStore) EnqueueJob(ctx context.Context, j auth.Job) error {
	input, err := decodeJSONAny(j.Input)
	if err != nil {
		return err
	}
	_, err = s.jobs().InsertOne(ctx, jobDoc{
		ID: j.ID, Database: j.Database, Function: j.Function, Input: input,
		CallerActor: j.CallerActor, CallerUserID: j.CallerUserID, CallerKeyHash: j.CallerKeyHash, Scheduled: j.Scheduled, Status: j.Status,
		ActsAsFunction: j.ActsAsFunction, Email: emailJobToDoc(j.Email), Erase: eraseToDoc(j.Erase), Origin: j.Origin, ParentID: j.ParentID, Depth: int32(j.Depth),
		Attempts: 0, TimeoutMS: j.TimeoutMS, RequestID: j.RequestID,
		CreatedAt: j.CreatedAt, ExpiresAt: j.ExpiresAt,
	})
	if mongo.IsDuplicateKeyError(err) {
		return auth.ErrJobExists
	}
	return err
}

// ClaimJob atomically claims the oldest claimable job: never claimed
// (status queued, no lease yet), or whose lease has expired (a dead
// worker's jobs are picked up again). The lease extends to at plus the
// job's own timeout_ms plus margin, computed in the same update so a
// claim never needs a second round trip to look the function's timeout
// up.
func (s *AuthStore) ClaimJob(ctx context.Context, workerID string, at time.Time, margin time.Duration) (auth.Job, bool, error) {
	filter := bson.D{
		{Key: "status", Value: bson.D{{Key: "$ne", Value: auth.JobDone}}},
		{Key: "$or", Value: bson.A{
			bson.D{{Key: "lease_expires", Value: bson.D{{Key: "$exists", Value: false}}}},
			bson.D{{Key: "lease_expires", Value: bson.D{{Key: "$lte", Value: at}}}},
		}},
	}
	leaseExpires := bson.D{{Key: "$add", Value: bson.A{at, "$timeout_ms", margin.Milliseconds()}}}
	pipeline := mongo.Pipeline{{{Key: "$set", Value: bson.D{
		{Key: "status", Value: auth.JobRunning},
		{Key: "lease_owner", Value: workerID},
		{Key: "lease_expires", Value: leaseExpires},
		{Key: "attempts", Value: bson.D{{Key: "$add", Value: bson.A{"$attempts", int32(1)}}}},
	}}}, {{Key: "$unset", Value: "next_attempt_at"}}}
	opts := options.FindOneAndUpdate().SetSort(bson.D{{Key: "created_at", Value: 1}}).SetReturnDocument(options.After)
	var d jobDoc
	err := s.jobs().FindOneAndUpdate(ctx, filter, pipeline, opts).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.Job{}, false, nil
	}
	if err != nil {
		return auth.Job{}, false, err
	}
	return jobFromDoc(d), true, nil
}

// CompleteJob records a job's result; expiresAt starts its retention
// countdown. The update is conditional on the job not already being
// done, so a worker whose lease expired and was reclaimed can't finish
// late and overwrite a result another worker already recorded.
func (s *AuthStore) CompleteJob(ctx context.Context, id string, result auth.JobResult, completedAt, expiresAt time.Time) error {
	doc, err := jobResultToDoc(&result)
	if err != nil {
		return err
	}
	filter := bson.D{{Key: "_id", Value: id}, {Key: "status", Value: bson.D{{Key: "$ne", Value: auth.JobDone}}}}
	_, err = s.jobs().UpdateOne(ctx, filter, bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: auth.JobDone},
		{Key: "completed_at", Value: completedAt},
		{Key: "expires_at", Value: expiresAt},
		{Key: "result", Value: doc},
	}}})
	return err
}

// RetryJob counts a failed attempt and queues the job again: its lease runs
// until notBefore, so no worker claims it earlier. Conditional on the job
// not being done, like CompleteJob.
func (s *AuthStore) RetryJob(ctx context.Context, id string, notBefore time.Time) error {
	filter := bson.D{{Key: "_id", Value: id}, {Key: "status", Value: bson.D{{Key: "$ne", Value: auth.JobDone}}}}
	_, err := s.jobs().UpdateOne(ctx, filter, bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "status", Value: auth.JobQueued},
			{Key: "lease_expires", Value: notBefore},
			{Key: "next_attempt_at", Value: notBefore},
		}},
		{Key: "$unset", Value: bson.D{{Key: "lease_owner", Value: ""}}},
		{Key: "$inc", Value: bson.D{{Key: "failures", Value: 1}}},
	})
	return err
}

// ListJobs returns the jobs matching f, newest first.
func (s *AuthStore) ListJobs(ctx context.Context, f auth.JobFilter) ([]auth.Job, bool, error) {
	filter := bson.D{}
	for _, c := range []struct{ key, value string }{{"database", f.Database}, {"function", f.Function}, {"status", f.Status}, {"origin", f.Origin}, {"erase.user_id", f.EraseUser}} {
		if c.value != "" {
			filter = append(filter, bson.E{Key: c.key, Value: c.value})
		}
	}
	if f.Scheduled != nil {
		if *f.Scheduled {
			filter = append(filter, bson.E{Key: "scheduled", Value: true})
		} else {
			filter = append(filter, bson.E{Key: "scheduled", Value: bson.D{{Key: "$ne", Value: true}}})
		}
	}
	created := bson.D{}
	if !f.Since.IsZero() {
		created = append(created, bson.E{Key: "$gte", Value: f.Since})
	}
	if !f.Until.IsZero() {
		created = append(created, bson.E{Key: "$lt", Value: f.Until})
	}
	if len(created) > 0 {
		filter = append(filter, bson.E{Key: "created_at", Value: created})
	}
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}).SetSkip(int64(f.Skip))
	if f.Limit > 0 {
		opts.SetLimit(int64(f.Limit) + 1)
	}
	cur, err := s.jobs().Find(ctx, filter, opts)
	if err != nil {
		return nil, false, err
	}
	var docs []jobDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, false, err
	}
	more := f.Limit > 0 && len(docs) > f.Limit
	if more {
		docs = docs[:f.Limit]
	}
	out := make([]auth.Job, len(docs))
	for i, d := range docs {
		out[i] = jobFromDoc(d)
	}
	return out, more, nil
}

// GetJob returns one job by id.
func (s *AuthStore) GetJob(ctx context.Context, id string) (auth.Job, bool, error) {
	var d jobDoc
	err := s.jobs().FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.Job{}, false, nil
	}
	if err != nil {
		return auth.Job{}, false, err
	}
	return jobFromDoc(d), true, nil
}

// DeleteEmailJobsOfUser removes the email jobs addressed to the user.
func (s *AuthStore) DeleteEmailJobsOfUser(ctx context.Context, userID string) error {
	_, err := s.jobs().DeleteMany(ctx, bson.D{{Key: "email.user_id", Value: userID}})
	return err
}

// AddEraseCount adds n to one of an erase job's counters. The counters are
// incremented, never read and written back, so two workers finishing the
// same batch can't lose each other's counts.
func (s *AuthStore) AddEraseCount(ctx context.Context, jobID, key string, n int64) error {
	_, err := s.jobs().UpdateByID(ctx, jobID, bson.D{{Key: "$inc", Value: bson.D{{Key: "erase.counts." + key, Value: n}}}})
	return err
}

// ClearEraseEmail removes the email an erase job held.
func (s *AuthStore) ClearEraseEmail(ctx context.Context, jobID string) error {
	_, err := s.jobs().UpdateByID(ctx, jobID, bson.D{{Key: "$unset", Value: bson.D{{Key: "erase.email", Value: ""}}}})
	return err
}

// ReopenJob queues a job that ended with an error again.
func (s *AuthStore) ReopenJob(ctx context.Context, id string, expiresAt time.Time) (bool, error) {
	res, err := s.jobs().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: id}, {Key: "status", Value: auth.JobDone}, {Key: "result.status", Value: bson.D{{Key: "$ne", Value: "ok"}}}},
		bson.D{
			{Key: "$set", Value: bson.D{{Key: "status", Value: auth.JobQueued}, {Key: "attempts", Value: int32(0)}, {Key: "failures", Value: int32(0)}, {Key: "expires_at", Value: expiresAt}}},
			{Key: "$unset", Value: bson.D{{Key: "result", Value: ""}, {Key: "completed_at", Value: ""}, {Key: "lease_owner", Value: ""}, {Key: "lease_expires", Value: ""}, {Key: "next_attempt_at", Value: ""}}},
		})
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// JobStats summarizes the jobs that are not done, by kind and state: queued
// (claimable now), running (holding a lease) and waiting (for a retry's delay).
// It is the metrics refresher's query, never a scrape's.
func (s *AuthStore) JobStats(ctx context.Context, now time.Time) ([]metrics.JobStat, error) {
	kind := bson.D{{Key: "$switch", Value: bson.D{
		{Key: "branches", Value: bson.A{
			bson.D{{Key: "case", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$email", false}}}}, {Key: "then", Value: "email"}},
			bson.D{{Key: "case", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$erase", false}}}}, {Key: "then", Value: "erase"}},
			bson.D{{Key: "case", Value: bson.D{{Key: "$eq", Value: bson.A{bson.D{{Key: "$ifNull", Value: bson.A{"$scheduled", false}}}, true}}}}, {Key: "then", Value: "schedule"}},
		}},
		{Key: "default", Value: "function"},
	}}}
	leased := bson.D{{Key: "$gt", Value: bson.A{bson.D{{Key: "$ifNull", Value: bson.A{"$lease_expires", time.Time{}}}}, now}}}
	state := bson.D{{Key: "$switch", Value: bson.D{
		{Key: "branches", Value: bson.A{
			bson.D{{Key: "case", Value: bson.D{{Key: "$and", Value: bson.A{leased, bson.D{{Key: "$ifNull", Value: bson.A{"$lease_owner", false}}}}}}}, {Key: "then", Value: "running"}},
			bson.D{{Key: "case", Value: leased}, {Key: "then", Value: "waiting"}},
		}},
		{Key: "default", Value: "queued"},
	}}}
	cur, err := s.jobs().Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "status", Value: bson.D{{Key: "$ne", Value: auth.JobDone}}}}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "kind", Value: kind}, {Key: "state", Value: state}}},
			{Key: "n", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "oldest", Value: bson.D{{Key: "$min", Value: "$created_at"}}},
		}}},
	})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID struct {
			Kind  string `bson:"kind"`
			State string `bson:"state"`
		} `bson:"_id"`
		N      int64     `bson:"n"`
		Oldest time.Time `bson:"oldest"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	byKind := map[string]*metrics.JobStat{}
	for _, r := range rows {
		st := byKind[r.ID.Kind]
		if st == nil {
			st = &metrics.JobStat{Kind: r.ID.Kind}
			byKind[r.ID.Kind] = st
		}
		switch r.ID.State {
		case "running":
			st.Running = r.N
		case "waiting":
			st.Waiting = r.N
		default:
			st.Queued, st.OldestQueued = r.N, r.Oldest.UTC()
		}
	}
	out := make([]metrics.JobStat, 0, len(byKind))
	for _, st := range byKind {
		out = append(out, *st)
	}
	return out, nil
}

// EraseNeedsAttention counts the erase jobs that ended in failure: their
// attempts were used up, and an administrator has to repeat the erase.
func (s *AuthStore) EraseNeedsAttention(ctx context.Context) (int64, error) {
	return s.jobs().CountDocuments(ctx, bson.D{
		{Key: "erase", Value: bson.D{{Key: "$exists", Value: true}}},
		{Key: "status", Value: auth.JobDone},
		{Key: "result.status", Value: bson.D{{Key: "$ne", Value: "ok"}}},
	})
}
