package mongodb

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
)

func (s *AuthStore) pushSends() *mongo.Collection { return s.db.Collection(PushSendsCollection) }

// --- the jobs of a send ---

type pushRecipientDoc struct {
	UserID  string   `bson:"user_id"`
	Locale  string   `bson:"locale"`
	Devices []string `bson:"devices"`
}

type pushAudienceDoc struct {
	All           bool       `bson:"all,omitempty"`
	Roles         []string   `bson:"roles,omitempty"`
	EmailVerified *bool      `bson:"email_verified,omitempty"`
	Locale        string     `bson:"locale,omitempty"`
	CreatedBefore *time.Time `bson:"created_before,omitempty"`
	CreatedAfter  *time.Time `bson:"created_after,omitempty"`
}

type pushJobDoc struct {
	Kind       string             `bson:"kind"`
	SendID     string             `bson:"send_id"`
	Messages   string             `bson:"messages,omitempty"` // JSON: language → message
	Users      []string           `bson:"users,omitempty"`
	Audience   *pushAudienceDoc   `bson:"audience,omitempty"`
	After      string             `bson:"after,omitempty"`
	NextBatch  int32              `bson:"next_batch,omitempty"`
	Recipients []pushRecipientDoc `bson:"recipients,omitempty"`
	Limited    int32              `bson:"limited,omitempty"`
	NoDevices  int32              `bson:"no_devices,omitempty"`
}

func pushToDoc(p *auth.PushJob) *pushJobDoc {
	if p == nil {
		return nil
	}
	d := &pushJobDoc{Kind: p.Kind, SendID: p.SendID, Users: p.Users, After: p.After, NextBatch: int32(p.NextBatch), Limited: int32(p.Limited), NoDevices: int32(p.NoDevices)}
	if len(p.Messages) > 0 {
		b, _ := json.Marshal(p.Messages)
		d.Messages = string(b)
	}
	if a := p.Audience; a != nil {
		ad := &pushAudienceDoc{All: a.All, Roles: a.Roles, EmailVerified: a.EmailVerified, Locale: a.Locale}
		if !a.CreatedBefore.IsZero() {
			t := a.CreatedBefore
			ad.CreatedBefore = &t
		}
		if !a.CreatedAfter.IsZero() {
			t := a.CreatedAfter
			ad.CreatedAfter = &t
		}
		d.Audience = ad
	}
	for _, r := range p.Recipients {
		d.Recipients = append(d.Recipients, pushRecipientDoc(r))
	}
	return d
}

func pushFromDoc(d *pushJobDoc) *auth.PushJob {
	if d == nil {
		return nil
	}
	p := &auth.PushJob{Kind: d.Kind, SendID: d.SendID, Users: d.Users, After: d.After, NextBatch: int(d.NextBatch), Limited: int(d.Limited), NoDevices: int(d.NoDevices)}
	if d.Messages != "" {
		_ = json.Unmarshal([]byte(d.Messages), &p.Messages)
	}
	if a := d.Audience; a != nil {
		pa := &auth.PushAudience{All: a.All, Roles: a.Roles, EmailVerified: a.EmailVerified, Locale: a.Locale}
		if a.CreatedBefore != nil {
			pa.CreatedBefore = a.CreatedBefore.UTC()
		}
		if a.CreatedAfter != nil {
			pa.CreatedAfter = a.CreatedAfter.UTC()
		}
		p.Audience = pa
	}
	for _, r := range d.Recipients {
		p.Recipients = append(p.Recipients, auth.PushRecipient(r))
	}
	return p
}

// SetPushPlanPosition keeps a planner job's place.
func (s *AuthStore) SetPushPlanPosition(ctx context.Context, jobID, after string, nextBatch int) error {
	_, err := s.jobs().UpdateByID(ctx, jobID, bson.D{{Key: "$set", Value: bson.D{{Key: "push.after", Value: after}, {Key: "push.next_batch", Value: int32(nextBatch)}}}})
	return err
}

