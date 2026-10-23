package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

// ErrEmailNotConfigured means the realm has no email section: it sends no
// email.
var ErrEmailNotConfigured = errors.New("this realm doesn't send email")

// EmailLimitedError means an email limit was reached: nothing was queued.
type EmailLimitedError struct {
	RetryAfter time.Duration
	// Scope says which limit it was: "recipient", "ip", "function" or
	// "invocation".
	Scope string
}

func (e *EmailLimitedError) Error() string {
	scope := ""
	if e.Scope != "" {
		scope = " (" + e.Scope + " limit)"
	}
	return fmt.Sprintf("too many emails%s; retry in %s", scope, e.RetryAfter.Round(time.Second))
}

// EmailRequest asks for one email to be sent to a user.
type EmailRequest struct {
	Kind       string // an email kind (verify-email, ...)
	UserID     string
	Address    string // the recipient, for the per-recipient limits only (never stored readable)
	Locale     string
	RedirectTo string // already checked against the realm's allowed redirects
	// Org is the company context of the message; when empty, the one recorded on the request's
	// context (WithOrgContext) is used.
	Org string
	// Draft is a template to render instead of the realm's (or the organization's saved) one: a
	// test send of an owner template. The job keeps it until job retention ends.
	Draft     *email.Override
	ClientIP  string // who caused it, for the per-IP limit; "" when nobody did
	RequestID string
	// Event and CodesLeft are the fields of the same name of EmailJob.
	Event       string
	CodesLeft   int
	ChallengeID string
	// To, Notice, InvitationID and OrgInvitationID are the fields of the same name of EmailJob.
	To              string
	Notice          bool
	InvitationID    string
	OrgInvitationID string
	// ExportID is the personal data export a data-export-ready or data-export-made email is
	// about: the worker takes the link and the expiry from its record.
	ExportID string
	// Custom marks an email a function asked for; Invocation is the
	// invocation that asked (the parent of the job, and what the per-invocation
	// cap counts). Address is then the user's, for to_user; Custom holds the
	// addresses of the others.
	Custom     *CustomEmail
	Invocation string
	Depth      int
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
	type check struct {
		key, scope string
		limit      int
		window     time.Duration
	}
	var checks []check
	if c := r.Custom; c != nil {
		// A function's caps, whoever the recipients are.
		checks = append(checks,
			check{"email:fn:" + c.Function, "function", e.Limits.PerFunctionPerHour, time.Hour},
			check{"email:inv:" + r.Invocation, "invocation", e.Limits.PerInvocation, time.Hour})
	}
	if ip := IPKey(r.ClientIP); ip != "" {
		checks = append(checks, check{"email:" + ip, "ip", e.Limits.PerIPPerHour, time.Hour})
	}
	addresses := []string{}
	if r.Address != "" {
		addresses = append(addresses, r.Address)
	}
	if r.Custom != nil {
		addresses = append(append(append(addresses, r.Custom.To...), r.Custom.CC...), r.Custom.BCC...)
	}
	for _, a := range addresses {
		address, err := registry.NormalizeEmail(a)
		if err != nil {
			return Job{}, err
		}
		sum := sha256.Sum256([]byte(address))
		to := "email:to:" + hex.EncodeToString(sum[:])
		checks = append(checks, check{to + ":" + r.Kind, "recipient", e.Limits.PerKindPerHour, time.Hour}, check{to, "recipient", e.Limits.PerDay, 24 * time.Hour})
	}
	for _, c := range checks {
		if err := s.RateLimit(ctx, c.key, c.limit, c.window); err != nil {
			var te *ThrottledError
			if errors.As(err, &te) {
				s.Metrics.RateLimited(s.Realm, "email_"+c.scope)
				return Job{}, &EmailLimitedError{RetryAfter: te.RetryAfter, Scope: c.scope}
			}
			return Job{}, err
		}
	}
	if r.UserID == "" && r.InvitationID == "" && r.OrgInvitationID == "" && r.Custom == nil && r.ChallengeID == "" {
		// Only the limits were applied: nobody gets a message.
		return Job{}, nil
	}
	database, function := e.DatabaseAndName()
	if r.Org == "" {
		r.Org = OrgContextOf(ctx)
	}
	job := Job{
		Database: database, Function: function, CallerActor: "backd", Origin: "backd:email." + r.Kind,
		TimeoutMS: e.Timeout.Milliseconds(), RequestID: r.RequestID,
		Email: &EmailJob{Kind: r.Kind, UserID: r.UserID, Locale: r.Locale, RedirectTo: r.RedirectTo, Org: r.Org, Draft: r.Draft, To: r.To, Notice: r.Notice, InvitationID: r.InvitationID, OrgInvitationID: r.OrgInvitationID, ExportID: r.ExportID, Event: r.Event, CodesLeft: r.CodesLeft, ChallengeID: r.ChallengeID, Custom: r.Custom},
	}
	if r.Custom != nil {
		job.CallerActor, job.Origin = "function:"+r.Custom.Function, "function:"+r.Custom.Function
		job.ParentID, job.Depth = r.Invocation, r.Depth
	}
	return s.EnqueueJob(ctx, job)
}
