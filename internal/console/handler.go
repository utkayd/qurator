// Package console serves the embedded, no-build-step web console under /ui/
// (research.md §5, spec US6, FR-042/FR-043). It never talks to the HTTP API over the
// network — it calls the same service layer through the interfaces in types.go — and it
// never imports a storage package directly; wiring adapts the real services at
// composition time.
package console

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/httpapi/middleware"
)

// defaultPageLimit is the page size for the codes list when the caller does not specify
// one.
const defaultPageLimit = 50

// Handler serves every route under /ui/.
type Handler struct {
	deps   Deps
	tmpl   *templateSet
	assets *assetRegistry
	mux    *http.ServeMux
}

// New constructs the console handler. It panics if the embedded templates or assets
// fail to parse — both are compiled into the binary, so a failure here means a broken
// build, not a runtime condition.
func New(deps Deps) *Handler {
	tmpl, err := newTemplateSet()
	if err != nil {
		panic(err)
	}
	assets, err := buildAssetRegistry()
	if err != nil {
		panic(err)
	}
	h := &Handler{deps: deps, tmpl: tmpl, assets: assets}
	h.routes()
	return h
}

func (h *Handler) routes() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /ui/assets/", h.assets.ServeHTTP)

	mux.HandleFunc("GET /ui/signin", h.getSignIn)
	mux.HandleFunc("POST /ui/signin", h.postSignIn)
	mux.HandleFunc("POST /ui/signout", h.requireAuth(h.postSignOut))

	mux.HandleFunc("GET /ui/", h.requireAuth(h.getCodesList)) // also matches "/ui/" exactly
	mux.HandleFunc("GET /ui/codes/new", h.requireAuth(h.getCodeNew))
	mux.HandleFunc("POST /ui/codes", h.requireAuth(h.postCodeCreate))
	mux.HandleFunc("GET /ui/codes/{id}", h.requireAuth(h.getCodeDetail))
	mux.HandleFunc("PATCH /ui/codes/{id}", h.requireAuth(h.patchCodeDestination))
	mux.HandleFunc("DELETE /ui/codes/{id}", h.requireAuth(h.deleteCode))

	mux.HandleFunc("GET /ui/tokens", h.requireAuth(h.getTokens))
	mux.HandleFunc("POST /ui/tokens", h.requireAuth(h.postTokenCreate))
	mux.HandleFunc("DELETE /ui/tokens/{id}", h.requireAuth(h.deleteToken))

	h.mux = mux
}

// ServeHTTP implements http.Handler. Every response gets the strict CSP and a fresh
// per-request nonce (csp_test.go).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	withCSP(h.mux).ServeHTTP(w, r)
}

// ---------------------------------------------------------------------------
// Auth guard
// ---------------------------------------------------------------------------

// requireAuth redirects an anonymous request to the sign-in page (302; every /ui/*
// route except /ui/signin and static assets requires a session) and enforces the CSRF
// header on mutating requests from an authenticated session.
func (h *Handler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := h.deps.Auth.CurrentUser(r)
		if !ok {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, "/ui/signin", http.StatusFound)
			return
		}
		if !isSafeMethod(r.Method) && r.Header.Get(middleware.CSRFHeader) == "" {
			h.renderError(w, http.StatusForbidden, "This action requires the console's own interface.")
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey{}, user)
		next(w, r.WithContext(ctx))
	}
}

type userCtxKey struct{}