// UpdatePushRecipients replaces what a delivery unit still has to do.
func (s *AuthStore) UpdatePushRecipients(ctx context.Context, jobID string, recipients []auth.PushRecipient) error {
	docs := make([]pushRecipientDoc, len(recipients))
	for i, r := range recipients {
		docs[i] = pushRecipientDoc(r)
	}
	_, err := s.jobs().UpdateByID(ctx, jobID, bson.D{{Key: "$set", Value: bson.D{{Key: "push.recipients", Value: docs}}}})
	return err
}

// --- the send record ---

type pushSendDoc struct {
	ID          string     `bson:"_id"`
	Kind        string     `bson:"kind"`
	Sender      string     `bson:"sender"`
	Users       int32      `bson:"users"`
	Planned     bool       `bson:"planned"`
	Batches     int32      `bson:"batches"`
	Units       int32      `bson:"units"`
	BatchesDone int32      `bson:"batches_done"`
	UnitsDone   int32      `bson:"units_done"`
	Queued      int32      `bson:"queued"`
	Limited     int32      `bson:"limited"`
	NoDevices   int32      `bson:"no_devices"`
	Sent        int32      `bson:"sent"`
	Invalid     int32      `bson:"invalid"`
	Failed      int32      `bson:"failed"`
	BatchIDs    []string   `bson:"batch_ids_done,omitempty"`
	UnitIDs     []string   `bson:"unit_ids_done,omitempty"`
	CreatedAt   time.Time  `bson:"created_at"`
	ExpiresAt   time.Time  `bson:"expires_at"`
	CompletedAt *time.Time `bson:"completed_at,omitempty"`
}

func (d pushSendDoc) send() auth.PushSend {
	p := auth.PushSend{
		ID: d.ID, Kind: d.Kind, Sender: d.Sender, Users: int(d.Users), Planned: d.Planned, Batches: int(d.Batches),
		Units: int(d.Units), BatchesDone: int(d.BatchesDone), UnitsDone: int(d.UnitsDone), Queued: int(d.Queued),
		Limited: int(d.Limited), NoDevices: int(d.NoDevices), Sent: int(d.Sent), Invalid: int(d.Invalid), Failed: int(d.Failed),
		CreatedAt: d.CreatedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC(),
	}
	if d.CompletedAt != nil {
		p.CompletedAt = d.CompletedAt.UTC()
	}
	return p
}

func (s *AuthStore) CreatePushSend(ctx context.Context, p auth.PushSend) error {
	_, err := s.pushSends().InsertOne(ctx, pushSendDoc{
		ID: p.ID, Kind: p.Kind, Sender: p.Sender, Users: int32(p.Users), Planned: p.Planned, Batches: int32(p.Batches),
		CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt,
	})
	return err
}

func (s *AuthStore) PushSendByID(ctx context.Context, id string) (auth.PushSend, error) {
	var d pushSendDoc
	err := s.pushSends().FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.PushSend{}, auth.ErrPushSendNotFound
	}
	if err != nil {
		return auth.PushSend{}, err
	}
	return d.send(), nil
}

// settlePush sets completed_at once everything of the send is done.
func (s *AuthStore) settlePush(ctx context.Context, id string, now time.Time) (auth.PushSend, error) {
	var d pushSendDoc
	err := s.pushSends().FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.PushSend{}, auth.ErrPushSendNotFound
	}
	if err != nil {
		return auth.PushSend{}, err
	}
	if d.CompletedAt == nil && d.Planned && d.BatchesDone >= d.Batches && d.UnitsDone >= d.Units {
		res, err := s.pushSends().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "completed_at", Value: bson.D{{Key: "$exists", Value: false}}}},
			bson.D{{Key: "$set", Value: bson.D{{Key: "completed_at", Value: now}}}})
		if err != nil {
			return auth.PushSend{}, err
		}
		if res.ModifiedCount == 1 {
			d.CompletedAt = &now
		}
	}
	return d.send(), nil
}

