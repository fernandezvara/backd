// Package email holds what backd knows about the emails it asks a realm's
// delivery function to send: the kinds, the templates (loaded from
// <realm>/email/ into memory at startup and rendered per message), and the
// shape of the message handed to the function. backd never sends mail itself.
package email

import (
	"embed"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	texttemplate "text/template"
	"time"
)

// DirName is the folder of a realm that holds its email templates.
const DirName = "email"

// The kinds of email backd sends on its own.
const (
	VerifyEmail     = "verify-email"
	ResetPassword   = "reset-password"
	AccountExists   = "account-exists"
	PasswordChanged = "password-changed"
	Welcome         = "welcome"
	ChangeEmail     = "change-email"
	EmailChanged    = "email-changed"
	Invitation      = "invitation"
)

// SystemKinds are the kinds backd knows; `backd template realm` writes an
// English template for each.
var SystemKinds = []string{VerifyEmail, ResetPassword, AccountExists, PasswordChanged, Welcome, ChangeEmail, EmailChanged, Invitation}

// RequiredKinds must have a template in every realm that configures email.
var RequiredKinds = []string{VerifyEmail, ResetPassword, AccountExists, PasswordChanged}

// Purpose is what a token created for an email can be redeemed for.
type Purpose string

// TokenPurpose returns the token an email of this kind carries, or "" when
// the kind has no link with a token.
func TokenPurpose(kind string) Purpose {
	switch kind {
	case VerifyEmail:
		return "verify-email"
	case ResetPassword:
		return "reset-password"
	case ChangeEmail:
		return "change-email"
	case EmailChanged:
		return "revert-email-change"
	case Invitation:
		return "invitation"
	}
	return ""
}

// LinkPath is the route of backd that a link of this purpose opens, under
// /v1/<realm>/_auth/.
func (p Purpose) LinkPath() string {
	switch p {
	case "change-email":
		return "confirm-email-change"
	case "invitation":
		return "accept-invitation"
	}
	return string(p)
}

//go:embed all:defaults
var defaults embed.FS

