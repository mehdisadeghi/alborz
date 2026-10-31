# ADR 103: Header menus

Status: accepted. Amends ADR 1, ADR 76 and ADR 102.

## Context

The header named the account in force in a disclosure as wide as the
address, so the header moved when the scope changed; signing out of one
account sat beside each address; the language was chosen only on a
settings page. Pages of the person, which every account shares, were
drawn as if in the merged scope.

## Decision

### Header

1. Right of the search the header MUST hold only fixed-width icon
   buttons, in this order: language, colour scheme (ADR 50), lock
   (ADR 95) where a passkey locks, person menu. No header control MAY
   move when the scope changes.
2. A header menu MUST be an HTML popover (`popover`, `popovertarget`)
   hung from its button by CSS anchor positioning. Where `position-area`
   is unsupported, an `@supports` rule MUST place it under the header's
   inline end; `helpers.js` MAY place it by hand.
3. At `<= 600px` the language and person menus MUST NOT show; the
   drawer holds the accounts, Settings and Sign out of all accounts
   (ADR 94).

### Person menu

4. The person button's accessible name MUST be `nav.accounts` and the
   scope in force: the account, All accounts, or the one account's
   address. On a page of the person it MUST name no scope.
5. The menu MUST list, in groups separated by `hr`: each signed-in
   account (the current section in that account's scope), All accounts
   with more than one account, and Add account; Settings, Keyboard
   shortcuts (not on a coarse pointer) and Take the tour; Sign out of
   all accounts. The scope in force MUST be marked (`is-current`,
   `aria-current`).
6. The menu MUST NOT sign out of one account. That is offered only on
   Sessions, by this browser's row (`auth.logout` with `account`): it
   ends a session, which is what that page lists.
7. The person button MUST carry the `accounts` and `settings` tour marks
   (ADR 76).

### Language

8. The language menu MUST list Auto (`settings.langauto`) and then every
   language in its own name, marking the one in force. A row MUST load a
   whole page, since `lang` and `dir` are the root's.
9. Signed in, a row MUST POST `language` to `settings.language`
   (`/settings/language`), which sets `Profile.Display.Language` alone
   and redirects to `next` without its `lang` parameter.
10. Signed out, a row MUST GET the current page with `lang` set, keeping
    its other parameters. A `lang` in the URL MUST be kept in the
    profile (the cookie when signed out) and outranks it for that
    request; an empty `lang` follows `Accept-Language`.

### Pages of the person

11. A page of the person (General, Passkeys, an add-on's status page such
    as Weather) MUST be served through `web.Personal`: in no account's
    scope and not merged. The header marks no scope there, its links
    carry no `account`, and the settings rail opens no account group.
12. An account chosen in the person menu on a settings page or a page of
    the person MUST open that account's Account page.

## Consequences

- The header shows no address; the scope is read from the rail, the
  crumb and the person button's name.
- Signing out of one account is two steps away instead of one.
- A language chosen while signed out stays in the URL until the next
  link drops it; the cookie keeps it after.

## References

- WHATWG HTML (the `popover` attribute), CSS Anchor Positioning Module
  Level 1, BCP 47
- ADR 1, ADR 25, ADR 50, ADR 76, ADR 94, ADR 95, ADR 102
- `templates/partials/nav.html` (`person-menu`, `language-menu`,
  `signout-all`), `templates/pages/login.html`,
  `templates/pages/settings/sessions.html`, `templates/assets/style.css`
- `web/accounts.go` (`Personal`), `web/renderer.go` (`Languages`,
  `queryLanguage`), `web/middleware.go` (`Prepare`), `web/prefs.go`
  (`SetLanguage`), `settings/web/profile.go` (`handleLanguage`),
  `settings/web/routes.go`, `addons/weather/web/weather.go`