func (s *AuthStore) AddPushSend(ctx context.Context, id string, d auth.PushDelta, now time.Time) (auth.PushSend, error) {
	if d.DoneBatch != "" {
		// A finished batch counts once, with its totals, however often this runs.
		_, err := s.pushSends().UpdateOne(ctx,
			bson.D{{Key: "_id", Value: id}, {Key: "batch_ids_done", Value: bson.D{{Key: "$ne", Value: d.DoneBatch}}}},
			bson.D{
				{Key: "$push", Value: bson.D{{Key: "batch_ids_done", Value: d.DoneBatch}}},
				{Key: "$inc", Value: bson.D{
					{Key: "batches_done", Value: int32(1)}, {Key: "units", Value: int32(d.Units)}, {Key: "queued", Value: int32(d.Queued)},
					{Key: "limited", Value: int32(d.Limited)}, {Key: "no_devices", Value: int32(d.NoDevices)},
				}},
			})
		if err != nil {
			return auth.PushSend{}, err
		}
	}
	if d.Sent != 0 || d.Invalid != 0 || d.Failed != 0 {
		_, err := s.pushSends().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}},
			bson.D{{Key: "$inc", Value: bson.D{{Key: "sent", Value: int32(d.Sent)}, {Key: "invalid", Value: int32(d.Invalid)}, {Key: "failed", Value: int32(d.Failed)}}}})
		if err != nil {
			return auth.PushSend{}, err
		}
	}
	if d.DoneUnit != "" {
		_, err := s.pushSends().UpdateOne(ctx,
			bson.D{{Key: "_id", Value: id}, {Key: "unit_ids_done", Value: bson.D{{Key: "$ne", Value: d.DoneUnit}}}},
			bson.D{{Key: "$push", Value: bson.D{{Key: "unit_ids_done", Value: d.DoneUnit}}}, {Key: "$inc", Value: bson.D{{Key: "units_done", Value: int32(1)}}}})
		if err != nil {
			return auth.PushSend{}, err
		}
	}
	return s.settlePush(ctx, id, now)
}

func (s *AuthStore) SetPushPlanned(ctx context.Context, id string, batches int, now time.Time) error {
	_, err := s.pushSends().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "planned", Value: true}, {Key: "batches", Value: int32(batches)}}}})
	if err != nil {
		return err
	}
	_, err = s.settlePush(ctx, id, now)
	return err
}

// --- devices and users, for a send ---

func (s *AuthStore) DevicesByIDs(ctx context.Context, ids []string) ([]auth.Device, error) {
	cur, err := s.devices().Find(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}})
	if err != nil {
		return nil, err
	}
	var docs []deviceDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.Device, len(docs))
	for i, d := range docs {
		out[i] = d.device()
	}
	return out, nil
}

func (s *AuthStore) LiveDevicesOf(ctx context.Context, userIDs []string, now time.Time) (map[string][]auth.Device, error) {
	cur, err := s.devices().Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "user_id", Value: bson.D{{Key: "$in", Value: userIDs}}}}}},
		{{Key: "$lookup", Value: bson.D{{Key: "from", Value: SessionsCollection}, {Key: "localField", Value: "session_id"}, {Key: "foreignField", Value: "_id"}, {Key: "as", Value: "s"}}}},
		{{Key: "$match", Value: bson.D{{Key: "s.expires_at", Value: bson.D{{Key: "$gt", Value: now}}}}}},
		{{Key: "$sort", Value: bson.D{{Key: "last_seen", Value: -1}}}},
		{{Key: "$project", Value: bson.D{{Key: "s", Value: 0}}}},
	})
	if err != nil {
		return nil, err
	}
	var docs []deviceDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := map[string][]auth.Device{}
	for _, d := range docs {
		out[d.UserID] = append(out[d.UserID], d.device())
	}
	return out, nil
}

func (s *AuthStore) DeleteDeviceByID(ctx context.Context, id string) error {
	_, err := s.devices().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	return err
}

func (s *AuthStore) UsersByIDs(ctx context.Context, ids []string) ([]auth.User, error) {
	cur, err := s.users().Find(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}})
	if err != nil {
		return nil, err
	}
	var docs []userDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.User, len(docs))
	for i, d := range docs {
		out[i] = d.user()
	}
	return out, nil
}

