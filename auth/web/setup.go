package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

type setupEndpoint struct{ Host, Port, Security string }

func (s setupEndpoint) URL() string {
	if s.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: s.Security, Host: net.JoinHostPort(s.Host, s.Port)}).String()
}

func (s *setupEndpoint) discovered(raw string) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return
	}
	s.Host, s.Port, s.Security = u.Hostname(), u.Port(), u.Scheme
	if s.Port == "" {
		s.Port = map[string]string{"imaps": "993", "imap": "143", "smtps": "465", "smtp": "587"}[s.Security]
	}
}

type setupData struct {
	web.BaseRenderData
	Code, Username         string
	IMAP, SMTP             setupEndpoint
	CalDAV, CardDAV, Sieve string
	ShowServers, Manual    bool
	Domains                []string
	// PublicURL is offered prefilled with PublicPrefill, or shown read-only when PublicFixed by the configuration.
	PublicURL, PublicPrefill string
	PublicFixed              bool
	// Proxies are the configured trusted proxies; without them OfferProxy asks to trust a proxy on this machine.
	Proxies                string
	OfferProxy, TrustProxy bool
}

// localProxies are what "Trust the proxy on this machine" saves as -trusted-proxy.
var localProxies = []string{"127.0.0.1", "::1"}

// site reads the public address and the local proxy from the request: the browser's Origin
// replaces a public address left at its prefill, and a loopback peer forwarding for another is a proxy here.
// Only the review step carries public-url-prefill; any other request offers the defaults afresh.
func (d *setupData) site(ctx *web.Context) {
	r := ctx.Request()
	prefill := ctx.FormValue("public-url-prefill")
	reviewed := r.Method == http.MethodPost && r.PostForm.Has("public-url-prefill")
	if ctx.Server.PublicURL != nil {
		d.PublicURL, d.PublicFixed = ctx.Origin(), true
	} else {
		scheme := ctx.Scheme()
		if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded == "https" || forwarded == "http" {
			scheme = forwarded
		}
		d.PublicPrefill = scheme + "://" + r.Host
		if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
			d.PublicPrefill = origin
		}
		d.PublicURL = d.PublicPrefill
		if typed := strings.TrimSpace(ctx.FormValue("public-url")); reviewed && typed != prefill {
			d.PublicURL = typed
		}
	}
	if d.Proxies = strings.Join(ctx.Server.TrustedProxies(), ", "); d.Proxies != "" {
		return
	}
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	d.OfferProxy = err == nil && peer.Addr().IsLoopback() && (r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "")
	d.TrustProxy = d.OfferProxy && (!reviewed || ctx.FormValue("trust-proxy") == "1")
}

// chosenSite is what finishing saves beside the domains.
func (d *setupData) chosenSite() alborz.SetupSite {
	var site alborz.SetupSite
	if !d.PublicFixed {
		site.PublicURL = d.PublicURL
	}
	if d.TrustProxy {
		site.TrustedProxies = localProxies
	}
	return site
}

