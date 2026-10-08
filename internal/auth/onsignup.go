package auth

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"time"
)

// OnSignupOrigin is the origin of the job that calls account.on_signup.
const OnSignupOrigin = "backd:account.on_signup"

// defaultOnSignupTimeout is the lease of the hook's job when the function declares none.
const defaultOnSignupTimeout = 30 * time.Second

// Profile fields a provider can give, in the order they are listed.
var profileFields = []string{"name", "given_name", "family_name", "picture"}

// CleanProfile keeps the known profile fields that are not empty, without control characters
// and cut to a sane length. It is what a provider (or the browser, for Apple's first
// authorization) says about the person: a hint for the app, never trusted.
func CleanProfile(in map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range profileFields {
		v := strings.Map(func(r rune) rune {
			if r < ' ' || r == 0x7f {
				return -1
			}
			return r
		}, strings.TrimSpace(in[k]))
		limit := 200
		if k == "picture" {
			limit = 2000
		}
		if r := []rune(v); len(r) > limit {
			v = string(r[:limit])
		}
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// OnSignupInput is what account.on_signup receives: the new user's id, how they signed up
// ("password", or a provider's name) and, from a provider, the profile it gave.
type OnSignupInput struct {
	UserID   string            `json:"user_id"`
	Provider string            `json:"provider"`
	Profile  map[string]string `json:"profile,omitempty"`
}

// queueOnSignup queues the call of the realm's account.on_signup function for a new user. The
// user is already created and the sign-up goes on whatever happens here: a failure is only
// logged, and the function itself is retried by the job mechanism (its `retry:`).
func (s *Users) queueOnSignup(ctx context.Context, u User, provider string, profile map[string]string) {
	if s.Settings.Account.OnSignup == "" {
		return
	}
	db, fn := s.Settings.Account.OnSignupDatabaseAndName()
	in := OnSignupInput{UserID: u.ID, Provider: provider}
	if len(profile) > 0 {
		in.Profile = maps.Clone(profile)
	}
	input, err := json.Marshal(in)
	if err == nil {
		timeout := s.Settings.Account.OnSignupTimeout
		if timeout <= 0 {
			timeout = defaultOnSignupTimeout
		}
		_, err = s.EnqueueJob(ctx, Job{
			Database: db, Function: fn, Input: input, CallerActor: "system", Origin: OnSignupOrigin,
			ActsAsFunction: true, TimeoutMS: timeout.Milliseconds(),
		})
	}
	if err != nil && s.Log != nil {
		s.Log.Error("could not queue account.on_signup", "user_id", u.ID, "error", err)
	}
}

// ProfileValues are the values of an on_signup job's profile, which the function's logs must not show.
func ProfileValues(input json.RawMessage) []string {
	var in OnSignupInput
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	var out []string
	for _, k := range profileFields {
		if v := in.Profile[k]; v != "" {
			out = append(out, v)
		}
	}
	return out
}