func userFromContext(ctx context.Context) (domain.User, bool) {
	u, ok := ctx.Value(userCtxKey{}).(domain.User)
	return u, ok
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func isHTMXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// redirectAfterMutation sends the browser to target: an HX-Redirect header for an
// htmx-driven request (which htmx follows client-side without a full navigation), or a
// standard 303 for a plain form submission or a plain HTTP client such as the e2e test.
func redirectAfterMutation(w http.ResponseWriter, r *http.Request, target string) {
	if isHTMXRequest(r) {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Rendering helpers
// ---------------------------------------------------------------------------

func (h *Handler) baseLayout(r *http.Request, title string) Layout {
	l := Layout{
		Title:      title,
		Nonce:      nonceFromContext(r.Context()),
		AppCSSURL:  h.assets.URL("app.css"),
		AppJSURL:   h.assets.URL("app.js"),
		HtmxURL:    h.assets.URL("htmx.min.js"),
		CSRFHeader: csrfHeaderName,
	}
	if u, ok := userFromContext(r.Context()); ok {
		l.User = newUserView(u)
	}
	return l
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, page, title string, content any) {
	l := h.baseLayout(r, title)
	l.Content = content
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := h.tmpl.render(w, page, l); err != nil {
		httpapi.Internal(w, r, fmt.Errorf("console: rendering %s: %w", page, err))
	}
}

// renderError renders a bare error banner when we do not have a good page-specific
// place to show it (e.g. a CSRF failure before we know which page the user was on).
func (h *Handler) renderError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<!doctype html><html><body><p class=\"error-banner\" role=\"alert\">%s</p></body></html>", escapeHTML(message))
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// ---------------------------------------------------------------------------
// Sign-in / sign-out
// ---------------------------------------------------------------------------

type signInData struct {
	Email string
	Error string
}

func (h *Handler) getSignIn(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.deps.Auth.CurrentUser(r); ok {
		http.Redirect(w, r, "/ui/", http.StatusFound)
		return
	}
	h.render(w, r, http.StatusOK, "signin.html", "Sign in", signInData{})
}

func (h *Handler) postSignIn(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, "signin.html", "Sign in", signInData{Error: "Could not read the form."})
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	if _, err := h.deps.Auth.SignIn(r.Context(), w, email, password); err != nil {
		msg := "Could not sign in."
		if errors.Is(err, ErrInvalidCredentials) {
			msg = "Incorrect email or password."
		}
		h.render(w, r, http.StatusUnauthorized, "signin.html", "Sign in", signInData{Email: email, Error: msg})
		return
	}
	http.Redirect(w, r, "/ui/", http.StatusSeeOther)
}

func (h *Handler) postSignOut(w http.ResponseWriter, r *http.Request) {
	h.deps.Auth.SignOut(w, r)
	redirectAfterMutation(w, r, "/ui/signin")
}

// ---------------------------------------------------------------------------
// Codes
// ---------------------------------------------------------------------------

type codesListData struct {
	Items      []domain.Code
	NextCursor string
}

func (h *Handler) getCodesList(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ui/" {
		http.NotFound(w, r)
		return
	}
	user, _ := userFromContext(r.Context())

	filter := domain.CodeFilter{
		UserID: user.ID,
		Limit:  defaultPageLimit,
		Cursor: r.URL.Query().Get("cursor"),
	}
	page, err := h.deps.Codes.List(r.Context(), user.ID, filter)
	if err != nil {
		httpapi.Internal(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "codes_list.html", "Your codes", codesListData(page))
}

type codeNewData struct {
	Destination   string
	Alias         string
	Mode          string
	Format        string
	FgColor       string
	BgColor       string
	ModuleShape   string
	MarginModules int
	SizePx        int
	ECLevel       string
	Error         string
}

func defaultCodeNewData() codeNewData {
	return codeNewData{
		Mode:          modeDynamic,
		Format:        "png",
		FgColor:       "#101828",
		BgColor:       "#FFFFFF",
		ModuleShape:   string(domain.ShapeSquare),
		MarginModules: 4,
		SizePx:        512,
		ECLevel:       string(domain.ECMedium),
	}
}

func (h *Handler) getCodeNew(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, "code_new.html", "New code", defaultCodeNewData())
}

