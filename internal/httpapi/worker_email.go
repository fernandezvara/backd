package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/email"
)

// emailInput is what the delivery function receives as ctx.input: a finished
// message. It exists only in memory and at the provider: backd stores none
// of it.
type emailInput struct {
	ID      string         `json:"id"` // the email job's id: the same on every retry
	Kind    string         `json:"kind"`
	From    string         `json:"from"`
	ReplyTo *string        `json:"reply_to"`
	To      []emailAddress `json:"to"`
	CC      []emailAddress `json:"cc"`
	BCC     []emailAddress `json:"bcc"`
	Subject string         `json:"subject"`
	Text    string         `json:"text"`
	HTML    string         `json:"html"`
	Locale  string         `json:"locale"`
	Data    map[string]any `json:"data"`
}

type emailAddress struct {
	Email string  `json:"email"`
	Name  *string `json:"name"`
}

// prepareEmail makes the message of an email job: it finds the user, creates
// the token (storing only its hash), renders the templates in the user's
// language and returns the delivery function's input, plus the token to
// keep out of the function's logs. ok is false when the job was ended (a
// condition that won't change) or must be retried later.
func (w *Worker) prepareEmail(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) (input json.RawMessage, mask []string, ok bool) {
	rl := w.reg.Realms[realm]
	if rl == nil || rl.Settings.Email == nil || rl.Email == nil {
		w.fail(ctx, log, svc, job, "the realm no longer configures email")
		return nil, nil, false
	}
	es, tpl := rl.Settings.Email, rl.Email
	kind := job.Email.Kind
	// Who the message is for, in which language, and what its token acts on.
	var (
		userID, address, locale string
		invitationID            string
		notice                  = job.Email.Notice
	)
	if job.Email.InvitationID != "" {
		// An invitation: there is no user yet, the invited address is the
		// recipient, and the token lives as long as the invitation.
		inv, found, err := svc.InvitationByID(ctx, job.Email.InvitationID)
		if err != nil {
			log.Warn("find the invitation; leaving the job to be retried", "error", err)
			return nil, nil, false
		}
		if !found {
			w.fail(ctx, log, svc, job, "the invitation was revoked or has expired")
			return nil, nil, false
		}
		invitationID, address = inv.ID, inv.Email
		locale = job.Email.Locale
	} else {
		user, err := svc.UserByID(ctx, job.Email.UserID)
		if err != nil {
			if err == auth.ErrNotFound {
				w.fail(ctx, log, svc, job, "the user no longer exists")
			} else {
				log.Warn("find the email's user; leaving the job to be retried", "error", err)
			}
			return nil, nil, false
		}
		if user.Disabled {
			w.fail(ctx, log, svc, job, "the user is disabled")
			return nil, nil, false
		}
		userID, address = user.ID, user.Email
		switch job.Email.To {
		case "pending":
			address = user.PendingEmail
		case "previous":
			address = user.PreviousEmail
		}
		if address == "" {
			w.fail(ctx, log, svc, job, "there is no address to send this message to any more")
			return nil, nil, false
		}
		// The language the request asked for, else the user's own, else the default.
		if locale = job.Email.Locale; locale == "" {
			locale = svc.LocaleOf(user)
		}
	}
	if !tpl.Has(kind, locale) {
		locale = es.DefaultLocale
	}
	data := email.Data{Realm: realm, User: email.User{Email: address, Locale: locale}, Data: map[string]any{}}
	extra := map[string]any{}
	if purpose := email.TokenPurpose(kind); purpose != "" && !notice {
		base := es.PublicURL
		if base == "" {
			base = w.backdURL
		}
		if base == "" {
			w.fail(ctx, log, svc, job, "backd's public address is unknown (BACKD_URL or email.public_url)")
			return nil, nil, false
		}
		spec := auth.EmailToken{Purpose: string(purpose), UserID: userID, RedirectTo: job.Email.RedirectTo, InvitationID: invitationID}
		if job.Email.To != "" || invitationID != "" {
			spec.Address = address // what the link acts on
		}
		ttl := svc.Settings.Account.TokenLifetime(purpose)
		if invitationID != "" {
			inv, _, _ := svc.InvitationByID(ctx, invitationID)
			ttl = inv.ExpiresAt.Sub(svc.Clock())
		}
		token, expires, err := svc.NewEmailTokenFor(ctx, spec, ttl)
		if err != nil {
			log.Warn("create the email's token; leaving the job to be retried", "error", err)
			return nil, nil, false
		}
		data.Link = strings.TrimRight(base, "/") + "/v1/" + realm + "/_auth/" + purpose.LinkPath() + "?token=" + token
		if own := es.Links[flowKey(purpose)]; own != "" {
			// The app hosts this page: the link goes to it, and it posts the token as JSON.
			data.Link = strings.Replace(own, "{token}", token, 1)
		}
		data.ExpiresAt = expires.UTC()
		extra["link"] = data.Link
		extra["expires_at"] = data.ExpiresAt.Format(time.RFC3339)
		mask = []string{token}
	}
	msg, err := tpl.Render(kind, locale, data)
	if err != nil {
		w.fail(ctx, log, svc, job, "could not render the email: "+err.Error())
		return nil, nil, false
	}
	in := emailInput{
		ID: job.ID, Kind: kind, From: es.From, To: []emailAddress{{Email: address}}, CC: []emailAddress{}, BCC: []emailAddress{},
		Subject: msg.Subject, Text: msg.Text, HTML: msg.HTML, Locale: locale, Data: extra,
	}
	if es.ReplyTo != "" {
		in.ReplyTo = &es.ReplyTo
	}
	input, err = json.Marshal(in)
	if err != nil {
		w.fail(ctx, log, svc, job, "could not encode the email")
		return nil, nil, false
	}
	return input, mask, true
}
