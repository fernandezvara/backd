package email

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/net/html"
)

// SanitizeHTML makes the HTML of an owner template safe to put in an email (design/team-access.md
// §11): it keeps formatting tags, tables, links (https and mailto), images (https) and a short list
// of inline styles, and drops everything else: scripts, styles, forms, frames, event handlers,
// unknown tags (their text stays) and comments. The result is built from what was understood, not
// the input's own bytes, so what the parser misreads can't come through. removed lists, without
// repeats, what was dropped, for the person who wrote the template.
func SanitizeHTML(in string) (out string, removed []string) {
	var b strings.Builder
	seen := map[string]bool{}
	drop := func(what string) {
		if !seen[what] {
			seen[what] = true
			removed = append(removed, what)
		}
	}
	z := html.NewTokenizer(strings.NewReader(in))
	skipping := "" // inside an element whose content goes too
	var open []string
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.TextToken:
			if skipping == "" {
				b.WriteString(html.EscapeString(string(z.Text())))
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			if skipping != "" {
				continue
			}
			if dropContent[tag] {
				if tt == html.StartTagToken {
					skipping = tag
				}
				drop("<" + tag + ">")
				continue
			}
			if !allowedTags[tag] {
				drop("<" + tag + ">")
				continue
			}
			attrs := map[string]string{}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				attrs[string(k)] = string(v)
			}
			b.WriteString(openTag(tag, attrs, drop))
			if !voidTags[tag] && tt == html.StartTagToken {
				open = append(open, tag)
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if skipping != "" {
				if tag == skipping {
					skipping = ""
				}
				continue
			}
			if !allowedTags[tag] || voidTags[tag] {
				continue
			}
			// Close only what is open, so the output is balanced whatever the input did.
			for i, o := range slices.Backward(open) {
				if o == tag {
					for j := len(open) - 1; j >= i; j-- {
						b.WriteString("</")
						b.WriteString(open[j])
						b.WriteString(">")
					}
					open = open[:i]
					break
				}
			}
		case html.CommentToken, html.DoctypeToken:
			drop("comments and doctype")
		}
	}
	for _, o := range slices.Backward(open) {
		b.WriteString("</")
		b.WriteString(o)
		b.WriteString(">")
	}
	return b.String(), removed
}

var allowedTags = map[string]bool{
	"a": true, "b": true, "blockquote": true, "br": true, "code": true, "div": true, "em": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "hr": true, "i": true, "img": true, "li": true,
	"ol": true, "p": true, "pre": true, "small": true, "span": true, "strong": true, "table": true,
	"tbody": true, "td": true, "tfoot": true, "th": true, "thead": true, "tr": true, "u": true, "ul": true,
}

var voidTags = map[string]bool{"br": true, "hr": true, "img": true}

// dropContent are elements whose content is dropped with them.
var dropContent = map[string]bool{
	"script": true, "style": true, "iframe": true, "object": true, "embed": true, "form": true,
	"noscript": true, "template": true, "svg": true, "math": true, "textarea": true, "select": true,
	"title": true, "head": true,
}

// allowedAttrs are the attributes each tag keeps (style on every tag, filtered).
var allowedAttrs = map[string][]string{
	"a":     {"href", "title"},
	"img":   {"src", "alt", "width", "height", "title"},
	"table": {"width", "align", "cellpadding", "cellspacing", "border"},
	"td":    {"colspan", "rowspan", "width", "height", "align", "valign"},
	"th":    {"colspan", "rowspan", "width", "height", "align", "valign"},
	"tr":    {"align", "valign"},
	"div":   {"align"},
	"p":     {"align"},
}

func openTag(tag string, attrs map[string]string, drop func(string)) string {
	var b strings.Builder
	b.WriteString("<")
	b.WriteString(tag)
	for _, name := range allowedAttrs[tag] {
		v, ok := attrs[name]
		if !ok {
			continue
		}
		switch name {
		case "href":
			if !safeURL(v, "https", "mailto") {
				drop("a link that isn't https or mailto")
				continue
			}
		case "src":
			if !safeURL(v, "https") {
				drop("an image that isn't https")
				continue
			}
		case "width", "height", "colspan", "rowspan", "cellpadding", "cellspacing", "border":
			if !numeric.MatchString(v) {
				continue
			}
		case "align", "valign":
			if !alignment[strings.ToLower(v)] {
				continue
			}
		}
		b.WriteString(" ")
		b.WriteString(name)
		b.WriteString(`="`)
		b.WriteString(html.EscapeString(v))
		b.WriteString(`"`)
	}
	if style, ok := attrs["style"]; ok {
		if clean := safeStyle(style); clean != "" {
			b.WriteString(` style="`)
			b.WriteString(html.EscapeString(clean))
			b.WriteString(`"`)
		} else if strings.TrimSpace(style) != "" {
			drop("inline styles outside the allowed ones")
		}
	}
	if tag == "a" {
		b.WriteString(` rel="noopener noreferrer"`)
	}
	b.WriteString(">")
	return b.String()
}

var (
	numeric   = regexp.MustCompile(`^[0-9]{1,4}%?$`)
	alignment = map[string]bool{"left": true, "right": true, "center": true, "top": true, "middle": true, "bottom": true}
)

// safeURL reports whether v is an absolute URL with one of the schemes.
func safeURL(v string, schemes ...string) bool {
	v = strings.TrimSpace(v)
	u, err := url.Parse(v)
	if err != nil || u.Scheme == "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if slices.Contains(schemes, scheme) {
		return scheme != "https" && scheme != "http" || u.Host != ""
	}
	return false
}

var styleProperties = map[string]bool{
	"color": true, "background-color": true, "font-size": true, "font-weight": true, "font-style": true,
	"font-family": true, "text-align": true, "text-decoration": true, "line-height": true,
	"padding": true, "padding-top": true, "padding-bottom": true, "padding-left": true, "padding-right": true,
	"margin": true, "margin-top": true, "margin-bottom": true, "margin-left": true, "margin-right": true,
	"border": true, "border-top": true, "border-bottom": true, "border-left": true, "border-right": true,
	"border-radius": true, "width": true, "max-width": true, "height": true, "vertical-align": true,
}

// styleValue accepts words, numbers, units, colours and simple lists: no url(), no functions
// other than rgb(), no quotes beyond font names.
var styleValue = regexp.MustCompile(`^[#a-zA-Z0-9%.,\s'"-]+$|^rgba?\([0-9,.\s%]+\)$`)

// safeStyle keeps the declarations of a style attribute that are on the list and have plain values.
func safeStyle(style string) string {
	var out []string
	for decl := range strings.SplitSeq(style, ";") {
		prop, value, ok := strings.Cut(decl, ":")
		prop, value = strings.ToLower(strings.TrimSpace(prop)), strings.TrimSpace(value)
		if !ok || !styleProperties[prop] || value == "" || len(value) > 100 || !styleValue.MatchString(value) {
			continue
		}
		lower := strings.ToLower(value)
		if strings.Contains(lower, "expression") || strings.Contains(lower, "javascript") || strings.Contains(lower, "url") {
			continue
		}
		out = append(out, prop+": "+value)
	}
	return strings.Join(out, "; ")
}