func (h *Handler) postCodeCreate(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		h.renderCodeNewError(w, r, "Could not read the form.")
		return
	}

	data := defaultCodeNewData()
	data.Destination = strings.TrimSpace(r.FormValue("destination"))
	data.Alias = strings.TrimSpace(r.FormValue("alias"))
	if v := r.FormValue("mode"); v != "" {
		data.Mode = v
	}
	if v := r.FormValue("format"); v != "" {
		data.Format = v
	}
	if v := r.FormValue("fg_color"); v != "" {
		data.FgColor = v
	}
	if v := r.FormValue("bg_color"); v != "" {
		data.BgColor = v
	}
	if v := r.FormValue("module_shape"); v != "" {
		data.ModuleShape = v
	}
	if v := r.FormValue("margin_modules"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			data.MarginModules = n
		}
	}
	if v := r.FormValue("size_px"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			data.SizePx = n
		}
	}
	if v := r.FormValue("ec_level"); v != "" {
		data.ECLevel = v
	}

	if data.Mode != modeDynamic && data.Mode != modeDirect {
		data.Error = safeErrorMessage(fmt.Errorf("%w: unknown mode %q", ErrValidation, data.Mode), "Could not create the code.")
		h.render(w, r, http.StatusBadRequest, "code_new.html", "New code", data)
		return
	}

	in := CreateCodeInput{
		Destination: data.Destination,
		Alias:       data.Alias,
		Mode:        data.Mode,
		Styling: StylingInput{
			FgColor:       data.FgColor,
			BgColor:       data.BgColor,
			ModuleShape:   domain.ModuleShape(data.ModuleShape),
			MarginModules: data.MarginModules,
			SizePx:        data.SizePx,
			ECLevel:       domain.ECLevel(data.ECLevel),
		},
	}

	code, err := h.deps.Codes.Create(r.Context(), user.ID, in)
	if err != nil {
		data.Error = safeErrorMessage(err, "Could not create the code.")
		h.render(w, r, http.StatusBadRequest, "code_new.html", "New code", data)
		return
	}
	redirectAfterMutation(w, r, "/ui/codes/"+code.ID)
}

func (h *Handler) renderCodeNewError(w http.ResponseWriter, r *http.Request, msg string) {
	data := defaultCodeNewData()
	data.Error = msg
	h.render(w, r, http.StatusBadRequest, "code_new.html", "New code", data)
}

// codeView adds console-only presentation fields (the scan address) to a domain.Code
// without polluting the domain type.
type codeView struct {
	domain.Code
	ScanURL string
	Mode    string
}

type codeDetailData struct {
	Code      codeView
	ImageURL  string
	Analytics *domain.AnalyticsResult
	From      string
	To        string
	Error     string
}

func (h *Handler) getCodeDetail(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := r.PathValue("id")

	code, err := h.deps.Codes.Get(r.Context(), user.ID, id)
	if err != nil {
		h.renderNotFoundOr500(w, r, err)
		return
	}

	to := time.Now().UTC()
	from := to.AddDate(0, 0, -30)
	var analytics *domain.AnalyticsResult
	if h.deps.Analytics != nil && codeMode(code) != modeDirect {
		result, err := h.deps.Analytics.Get(r.Context(), user.ID, domain.AnalyticsQuery{
			CodeID: id,
			From:   from,
			To:     to,
			Bucket: domain.BucketDay,
		})
		if err == nil {
			analytics = &result
		}
	}

	h.render(w, r, http.StatusOK, "code_detail.html", code.ShortCode, codeDetailData{
		Code:      codeView{Code: code, ScanURL: "/r/" + code.ShortCode, Mode: codeMode(code)},
		ImageURL:  "/i/" + code.ID + ".png",
		Analytics: analytics,
		From:      from.Format("2006-01-02"),
		To:        to.Format("2006-01-02"),
	})
}

