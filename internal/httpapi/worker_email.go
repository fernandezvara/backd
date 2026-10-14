package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
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
		orgInvite               *auth.OrgInvitationEmail
		notice                  = job.Email.Notice
	)
	custom := job.Email.Custom
	if custom != nil && job.Email.UserID == "" {
		// A function's email to addresses: no user to look up.
		if len(custom.To) == 0 {
			w.fail(ctx, log, svc, job, "the email has no recipient")
			return nil, nil, false
		}
		address, locale = custom.To[0], job.Email.Locale
	} else if job.Email.ChallengeID != "" && job.Email.UserID == "" {
		// A request for a sign-in link of an address that has no account: the address is in
		// the request, sealed.
		_, to, err := svc.MagicLinkEmailFor(ctx, job.Email.ChallengeID)
		switch {
		case err == auth.ErrNotFound:
			w.fail(ctx, log, svc, job, "the request has expired")
			return nil, nil, false
		case err != nil || to == "":
			log.Warn("read the request's address; leaving the job to be retried", "error", err)
			return nil, nil, false
		}
		address, locale = to, job.Email.Locale
	} else if job.Email.OrgInvitationID != "" {
		// An invitation to an organization: the recipient and the link are the invitation's.
		oi, err := svc.OrgInvitationEmail(ctx, job.Email.OrgInvitationID)
		switch {
		case errors.Is(err, auth.ErrOrgInvitationGone):
			w.fail(ctx, log, svc, job, "the invitation was revoked or has ended")
			return nil, nil, false
		case err != nil:
			log.Warn("read the invitation; leaving the job to be retried", "error", err)
			return nil, nil, false
		}
		orgInvite, address, locale = &oi, oi.Address, job.Email.Locale
	} else if job.Email.InvitationID != "" {
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
		locale, _ = svc.Settings.Languages()
	}
	data := email.Data{Realm: realm, User: email.User{Email: address, Locale: locale}, Data: map[string]any{}}
	extra := map[string]any{}
	if custom != nil && custom.Data != nil {
		// What the function gave, for the template and for the delivery function.
		data.Data, extra = custom.Data, maps.Clone(custom.Data)
	}
	if job.Email.Event != "" {
		// A security notice: which change it reports (backd's own words, nothing secret).
		data.Data["event"], extra["event"] = job.Email.Event, job.Email.Event
		data.Data["codes_left"], extra["codes_left"] = job.Email.CodesLeft, job.Email.CodesLeft
	}
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
	if orgInvite != nil {
		// The link carries the invitation's own token, which was sealed for this message.
		if orgInvite.Link == "" {
			w.fail(ctx, log, svc, job, "the realm sets no organizations.invitation_link")
			return nil, nil, false
		}
		data.Link, data.ExpiresAt = orgInvite.Link, orgInvite.ExpiresAt.UTC()
		data.Data["org_name"], data.Data["roles"] = orgInvite.OrgName, orgInvite.Roles
		extra["link"], extra["expires_at"] = data.Link, data.ExpiresAt.Format(time.RFC3339)
		extra["org_name"], extra["roles"] = orgInvite.OrgName, orgInvite.Roles
		mask = append(mask, orgInvite.Token)
	}
	if kind == email.MagicLink && job.Email.ChallengeID != "" {
		// The link and the code are made now and only their hashes are kept; a retried job
		// makes new ones.
		base := es.PublicURL
		if base == "" {
			base = w.backdURL
		}
		if base == "" {
			w.fail(ctx, log, svc, job, "backd's public address is unknown (BACKD_URL or email.public_url)")
			return nil, nil, false
		}
		token, code, expires, err := svc.MakeMagicLinkSecrets(ctx, job.Email.ChallengeID)
		switch {
		case errors.Is(err, auth.ErrNotFound):
			w.fail(ctx, log, svc, job, "the request has expired")
			return nil, nil, false
		case err != nil:
			log.Warn("make the sign-in link; leaving the job to be retried", "error", err)
			return nil, nil, false
		}
		data.Link = strings.TrimRight(base, "/") + "/v1/" + realm + "/_auth/magic-link?token=" + token
		data.Data["code"], data.ExpiresAt = code, expires.UTC()
		// A delivery function with its own templates needs the same values the default ones use.
		extra["link"], extra["code"], extra["expires_at"] = data.Link, code, data.ExpiresAt.Format(time.RFC3339)
		mask = append(mask, token, code)
	}
	if kind == email.ReauthCode && job.Email.ChallengeID != "" {
		// The code is made now and only its hash is kept; a retried job makes a new one.
		code, expires, err := svc.MakeReauthCode(ctx, job.Email.ChallengeID)
		switch {
		case errors.Is(err, auth.ErrNotFound):
			w.fail(ctx, log, svc, job, "the request for a code has expired")
			return nil, nil, false
		case err != nil:
			log.Warn("make the re-authentication code; leaving the job to be retried", "error", err)
			return nil, nil, false
		}
		data.Data["code"], data.ExpiresAt = code, expires.UTC()
		extra["code"], extra["expires_at"] = code, data.ExpiresAt.Format(time.RFC3339)
		mask = append(mask, code)
	}
	msg, err := tpl.Render(kind, locale, data)
	if err != nil {
		w.fail(ctx, log, svc, job, "could not render the email: "+err.Error())
		return nil, nil, false
	}
	toList, ccList, bccList := []emailAddress{{Email: address}}, []emailAddress{}, []emailAddress{}
	if custom != nil {
		for i, a := range custom.To {
			if i > 0 || job.Email.UserID != "" {
				toList = append(toList, emailAddress{Email: a})
			}
		}
		for _, a := range custom.CC {
			ccList = append(ccList, emailAddress{Email: a})
		}
		for _, a := range custom.BCC {
			bccList = append(bccList, emailAddress{Email: a})
		}
	}
	in := emailInput{
		ID: job.ID, Kind: kind, From: es.From, To: toList, CC: ccList, BCC: bccList,
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
