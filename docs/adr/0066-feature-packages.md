# ADR 66: Feature packages and their web packages

Status: accepted.

## Context

About 250 functions below the handlers took the HTTP context, so any
logic could depend on the request. echo carried routes, context and
middleware, and URLs were written out by hand in Go and templates.

## Decision

### Packages

1. Each feature is a package with no HTTP dependency (root `alborz`,
   `mail`, `filters`, `pim`, `pim/calendar`, `pim/contacts`). Its pages
   live in a sibling `web` package (`auth/web`, `mail/web`,
   `filters/web`, `settings/web`, `pim/web`, `pim/calendar/web`,
   `pim/contacts/web`). A logic package takes a `context.Context` and
   plain values and returns data and plain errors.
2. Only `web` packages, packages under `cmd/` and test support packages
   (named `*test`) MAY import `alborz/web`. A logic package MUST NOT
   import a `web` package. A `web` package MUST NOT import another
   feature's `web` package, except a sub-feature's pages using its
   parent's (`pim/calendar/web` uses `pim/web`) and an add-on's pages
   using the page they extend (ADR 82, `web.Extend`). `TestImportRule`
   enforces this over `go list`.
3. Formatting lives in `internal/locale` (catalog, plurals, language
   matching, digits) and `internal/dates`. A `dates.Reader` is a
   language, a zone and a primary and secondary calendar; a
   `dates.Field` names the calendar it is written in. `locales/` only
   embeds the locale files.
4. Features own their saved settings. The servers page gets each
   feature's cards through `Server.OnServers`; a feature adds to
   another's page by type through `web.Extend` (e.g. `*mail.Shown`).

### HTTP layer

5. `alborz/web` is the HTTP layer on `net/http`: Context, groups,
   routes with body limits, middleware, and error-to-status mapping in
   one place. echo is removed.
6. `cmd/alborz/app/routes.go` composes the groups and mounts each
   feature; `web/routes.go` holds the core's endpoints; each feature's
   routes are in its package (`routes.go`, relative to its prefix).
7. A group is a path prefix, a name prefix and a middleware stack;
   nested groups join all three. Middleware defines the group:
   public pages `Accounts`; the app `Activity`, `Lock`, `SignedIn`;
   `/unlock` `Activity`, `SignedIn`; the event stream `Lock`,
   `SignedIn` without compression or `Activity`; the DAV group none
   (HTTP authentication per request). No path is special-cased.
8. Routes are `http.ServeMux` patterns with a method. A path no route
   serves is 405 (with `Allow`) where another method is served, else
   404, both through the error page.
9. Every route MUST have a name, `feature.page`, dotted for actions
   (`mail.message.reply`). Templates build paths with `url "name"
   values...`, Go with `ctx.Path` or `ctx.URL` (in the request's
   account). Values are escaped as path segments, `:` included. A name
   bound to two patterns, an unnamed route, a template `url` naming no
   route or with the wrong number of values MUST stop startup.
10. A path is read back by route (`Server.Match`), never by string
    comparison. Scripts get paths from data attributes. A route that
    answers a file declares it (`Group.Download`); one whose bytes are
    never compressed declares it (`Group.Verbatim`). The service worker
    takes downloads, sign-in/out posts and the asset prefix from the
    routes.
11. `main` parses flags and calls a testable `run`; options are
    validated by `alborz.New` and `web.New`.

### Tests

12. Tests use `internal/alborztest` and sit beside the feature, in
    external `web_test` packages, reaching pages by route name.
    Fixtures (`Alborz`, `IMAP`, `SMTP`, `DAV`, `Sieve`, `Passwd`) are
    functions of the test that clean up after it; `Alborz` serves
    through `app.New`, as `cmd/alborz` does.
13. Outgoing requests to servers outsiders name take their transport
    from the options (`Guarded`), so a test can trust its own
    certificate.

## Consequences

- Handlers read the request, call the feature, and render or redirect.
- Another front end (JSON API, JMAP) can sit beside `web` over the
  same logic.
- Templates stay in one tree.
- Unit tests of packages that `app` composes keep small local stubs,
  since importing the fixtures would be an import cycle.

## References

- ADR 28 (colon escaping)
- `cmd/alborz/app/routes.go`, `cmd/alborz/main.go`, `web/router.go`
  (`Group`, `Server.Path`, `Server.Match`, `unrouted`,
  `checkURLs`), `web/server.go` (`Extend`, `OnServers`),
  `importrule_test.go`, `internal/locale`, `internal/dates`,
  `internal/alborztest`, `Makefile` (`lint`)