func handleSetup(ctx *web.Context) error {
	ctx.Response().Header().Set("Cache-Control", "no-store")
	ctx.Response().Header().Set("Referrer-Policy", "no-referrer")
	if !ctx.Server.NeedsSetup() {
		return ctx.NoContent(http.StatusNotFound)
	}
	d := setupData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("setup.title")),
		IMAP:           setupEndpoint{Port: "993", Security: "imaps"},
		SMTP:           setupEndpoint{Port: "465", Security: "smtps"},
	}
	d.site(ctx)
	render := func(status int) error {
		d.Domains = ctx.Server.SetupDomains(d.Code)
		return ctx.Render(status, "setup", &d)
	}
	draftCookie := &http.Cookie{Name: "alborz_setup", Path: ctx.Path("auth.setup"), HttpOnly: true, Secure: ctx.Scheme() == "https", SameSite: http.SameSiteStrictMode}
	if ctx.Request().Method == http.MethodGet {
		if cookie, err := ctx.Cookie(draftCookie.Name); err == nil {
			ctx.Server.ResetSetup(cookie.Value)
			draftCookie.MaxAge = -1
			ctx.SetCookie(draftCookie)
		}
		return render(http.StatusOK)
	}
	d.Code = strings.TrimSpace(ctx.FormValue("setup-code"))
	d.Username = strings.TrimSpace(ctx.FormValue("username"))
	d.CalDAV = strings.TrimSpace(ctx.FormValue("caldav"))
	d.CardDAV = strings.TrimSpace(ctx.FormValue("carddav"))
	d.Sieve = strings.TrimSpace(ctx.FormValue("sieve"))
	refused := func(err error) error {
		if errors.Is(err, alborz.ErrSetupClosed) {
			return ctx.NoContent(http.StatusNotFound)
		}
		status, text := http.StatusUnprocessableEntity, ctx.T("setup.manual")
		if errors.Is(err, alborz.ErrSetupCode) {
			status, text = http.StatusForbidden, ctx.T("setup.badcode")
			d.ShowServers = false
		} else if setupErr, ok := errors.AsType[*alborz.SetupError](err); ok {
			key := "setup." + setupErr.Field + "error"
			if setupErr.Field == "caldav" || setupErr.Field == "carddav" || setupErr.Field == "sieve" {
				key = "setup.additionalerror"
			}
			text = ctx.T(key) + " " + setupErr.Err.Error()
			var dnsErr *net.DNSError
			if setupErr.Field == "discovery" && (errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &dnsErr) && dnsErr.IsTimeout)) {
				text = ctx.T("setup.discoverytimeout")
			}
			if setupErr.Field == "email" {
				d.ShowServers = false
			}
			if setupErr.Field == "storage" {
				status = http.StatusInternalServerError
			}
		}
		d.Manual = true
		d.GlobalData.Notice = &web.Notice{Kind: web.NoticeFailed, Text: text}
		return render(status)
	}
	switch ctx.FormValue("action") {
	case "", "discover":
		provider, err := ctx.Server.DiscoverSetup(ctx.Request().Context(), d.Code, d.Username)
		d.IMAP.discovered(provider.IMAP)
		d.SMTP.discovered(provider.SMTP)
		d.ShowServers = true
		if err != nil {
			return refused(err)
		}
		return render(http.StatusOK)
	case "add", "finish":
		if ctx.FormValue("action") == "add" || d.Username != "" {
			d.ShowServers = true
			read := func(prefix string) setupEndpoint {
				return setupEndpoint{Host: strings.TrimSpace(ctx.FormValue(prefix + "-host")),
					Port: strings.TrimSpace(ctx.FormValue(prefix + "-port")), Security: ctx.FormValue(prefix + "-security")}
			}
			d.IMAP, d.SMTP = read("imap"), read("smtp")
			provider := alborz.SetupProvider{IMAP: d.IMAP.URL(), SMTP: d.SMTP.URL(), CalDAV: d.CalDAV, CardDAV: d.CardDAV, Sieve: d.Sieve}
			if err := ctx.Server.AddSetup(ctx.Request().Context(), d.Code, d.Username, provider); err != nil {
				return refused(err)
			}
			draftCookie.Value = d.Code
			ctx.SetCookie(draftCookie)
		}
		if ctx.FormValue("action") != "add" {
			site := d.chosenSite()
			if err := ctx.Server.FinishSetup(d.Code, site); err != nil {
				return refused(err)
			}
			draftCookie.MaxAge = -1
			ctx.SetCookie(draftCookie)
			text := ctx.T("setup.ready")
			if site.PublicURL != "" || len(site.TrustedProxies) > 0 {
				text += " " + ctx.T("setup.restartsite")
			}
			ctx.Notify(web.Notice{Kind: web.NoticeDone, Text: text})
			if ctx.Partial() {
				ctx.Quiet()
				return ctx.Relocate(ctx.Path("auth.login"))
			}
			return ctx.QuietRedirect(ctx.Path("auth.login"))
		}
		d.Username, d.CalDAV, d.CardDAV, d.Sieve = "", "", "", ""
		d.IMAP, d.SMTP = setupEndpoint{Port: "993", Security: "imaps"}, setupEndpoint{Port: "465", Security: "smtps"}
		d.ShowServers = false
		return render(http.StatusOK)
	default:
		return ctx.NoContent(http.StatusBadRequest)
	}
}