// DefaultFiles returns the English template files of a system kind, by
// file name (en.subject.txt, en.txt, en.html).
func DefaultFiles(kind string) (map[string][]byte, error) {
	entries, err := fs.ReadDir(defaults, path.Join("defaults", kind))
	if err != nil {
		return nil, fmt.Errorf("no default template for email kind %q", kind)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		data, err := defaults.ReadFile(path.Join("defaults", kind, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = data
	}
	return out, nil
}

// User is who a message is for.
type User struct {
	Email  string
	Locale string
}

// Data is what a template can use: {{.Realm}}, {{.User.Email}},
// {{.User.Locale}}, {{.Link}}, {{.ExpiresAt}} and {{.Data}}. Templates never
// receive text written by whoever caused the email, so backd can't be used
// to send someone else's words.
type Data struct {
	Realm     string
	User      User
	Link      string
	ExpiresAt time.Time
	Data      map[string]any
}

// Message is a rendered email.
type Message struct {
	Subject string
	Text    string
	HTML    string
}

// template is one kind in one locale.
type template struct {
	subject *texttemplate.Template
	text    *texttemplate.Template
	html    *htmltemplate.Template
}

// Templates are a realm's parsed templates, held in memory.
type Templates struct {
	byKind map[string]map[string]*template // kind → locale → template
}

// Kinds lists the kinds that have templates, sorted.
func (t *Templates) Kinds() []string {
	out := make([]string, 0, len(t.byKind))
	for k := range t.byKind {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Has reports whether kind has a template in locale.
func (t *Templates) Has(kind, locale string) bool {
	_, ok := t.byKind[kind][locale]
	return ok
}

// Render renders kind in locale.
func (t *Templates) Render(kind, locale string, d Data) (Message, error) {
	tpl, ok := t.byKind[kind][locale]
	if !ok {
		return Message{}, fmt.Errorf("no email template %s/%s", kind, locale)
	}
	var subject, text, html strings.Builder
	if err := tpl.subject.Execute(&subject, d); err != nil {
		return Message{}, fmt.Errorf("email template %s/%s subject: %w", kind, locale, err)
	}
	if err := tpl.text.Execute(&text, d); err != nil {
		return Message{}, fmt.Errorf("email template %s/%s text: %w", kind, locale, err)
	}
	if err := tpl.html.Execute(&html, d); err != nil {
		return Message{}, fmt.Errorf("email template %s/%s html: %w", kind, locale, err)
	}
	// A subject is one line.
	oneLine := strings.Join(strings.Fields(subject.String()), " ")
	return Message{Subject: oneLine, Text: text.String(), HTML: html.String()}, nil
}

var (
	kindPattern   = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)
	localePattern = regexp.MustCompile(`^[a-z]{2,3}(?:-[A-Za-z0-9]{2,8})*$`)
)

// ValidLocale reports whether v looks like a BCP 47 language tag (es, es-MX).
func ValidLocale(v string) bool { return localePattern.MatchString(v) }

// sample is what a template is checked with at startup.
var sample = Data{
	Realm:     "sample",
	User:      User{Email: "ana@example.com", Locale: "en"},
	Link:      "https://example.com/v1/sample/_auth/verify-email?token=sample",
	ExpiresAt: time.Date(2030, 1, 2, 15, 4, 0, 0, time.UTC),
	Data:      map[string]any{},
}

// Load reads <realm>/email/ (dir): one folder per kind, with
// <locale>.subject.txt, <locale>.txt and <locale>.html per locale. It parses
// and test-renders every template, requires the RequiredKinds, and requires
// every kind found to be complete in every one of locales. Problems are
// reported together, naming the files.
func Load(dir string, locales []string) (*Templates, []error) {
	t := &Templates{byKind: map[string]map[string]*template{}}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return t, []error{fmt.Errorf("%s: this realm configures email but has no %s folder with templates (`backd template realm --realm <realm>` writes the defaults)", dir, DirName)}
	}
	if err != nil {
		return t, []error{err}
	}
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		kind := e.Name()
		kdir := filepath.Join(dir, kind)
		if !kindPattern.MatchString(kind) {
			errs = append(errs, fmt.Errorf("%s: invalid email kind name %q: use lowercase letters and digits, optionally separated by single '-' or '_'", kdir, kind))
			continue
		}
		for _, loc := range locales {
			tpl, kerrs := loadOne(kdir, kind, loc)
			errs = append(errs, kerrs...)
			if tpl != nil {
				if t.byKind[kind] == nil {
					t.byKind[kind] = map[string]*template{}
				}
				t.byKind[kind][loc] = tpl
			}
		}
	}
	for _, kind := range RequiredKinds {
		if _, ok := t.byKind[kind]; !ok {
			if _, statErr := os.Stat(filepath.Join(dir, kind)); statErr != nil {
				errs = append(errs, fmt.Errorf("%s: the email kind %q has no template (required when email is configured)", filepath.Join(dir, kind), kind))
			}
		}
	}
	return t, errs
}

func loadOne(kdir, kind, locale string) (*template, []error) {
	var errs []error
	read := func(name string) string {
		p := filepath.Join(kdir, locale+"."+name)
		data, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("%s: missing (every listed locale needs every template: %s.subject.txt, %s.txt, %s.html)", p, locale, locale, locale))
			} else {
				errs = append(errs, err)
			}
			return ""
		}
		return string(data)
	}
	subject, text, html := read("subject.txt"), read("txt"), read("html")
	if len(errs) > 0 {
		return nil, errs
	}
	name := kind + "/" + locale
	tpl := &template{}
	var err error
	if tpl.subject, err = texttemplate.New(name + ".subject").Option("missingkey=error").Parse(subject); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", filepath.Join(kdir, locale+".subject.txt"), err))
	}
	if tpl.text, err = texttemplate.New(name + ".txt").Option("missingkey=error").Parse(text); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", filepath.Join(kdir, locale+".txt"), err))
	}
	if tpl.html, err = htmltemplate.New(name + ".html").Option("missingkey=error").Parse(html); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", filepath.Join(kdir, locale+".html"), err))
	}
	if len(errs) > 0 {
		return nil, errs
	}
	// Run each once, to catch a field that doesn't exist.
	one := &Templates{byKind: map[string]map[string]*template{kind: {locale: tpl}}}
	if _, err := one.Render(kind, locale, sample); err != nil {
		return nil, []error{fmt.Errorf("%s: %w", kdir, err)}
	}
	return tpl, nil
}
