# Design: breach response and password expiry

- **Issues:** features 29.2–29.6, docs 29.7, review 29.8 (milestone `29-breach-response`)
- **Status:** decided after full code review; security review done (§11)
- **Related designs:** [account-lifecycle.md](./account-lifecycle.md) (reset, change, `password-changed`), [mfa-passwordless.md](./mfa-passwordless.md) (reauth, session methods), [email-delivery.md](./email-delivery.md) (kinds, limits, queue), [export-import.md](./export-import.md) (packages hold password hashes)

## 1. The problem

A backup leaks, or a credential dump of the realm turns up. The hashes in it are argon2id, so each is slow to guess, but every one that is guessed opens an account until its user changes the password. The operator needs to **make every existing password useless, end every session, and get each user to choose a new password on their own**, without touching anyone's password and without hand work per user.

What exists today:

| Need | Today |
|---|---|
| Users choose a password themselves | Yes: `POST /_auth/reset-password/request`, an emailed link, a hosted form (needs `email` in `realm.yaml`) |
| Make a leaked hash useless | One user at a time: `backd user set-password` (also ends that user's sessions) |
| End every session | A `mongosh` command in the backup guide |
| Tell every user | Nothing: no bulk message, and a user whose password stopped working sees "invalid credentials" |
| Reach a user without working email | Nothing |

The backup guide used to say reset emails could not be sent; that was wrong, and is corrected. The gap is the **bulk, forced, self-service** path.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Mechanism | A realm **password epoch**: a timestamp in the realm's system data; a password set before it is expired. One write expires every password, including future restores of old data |
| Evidence | Each password identity records `password_changed_at` (set when the password is created, reset, changed or set by an administrator); an identity without it counts as changed at its creation |
| Sign-in | A **correct** expired password gets no session: `401 password_expired`, and a reset email is queued (the proof of knowing the password makes the hint safe); a wrong one is unchanged `invalid_credentials`. Providers, magic links and sessions of other methods are untouched |
| Reset | A reset (or an administrator's set-password) sets a new `password_changed_at`: the account is back, with no flag to clear |
| Bulk action | `backd user expire-passwords`: sets the epoch, ends every session, optionally notifies; `--email` expires one user only (a per-user epoch on the user) |
| Notification | A new email kind `password-expired`, customizable like the others, that carries a reset link; sent by a worker job paced inside `email.limits`, one message per user per epoch, resumable |
| No email | `backd user reset-link --email`: an administrator gets a one-time link (the reset token) to hand over out of band, audited |
| Sessions | `backd user revoke-sessions --all` (and an admin API route) replaces the `mongosh` step |
| Audit | `password.expire` (scope, count, reason, notify), `password.expire_notify`, `session.revoke_all`, `password.reset_link` |
| Clients | The JS client raises `PasswordExpiredError`; the admin UI gets the action; Dart follows its own milestone |

## 3. The epoch (issue 29.2)

- **State:** one document in the realm's system database (`security_state`, `_id: "password_epoch"`) with `valid_after`, who set it, when and the reason. Not configuration: it is an operator's decision at a moment, audited, and survives deployments.
- **Check:** at password sign-in and at password re-authentication, after the hash verifies: `password_changed_at < valid_after` (or the user's own `password_expired_at`) means expired. Verification still costs one hash, like any other attempt, so the response time tells nothing.
- **Why an epoch and not a flag per user:** it is one write whatever the number of users, it can't miss a user created while the job runs, and an operator who restores an old backup after expiring passwords doesn't revive old passwords (the epoch is in the same system database, so a restore of *older* system data must be followed by running the command again; the docs say so).
- **Per user:** `password_expired_at` on the user, for `--email` and for support cases; the same check.
- **Who is affected:** accounts with a password identity. Users who only use providers or magic links have nothing to expire.
- **MFA:** an expired password doesn't skip the second factor; it is refused before the second step starts.

## 4. Telling the users (issue 29.4)

- **Kind `password-expired`:** a system kind with default templates in the realm's locales, rendered with the same data as `reset-password` plus the reason text the operator gave. The link is a reset token for the user (purpose `reset-password`), so the user can act in one click; it expires like any reset link.
- **The job:** a worker walks the users with a password identity in id order, skipping disabled users and addresses that are not verified, queues one message each through the normal email queue (so `email.limits` and the provider's own pacing apply), records `notified_epoch` on the user so a repeat or a retry sends nothing twice, reports steps and totals (f6), and can be cancelled and resumed. Progress and counts: `backd functions jobs`, the admin API job detail.
- **A user who didn't get it** types the address into *Forgot password* (always possible) or tries to sign in, which queues the same message (§2).

## 5. Without email (issue 29.5)

`POST /_admin/users/{id}/reset-link` makes a reset token and returns the link once, nothing is emailed; `backd user reset-link --realm r --email e`. Needs full administration and reauth (it is a way into the account), is audited, and the user's other sessions are untouched until the link is used. It is also what a support desk needs when a user lost access to their mailbox, once the address is changed.

## 6. Sessions and keys

- `backd user revoke-sessions --realm r --all` deletes every session of the realm (`--email` for one user already exists through disable and set-password). API keys stay with `backd apikey revoke`/create; the runbook lists them. Secrets are rotated with `backd secret rotate-key`. Field keys ([field-encryption.md](./field-encryption.md)) are secrets too: `rotate-key` re-seals them under the new master key, but a master key leaked together with the realm's secrets exposes them as well, so the runbook rotates each field key then (`backd secret rotate`, field-encryption §5.3, §16).
- `expire-passwords` ends sessions by default; `--keep-sessions` leaves them (for a planned rotation, not a breach).

## 7. Security considerations

- The `password_expired` answer appears only after a correct password, so it doesn't reveal whether an address exists or its password.
- The email sent on a refused sign-in is rate-limited per address and per IP like a reset request, and counts in `email.limits`.
- An epoch can't be moved backwards through the API, only replaced with a later one (`backd user expire-passwords --at <time>` for the future is out of scope). Clearing is a restore from a backup of the system database.
- The reset link of 29.5 is as strong as a user's mailbox: the audit record names the administrator and the user, never the token.

## 8. Issues

| id | Issue | Depends on |
|---|---|---|
| 29.1 | Design: breach response and password expiry | — |
| 29.2 | Password expiry: the epoch and the refused sign-in | 29.1 |
| 29.3 | Expire every password and end every session | 29.2 |
| 29.4 | Tell users to choose a new password | 29.3 |
| 29.5 | Reset links issued by an administrator | 29.2 |
| 29.6 | Password expiry in the JavaScript client and the admin UI | 29.3 |
| 29.7 | Breach response documentation | 29.4, 29.5, 29.6 |
| 29.8 | Security review of breach response | 29.7 |

## 9. Alternatives considered

- **A flag per user set by a bulk job:** rejected for the epoch: it needs a pass over every user before it is effective, can miss users created meanwhile and is far slower to turn on in an emergency.
- **Clearing every password hash:** rejected: it destroys the evidence for audit and leaves the account in the same state as a passwordless one, with no message to the user.
- **Forcing a change at the next sign-in (with the old password as proof):** rejected for a breach, because the old password is exactly what may be known to someone else; a reset through the mailbox proves a different thing.
- **A standing `password.max_age` policy:** a different feature (periodic rotation); the epoch makes it easy later.

## 10. Follow-ups

- `password.max_age` and a warning before it.
- Dart client support in its own milestone.
- A field-key section in the runbook, written with field encryption's documentation (16.8).
- Breach lists (k-anonymity checks against known-compromised passwords) at sign-up and reset.

## 11. Security review (issue 29.8)

Reviewed after 29.7, against the code. Fixed (with tests):

- **A pending second step survived the expiry.** A challenge made by a correct password (a second-factor step, an enrollment, consents to accept) could be completed after the passwords were expired and gave a session. Finishing any of them now re-checks the password when the first factor was one (`refuseExpiredFactor`), so `password_expired` is also the answer there.
- **Reset links outlived a reset.** Using one reset link left the user's other reset links valid, which matters more now that the notification link lasts a day and an administrator can make more. Setting a password with a link uses up all of the user's reset links.
- **Anonymized copies inherited production's password history.** An anonymized export now gives the staging password a `password_changed_at` of the export and leaves out `password_expired_at` and `password_notified_at`, so an epoch in the staging realm does not expire the staging password.
- **The reset link stayed in the admin interface's memory** after its dialog closed; it is cleared on close.

Checked and sound: a wrong password, an unknown address, a disabled user and a login outside the user's networks all answer `invalid_credentials` after the same hash; `password_expired` follows only a verified password; the epoch moves forward only (a conditional upsert, and a clock behind answers `409`); expiring every password, ending every session, notifying and making reset links need full administration and a fresh proof for sessions, and one user's expiry and reset link follow the rules that stop an administrator acting on someone with more rights (tested); the link of a reset link is built from `BACKD_URL`/`email.public_url`, never from a request header, and neither it nor a token reaches the audit trail or a log; the notification marks a user told only after the message is queued and walks users in id order in batches of a hundred, so a repeat, a cancel, or a worker that dies and is replaced writes to nobody twice.

Accepted, and documented where it matters:

- **The refused sign-in is an oracle for a correct password**, and queues an email to the account's owner. That is what the design chose: the owner is told when someone has their old password. The email is limited per address and per client like a reset request.
- **A crash between queueing a message and marking the user leaves one duplicate**, in preference to a lost message.
- **A notification email that is not delivered within the job retention (48 hours) is dropped**, and its user was marked told; they can use *Forgot password*.
- **A sign-in or a reset in flight at the instant of the expiry** can finish on the wrong side of it: a session created a millisecond after `DeleteAllSessions`, or a password set a millisecond before the epoch counted as expired. The runbook says to run `revoke-sessions` again when in doubt; the user resets again.
- **Clocks between instances** must agree (as they must for every token): an instance whose clock runs ahead sets an epoch in the others' future, and passwords set meanwhile count as expired until then.
- **`--keep-sessions` leaves stolen sessions alive**, by design (a planned rotation, not a breach).
- **A user with an expired password and a live session is offered password re-authentication**, which refuses; they reset instead.
- **Login codes and OAuth states are not purged** by ending every session: they are minted by a provider sign-in, last a minute and are single-use.
