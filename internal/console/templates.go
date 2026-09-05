package console

import (
	"fmt"
	"html/template"
	"io"
	"strings"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi/middleware"
)

// Layout is the data every page template has access to via the parent "layout"
// definition (nav, asset URLs, the CSP nonce for inline <script>/<style> tags).
type Layout struct {
	Title      string
	Nonce      string
	AppCSSURL  string
	AppJSURL   string
	HtmxURL    string
	CSRFHeader string
	Section    string // "codes" or "tokens": which primary nav entry is current
	User       *UserView
	Content    any // the page-specific data, exposed to the "content" block
}

// UserView is the minimal identity information a template ever renders. It deliberately
// exposes nothing sensitive.
type UserView struct {
	Email   string
	Initial string // first letter of the email, for the header avatar chip
	IsAdmin bool
}

func newUserView(u domain.User) *UserView {
	initial := "?"
	if u.Email != "" {
		initial = strings.ToUpper(u.Email[:1])
	}
	return &UserView{Email: u.Email, Initial: initial, IsAdmin: u.IsAdmin}
}

var templateFuncs = template.FuncMap{
	"trendChart": renderTrendChart,
	"codeMode":   codeMode,
	"relTime":    relTime,
}

// relTime renders t as a short, human relative time ("3h ago", "12d ago"); older than
// about two months it falls back to the calendar date. Templates pair it with a
// <time datetime> so the exact instant is still available on hover and to tooling. It
// accepts both time.Time and *time.Time (domain.APIToken.LastUsedAt is a pointer).
func relTime(v any) string {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case *time.Time:
		if x == nil {
			return "never"
		}
		t = *x
	default:
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2 Jan 2006")
	}
}

// sectionForPage maps a page template to the primary nav entry it belongs under.
func sectionForPage(page string) string {
	switch {
	case strings.HasPrefix(page, "token"):
		return "tokens"
	case strings.HasPrefix(page, "code"):
		return "codes"
	}
	return ""
}

// templateSet parses layout.html once and clones it per page, so each page's
// {{define "content"}} block does not collide with any other page's.
type templateSet struct {
	pages map[string]*template.Template
}

var pageFiles = []string{
	"signin.html",
	"codes_list.html",
	"code_new.html",
	"code_detail.html",
	"tokens.html",
	"token_created.html",
}

func newTemplateSet() (*templateSet, error) {
	layout, err := template.New("layout.html").Funcs(templateFuncs).ParseFS(embedFS, "templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("console: parsing layout template: %w", err)
	}

	ts := &templateSet{pages: map[string]*template.Template{}}
	for _, name := range pageFiles {
		clone, err := layout.Clone()
		if err != nil {
			return nil, fmt.Errorf("console: cloning layout for %s: %w", name, err)
		}
		clone, err = clone.ParseFS(embedFS, "templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("console: parsing template %s: %w", name, err)
		}
		ts.pages[name] = clone
	}
	return ts, nil
}

// render executes the named page's "layout" definition (which itself pulls in that
// page's "content" definition), with the given per-page content and layout metadata.
func (ts *templateSet) render(w io.Writer, page string, l Layout) error {
	t, ok := ts.pages[page]
	if !ok {
		return fmt.Errorf("console: unknown template %q", page)
	}
	return t.ExecuteTemplate(w, "layout", l)
}

// csrfHeaderName is exposed to templates so the sign-out / htmx forms can be documented
// inline without hardcoding the header name in two places.
const csrfHeaderName = middleware.CSRFHeader