func (h *Handler) patchCodeDestination(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "Could not read the form.", nil)
		return
	}
	destination := strings.TrimSpace(r.FormValue("destination"))
	ifMatch := ifMatchVersion(r.Header.Get("If-Match"))

	code, err := h.deps.Codes.UpdateDestination(r.Context(), user.ID, id, destination, ifMatch)
	if err != nil {
		status := http.StatusBadRequest
		msg := safeErrorMessage(err, "Could not update the destination.")
		switch {
		case errors.Is(err, ErrVersionConflict):
			status = http.StatusConflict
			msg = "Someone else changed this code in the meantime. Reload and try again."
		case errors.Is(err, ErrNotFound):
			status = http.StatusNotFound
			msg = "Code not found."
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `<p class="error-banner" role="alert">%s</p>`, escapeHTML(msg)) //nolint:gosec // msg is HTML-escaped by escapeHTML before interpolation
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<p role="status">Destination updated to %s.</p>`, escapeHTML(code.Destination)) //nolint:gosec // code.Destination is HTML-escaped by escapeHTML before interpolation
}

func (h *Handler) deleteCode(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := r.PathValue("id")
	if err := h.deps.Codes.Delete(r.Context(), user.ID, id); err != nil {
		h.renderNotFoundOr500(w, r, err)
		return
	}
	redirectAfterMutation(w, r, "/ui/")
}

func (h *Handler) renderNotFoundOr500(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	httpapi.Internal(w, r, err)
}

// ifMatchVersion parses an entity-tag of the form `"7"` into 7. It returns nil for an
// empty or malformed header, in which case the caller falls back to last-write-wins.
func ifMatchVersion(header string) *int64 {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	header = strings.TrimPrefix(header, `"`)
	header = strings.TrimSuffix(header, `"`)
	n, err := strconv.ParseInt(header, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// ---------------------------------------------------------------------------
// Tokens
// ---------------------------------------------------------------------------

type tokensData struct {
	Items []domain.APIToken
	Error string
}

func (h *Handler) getTokens(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	tokens, err := h.deps.Tokens.List(r.Context(), user.ID)
	if err != nil {
		httpapi.Internal(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "tokens.html", "API tokens", tokensData{Items: tokens})
}

type tokenCreatedData struct {
	Name   string
	Secret string
}

func (h *Handler) postTokenCreate(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, "tokens.html", "API tokens", tokensData{Error: "Could not read the form."})
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	var expiresAt *time.Time
	if v := strings.TrimSpace(r.FormValue("expires_at")); v != "" {
		if t, err := time.Parse("2006-01-02T15:04", v); err == nil {
			expiresAt = &t
		}
	}

	_, secret, err := h.deps.Tokens.Create(r.Context(), user.ID, name, expiresAt)
	if err != nil {
		tokens, _ := h.deps.Tokens.List(r.Context(), user.ID)
		h.render(w, r, http.StatusBadRequest, "tokens.html", "API tokens", tokensData{
			Items: tokens,
			Error: safeErrorMessage(err, "Could not create the token."),
		})
		return
	}

	h.render(w, r, http.StatusCreated, "token_created.html", "Token created", tokenCreatedData{
		Name:   name,
		Secret: secret,
	})
}

func (h *Handler) deleteToken(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := r.PathValue("id")
	if err := h.deps.Tokens.Revoke(r.Context(), user.ID, id); err != nil {
		h.renderNotFoundOr500(w, r, err)
		return
	}
	redirectAfterMutation(w, r, "/ui/tokens")
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// safeErrorMessage returns a message safe to show a browser user: the wrapped detail of
// a domain validation error, or a bland fallback for anything else (never a driver
// error, a path, or a stack trace — contracts/errors.md).
func safeErrorMessage(err error, fallback string) string {
	if errors.Is(err, ErrValidation) {
		msg := err.Error()
		if i := strings.Index(msg, ": "); i >= 0 {
			return msg[i+2:]
		}
		return msg
	}
	return fallback
}
