package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

// ErrEmailNotConfigured means the realm has no email section: it sends no
// email.
var ErrEmailNotConfigured = errors.New("this realm doesn't send email")

// EmailLimitedError means an email limit was reached: nothing was queued.
type EmailLimitedError struct{ RetryAfter time.Duration }

func (e *EmailLimitedError) Error() string {
	return fmt.Sprintf("too many emails; retry in %s", e.RetryAfter.Round(time.Second))
}

// EmailRequest asks for one email to be sent to a user.
type EmailRequest struct {
	Kind       string // an email kind (verify-email, ...)
	UserID     string
	Address    string // the recipient, for the per-recipient limits only (never stored readable)
	Locale     string
	RedirectTo string // already checked against the realm's allowed redirects
	ClientIP   string // who caused it, for the per-IP limit; "" when nobody did
	RequestID  string
	// To, Notice and InvitationID are the fields of the same name of EmailJob.
	To           string
	Notice       bool
	InvitationID string
}

// QueueEmail applies the limits and queues an email job. The job holds the
// kind, the user, the locale and the redirect: no address, no message and
// no token. A worker creates the token, renders the message and calls the
// delivery function. It returns ErrEmailNotConfigured in a realm without
// email, and *EmailLimitedError when a limit is reached: callers without a
// signed-in user skip the send silently, so the answer reveals nothing;
// callers with one answer 429. Without a UserID nothing is queued, but the
// limits are counted all the same, so a request about an address that has no
// account does the same work as one that has.
func (s *Users) QueueEmail(ctx context.Context, r EmailRequest) (Job, error) {
	e := s.Settings.Email
	if e == nil {
		return Job{}, ErrEmailNotConfigured
	}
	address, err := registry.NormalizeEmail(r.Address)
	if err != nil {
		return Job{}, err
	}
	sum := sha256.Sum256([]byte(address))
	to := "email:to:" + hex.EncodeToString(sum[:])
	checks := []struct {
		key    string
		limit  int
		window time.Duration
	}{
		{to + ":" + r.Kind, e.Limits.PerKindPerHour, time.Hour},
		{to, e.Limits.PerDay, 24 * time.Hour},
	}
	if ip := IPKey(r.ClientIP); ip != "" {
		checks = append([]struct {
			key    string
			limit  int
			window time.Duration
		}{{"email:" + ip, e.Limits.PerIPPerHour, time.Hour}}, checks...)
	}
	for _, c := range checks {
		if err := s.RateLimit(ctx, c.key, c.limit, c.window); err != nil {
			var te *ThrottledError
			if errors.As(err, &te) {
				return Job{}, &EmailLimitedError{RetryAfter: te.RetryAfter}
			}
			return Job{}, err
		}
	}
	if r.UserID == "" && r.InvitationID == "" {
		// Only the limits were applied: nobody gets a message.
		return Job{}, nil
	}
	database, function := e.DatabaseAndName()
	return s.EnqueueJob(ctx, Job{
		Database: database, Function: function, CallerActor: "backd", Origin: "backd:email." + r.Kind,
		TimeoutMS: e.Timeout.Milliseconds(), RequestID: r.RequestID,
		Email: &EmailJob{Kind: r.Kind, UserID: r.UserID, Locale: r.Locale, RedirectTo: r.RedirectTo, To: r.To, Notice: r.Notice, InvitationID: r.InvitationID},
	})
}
