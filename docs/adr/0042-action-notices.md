# ADR 42: Every action reports its result

Status: accepted. Amends ADR 4.

## Context

Handlers set notices by hand, and some state-changing actions redirected
without one, leaving no visible result.

## Decision

1. A state-changing POST answered with a redirect MUST set a notice
   stating what it did, with counts, and an Undo where the inverse is
   exact (ADR 4).
2. A POST whose result is visible where the reader already is (a star,
   a note on the object's page, a folder refresh, a narrowed list,
   signing in) MUST call `ctx.Quiet()` instead. Setting an empty notice
   is equivalent.
3. The `Noticed` middleware, on every page group, checks each POST
   answered with a 3xx: with neither a notice nor `Quiet` it logs the
   route; with `StrictNotices` set (as in tests) it fails the request.
4. The notice text is the handler's; the middleware only detects its
   absence.

## Consequences

- A route no test exercises is caught in the log on first use.
- htmx responses answered in place and form re-renders are not checked;
  neither leaves the page.

## References

- ADR 4, ADR 41
- `web/middleware.go` (`Noticed`), `web/session.go` (`Quiet`, `Notify`,
  `PutNotice`), `web/server.go` (`StrictNotices`),
  `cmd/alborz/app/routes.go`, `internal/alborztest/alborz.go`
