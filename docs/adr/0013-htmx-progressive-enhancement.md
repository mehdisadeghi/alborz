# 13. htmx sits over the pages, never under them

Status: accepted.

## Context

Every page in alborz is a document a server wrote: a link is a GET, a
button is a form, and the answer is a whole page. That is why the
interface works in a text browser, in a locked-down phone, and with
scripting off, and it is the property the project is least willing to
lose.

It costs something all the same. Starring a message reloads the list.
Turning a page throws away the scroll position and the focus ring.
A folder that takes a second to fetch shows a white document for that
second. On a phone every one of those is felt.

Almost every reader has scripting. The question is what a script is
allowed to be: an enhancement of the page that already works, or the
thing that makes it work at all.

## Decision

htmx 2 (2.0.10, MIT, vendored as an asset like Squire; not the 4.0
line, which is a week old at the time of writing) is loaded on every
page and given exactly one job: to fetch what the server would have
sent anyway and put it in place without discarding the document.

Three rules bound it.

**The server answers the same question either way.** Every route keeps
answering a whole page to a plain request. A request that carries
`HX-Request` may be answered with a fragment of that same page - a
template block the full page also renders, never a second copy of the
markup - and with nothing else. No route exists that only a script can
reach, and no route changes its meaning because a script asked.

**The markup carries the enhancement, not a script.** Behaviour is
`hx-*` attributes on the element that already does the work: the form
that stars a message, the link that turns the page. Remove the script
and the attribute is inert and the form still posts. Nothing is bound
by a selector reaching in from a file of its own, which is how the
no-script path rots without anyone noticing. `hx-on:` and `js:` values
are not used and eval stays off (`htmx-config` meta), so the page's
own Content-Security-Policy needs no `unsafe-eval`.

**A form reached from outside it is not boosted.** A button carrying
`form="..."` and a `formaction` is how alborz puts one form's actions
in a toolbar, and htmx only notices such a button when it carries an
explicit `type` attribute; without one the request goes to the page's
own URL and leaves the button's name and value behind. Boosting has to
mean "what the browser would have done", so every form with an `id` -
which is every form reached from outside it - carries
`hx-boost="false"`. An annotation on each button would work and would
be forgotten by the first person to add a button.

**A form that changes the document itself is not boosted either.** The
language, the reading direction, the forced colour scheme and the text
size are attributes of the root element; a swap replaces the body and
leaves them as they were. The language menu and the reading settings
form therefore ask for a whole page, which is what changing all of
those at once deserves.

**Where it earns its place.** Boosting covers navigation everywhere:
one attribute on `body`, no server change, and back and forward keep
working because htmx pushes the same URL the link held. Beyond that,
a fragment is written only where the whole page is plainly the wrong
answer: a mark on one row, a listing that pages or filters under a
toolbar that does not change, and the notice strip, which is swapped
out of band so an action's answer reaches the page it was made from.
Compose, import, and anything carrying a file post the old way; a
boosted upload loses the browser's own progress and gains nothing.

## Consequences

A reader with scripting gets a mail client that does not blink; a
reader without one gets what they had. The tests that matter stay the
Go ones, which speak plain HTTP and never see htmx, so the baseline is
checked on every run by construction rather than by discipline.

The cost is 51 KB of vendored script and a rule to keep: a fragment is
a block of the page it belongs to. The day a fragment is written that
the full page does not use, the two drift, and the reader without a
script is the one who finds out.
