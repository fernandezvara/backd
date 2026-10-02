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
	"strings"
)

// PagesDirName is the folder of a realm that holds its hosted pages: the
// pages the links in emails open (<realm>/pages/<kind>/<locale>.html).
const PagesDirName = "pages"

// The hosted pages. The first five are the forms a link opens; Result is
// shown after an action succeeds, InvalidToken for any link that doesn't
// work.
const (
	PageVerifyEmail        = "verify-email"
	PageResetPassword      = "reset-password"
	PageConfirmEmailChange = "confirm-email-change"
	PageRevertEmailChange  = "revert-email-change"
	PageAcceptInvitation   = "accept-invitation"
	PageResult             = "result"
	PageInvalidToken       = "invalid-token"
)

// PageKinds are the pages every realm that sends email has, in every
// language it lists.
var PageKinds = []string{PageVerifyEmail, PageResetPassword, PageConfirmEmailChange, PageRevertEmailChange, PageAcceptInvitation, PageResult, PageInvalidToken}

//go:embed all:pagedefaults
var pageDefaults embed.FS

// DefaultPage returns the English template of a hosted page.
func DefaultPage(kind string) ([]byte, error) {
	data, err := pageDefaults.ReadFile(path.Join("pagedefaults", kind, "en.html"))
	if err != nil {
		return nil, fmt.Errorf("no default page %q", kind)
	}
	return data, nil
}

// PageData is what a page template can use: {{.Realm}}, {{.Locale}},
// {{.Action}} (where its form posts), {{.Token}} (the hidden field),
// {{.Error}} and {{.ErrorCode}} (what to show on the form again), {{.Kind}} (which flow a result is for), {{.Email}} (the address the link is about),
// {{.Redirect}} and {{.DelaySeconds}} (where the result sends the user and
// when), {{.BackURL}} (a way back to the app on the error page).
type PageData struct {
	Realm  string
	Locale string
	Action string
	Token  string
	Error  string
	// ErrorCode names the problem in Error (password_mismatch, password_policy)
	// so a template can write it in its own words.
	ErrorCode string
	Kind      string
	// Email is the address the link is about: the new one for a change, the
	// previous one for an undo, the invited one for an invitation.
	Email string
	// Redirect and BackURL are checked against the realm's allowed_redirects
	// before they get here, so they may use an app's own scheme (acme://),
	// which html/template would otherwise refuse in a link.
	Redirect     htmltemplate.URL
	DelaySeconds int
	BackURL      htmltemplate.URL
}

// Pages are a realm's parsed hosted pages, held in memory.
type Pages struct {
	byKind map[string]map[string]*htmltemplate.Template // kind → locale → template
}

// Has reports whether the page exists in locale.
func (p *Pages) Has(kind, locale string) bool {
	_, ok := p.byKind[kind][locale]
	return ok
}

// Render renders a page in locale.
func (p *Pages) Render(kind, locale string, d PageData) (string, error) {
	tpl, ok := p.byKind[kind][locale]
	if !ok {
		return "", fmt.Errorf("no page %s/%s", kind, locale)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, d); err != nil {
		return "", fmt.Errorf("page %s/%s: %w", kind, locale, err)
	}
	return b.String(), nil
}

var samplePage = PageData{
	Realm: "sample", Locale: "en", Action: "/v1/sample/_auth/verify-email", Token: "sample", Error: "sample error",
	Kind: "verify-email", Redirect: "https://example.com/welcome", DelaySeconds: 3, BackURL: "https://example.com",
}

// LoadPages reads <realm>/pages/ (dir): <kind>/<locale>.html for every kind
// of PageKinds and every one of locales. It parses and test-renders each
// page, and reports every problem together, naming the files.
func LoadPages(dir string, locales []string) (*Pages, []error) {
	p := &Pages{byKind: map[string]map[string]*htmltemplate.Template{}}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return p, []error{fmt.Errorf("%s: this realm configures email but has no %s folder with the pages its links open (`backd template realm --realm <realm>` writes the defaults)", dir, PagesDirName)}
	}
	var errs []error
	for _, kind := range PageKinds {
		for _, loc := range locales {
			file := filepath.Join(dir, kind, loc+".html")
			data, err := os.ReadFile(file)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					errs = append(errs, fmt.Errorf("%s: missing (every listed locale needs every page: %s)", file, strings.Join(PageKinds, ", ")))
				} else {
					errs = append(errs, err)
				}
				continue
			}
			tpl, err := htmltemplate.New(kind + "/" + loc).Option("missingkey=error").Parse(string(data))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", file, err))
				continue
			}
			var b strings.Builder
			if err := tpl.Execute(&b, samplePage); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", file, err))
				continue
			}
			if p.byKind[kind] == nil {
				p.byKind[kind] = map[string]*htmltemplate.Template{}
			}
			p.byKind[kind][loc] = tpl
		}
	}
	return p, errs
}
