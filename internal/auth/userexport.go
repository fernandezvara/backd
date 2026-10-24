package auth

import (
	"context"
	"errors"
	"time"

	"github.com/rs/xid"
)

// The export of a user's personal data (design/compliance.md §4): a request is a UserExport record
// and a job that builds the package; the record is what the user lists and downloads from, and
// it outlives the job.
const (
	// ExportOrigin is the origin of the job that builds an export.
	ExportOrigin = "backd:account.export"
	// ExportLease is how long a worker holds an export job before another may take it; building
	// copies files, so it renews between steps.
	ExportLease = 20 * time.Minute
	// ExportMinGap is the least time between two exports a user asks for themselves.
	ExportMinGap = 24 * time.Hour
)

// Export statuses.
const (
	ExportQueued  = "queued"
	ExportRunning = "running"
	ExportReady   = "ready"   // the package is stored and can be downloaded until ExpiresAt
	ExportFailed  = "failed"  // gave up; the user may ask again
	ExportExpired = "expired" // the package was deleted at the end of its retention
)

// UserExport is one export of a user's personal data.
type UserExport struct {
	ID     string
	UserID string
	Status string
	JobID  string
	// Admin is the administrator who asked for it ("" when the user asked for their own).
	Admin string
	// Ticket is the reference an administrator gave (a support ticket, a request number).
	Ticket string
	// Notify says the user was told an administrator made it (data-export-made). Silent is the
	// opposite, with the Reason given for it; both are in the audit record too.
	Notify bool
	Silent bool
	Reason string
	// Locale is the language of the README and of the emails about it.
	Locale     string
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	// ExpiresAt is when the stored package is deleted; zero until it is ready.
	ExpiresAt time.Time
	// Key is where the package is stored (the realm's file storage); Size its bytes.
	Key  string
	Size int64
	// Counts is what the package holds: documents per collection part, files, and records of the
	// system data, for the user to see before they download.
	Counts map[string]int64
	// Error is why it failed, for the administrator; the user is told it did not work.
	Error string
	// Downloads counts the links given out.
	Downloads int
}

// ByAdmin says an administrator asked for it.
func (e UserExport) ByAdmin() bool { return e.Admin != "" }

// ExportJob is what an export job carries: the record it fills.
type ExportJob struct {
	ExportID string
	UserID   string
}

// ErrExportTooSoon means the user asked for an export less than ExportMinGap after another; the
// error says for how long to wait.
type ErrExportTooSoon struct{ RetryAfter time.Duration }

func (e *ErrExportTooSoon) Error() string {
	return "a personal data export was asked for in the last 24 hours; ask again in " + e.RetryAfter.Round(time.Minute).String()
}

// ErrExportRunning means an export of that user is queued or running.
var ErrExportRunning = errors.New("an export of this user is already in progress")

// ExportRequest says who asks for an export and how.
type ExportRequest struct {
	// Admin is the administrator asking for the user's export; "" for the user's own request,
	// which is limited to one a day.
	Admin  string
	Ticket string
	// Notify tells the user an administrator made it. Silent exports carry the reason.
	Notify bool
	Reason string
	Locale string
	// RequestID is the HTTP request, for the job.
	RequestID string
}

// RequestUserExport queues the export of a user's data.
func (s *Users) RequestUserExport(ctx context.Context, userID string, req ExportRequest) (UserExport, error) {
	u, err := s.Store.UserByID(ctx, userID)
	if err != nil {
		return UserExport{}, err
	}
	now := s.now()
	if req.Admin == "" {
		// The user's own: one a day. A failed one does not count, or a fault would lock them out.
		recent, err := s.Store.ListUserExports(ctx, userID, 20)
		if err != nil {
			return UserExport{}, err
		}
		for _, e := range recent {
			if e.ByAdmin() || e.Status == ExportFailed {
				continue
			}
			if wait := e.CreatedAt.Add(ExportMinGap).Sub(now); wait > 0 {
				return UserExport{}, &ErrExportTooSoon{RetryAfter: wait}
			}
			break
		}
	}
	locale := req.Locale
	if locale == "" {
		locale = s.LocaleOf(u)
	}
	rec := UserExport{
		ID: xid.New().String(), UserID: userID, Status: ExportQueued, Admin: req.Admin, Ticket: req.Ticket,
		Notify: req.Admin != "" && req.Notify, Silent: req.Admin != "" && !req.Notify, Reason: req.Reason,
		Locale: locale, CreatedAt: now,
	}
	job := Job{
		ID: xid.New().String(), CallerActor: "user:" + userID, Origin: ExportOrigin, Exclusive: "export:" + userID,
		TimeoutMS: ExportLease.Milliseconds(), RequestID: req.RequestID, Status: JobQueued, CreatedAt: now, ExpiresAt: now.Add(pendingJobTTL),
		Export: &ExportJob{ExportID: rec.ID, UserID: userID},
	}
	if req.Admin != "" {
		job.CallerActor = req.Admin
	}
	rec.JobID = job.ID
	if err := s.Store.CreateUserExport(ctx, rec); err != nil {
		return UserExport{}, err
	}
	if err := s.Store.EnqueueJob(ctx, job); err != nil {
		// Nothing will fill the record: it is not left queued for ever.
		rec.Status, rec.Error, rec.FinishedAt = ExportFailed, "not queued: "+err.Error(), now
		_ = s.Store.ReplaceUserExport(context.WithoutCancel(ctx), rec)
		if errors.Is(err, ErrJobExclusive) {
			return UserExport{}, ErrExportRunning
		}
		return UserExport{}, err
	}
	return rec, nil
}

// UserExports lists a user's exports, newest first.
func (s *Users) UserExports(ctx context.Context, userID string) ([]UserExport, error) {
	return s.Store.ListUserExports(ctx, userID, 50)
}

// UserExport returns one of a user's exports, or ErrNotFound (another user's is not found).
func (s *Users) UserExport(ctx context.Context, userID, id string) (UserExport, error) {
	e, err := s.Store.UserExportByID(ctx, id)
	if err != nil {
		return UserExport{}, err
	}
	if e.UserID != userID {
		return UserExport{}, ErrNotFound
	}
	return e, nil
}