func (s *AuthStore) AudienceUserIDs(ctx context.Context, a auth.PushAudience, after string, limit int) ([]string, error) {
	filter := bson.D{
		{Key: "_id", Value: bson.D{{Key: "$gt", Value: after}}},
		{Key: "disabled", Value: bson.D{{Key: "$ne", Value: true}}},
		{Key: "erased_at", Value: bson.D{{Key: "$exists", Value: false}}},
	}
	if len(a.Roles) > 0 {
		filter = append(filter, bson.E{Key: "roles", Value: bson.D{{Key: "$in", Value: a.Roles}}})
	}
	if a.EmailVerified != nil {
		filter = append(filter, bson.E{Key: "email_verified", Value: *a.EmailVerified})
	}
	if a.Locale != "" {
		filter = append(filter, bson.E{Key: "locale", Value: a.Locale})
	}
	created := bson.D{}
	if !a.CreatedBefore.IsZero() {
		created = append(created, bson.E{Key: "$lt", Value: a.CreatedBefore})
	}
	if !a.CreatedAfter.IsZero() {
		created = append(created, bson.E{Key: "$gt", Value: a.CreatedAfter})
	}
	if len(created) > 0 {
		filter = append(filter, bson.E{Key: "created_at", Value: created})
	}
	cur, err := s.users().Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)).SetProjection(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		ID string `bson:"_id"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.ID
	}
	return out, nil
}

func (s *AuthStore) ListPushSends(ctx context.Context, f auth.PushSendFilter) ([]auth.PushSend, bool, error) {
	filter := bson.D{}
	if f.Kind != "" {
		filter = append(filter, bson.E{Key: "kind", Value: f.Kind})
	}
	if f.Sender != "" {
		filter = append(filter, bson.E{Key: "sender", Value: f.Sender})
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
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	cur, err := s.pushSends().Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: -1}}).SetSkip(int64(f.Skip)).SetLimit(int64(limit+1)))
	if err != nil {
		return nil, false, err
	}
	var docs []pushSendDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, false, err
	}
	more := len(docs) > limit
	if more {
		docs = docs[:limit]
	}
	out := make([]auth.PushSend, len(docs))
	for i, d := range docs {
		out[i] = d.send()
	}
	return out, more, nil
}

type pushOutboxDoc struct {
	ID        string    `bson:"_id"`
	SendID    string    `bson:"send_id"`
	UserID    string    `bson:"user_id"`
	DeviceID  string    `bson:"device_id"`
	Platform  string    `bson:"platform"`
	Status    string    `bson:"status"`
	Attempt   int32     `bson:"attempt"`
	Message   string    `bson:"message"` // JSON
	CreatedAt time.Time `bson:"created_at"`
	ExpiresAt time.Time `bson:"expires_at"`
}

func (s *AuthStore) PutPushOutbox(ctx context.Context, e auth.PushOutboxEntry) error {
	msg, err := json.Marshal(e.Message)
	if err != nil {
		return err
	}
	_, err = s.db.Collection(PushOutboxCollection).InsertOne(ctx, pushOutboxDoc{
		ID: e.ID, SendID: e.SendID, UserID: e.UserID, DeviceID: e.DeviceID, Platform: e.Platform, Status: e.Status,
		Attempt: int32(e.Attempt), Message: string(msg), CreatedAt: e.CreatedAt, ExpiresAt: e.ExpiresAt,
	})
	return err
}

func (s *AuthStore) ListPushOutbox(ctx context.Context, limit int) ([]auth.PushOutboxEntry, error) {
	cur, err := s.db.Collection(PushOutboxCollection).Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "_id", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	var docs []pushOutboxDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.PushOutboxEntry, len(docs))
	for i, d := range docs {
		e := auth.PushOutboxEntry{ID: d.ID, SendID: d.SendID, UserID: d.UserID, DeviceID: d.DeviceID, Platform: d.Platform, Status: d.Status,
			Attempt: int(d.Attempt), CreatedAt: d.CreatedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC()}
		_ = json.Unmarshal([]byte(d.Message), &e.Message)
		out[i] = e
	}
	return out, nil
}

func (s *AuthStore) DeleteDevicesOfPlatform(ctx context.Context, platform string) (int64, error) {
	res, err := s.devices().DeleteMany(ctx, bson.D{{Key: "platform", Value: platform}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}
