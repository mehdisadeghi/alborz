# 14. What alborz is when it is installed on a phone

Status: accepted.

## Context

Alborz has had a manifest and icons for a while, which is enough for a
phone to put it on a home screen and enough for nothing else. Started
from that icon it was a web page: a white flash between folders, a
stylesheet fetched again on every cold start, a network error shown by
the browser's own dinosaur, and a list that only learned about new mail
when somebody asked for a page.

The mail itself is the thing that must not be kept. A browser that
writes a mailbox to disk keeps it after the session, after the logout
and after the phone changes hands, and no setting in alborz can reach
it once it is there.

## Decision

**A worker that holds the shell and never the mail.** `/sw.js` is
written by the server so it can name the asset URLs with their digests
in it. It caches the stylesheet, the scripts and the icons, cache-first,
which is sound because those URLs change when their content does. Every
other request goes to the network and is not stored. A navigation that
fails is answered with `/offline`, a page that says alborz keeps nothing
on the device and offers to try again - which is true, and is why it can
say nothing more useful.

*2026-09-19:* the pages the reader opened are now saved and answer
offline under a notice (ADR 19), and `/offline` is gone: a page never
saved fails the way any page does, with the browser's own words. A
page of its own imitated an application, and retried on its own like
one.

**A stream instead of a poll.** One connection per browser, `/events`,
carrying what the IDLE watchers already noticed: an account and a folder
per line, no rendering, no mail. What a page does with that is update
the rail's count and nothing else. The list under the reader's hands
does not move, nothing is inserted while they are reading, and the
folder they click next was fetched the moment the watcher heard about
it. A reader with no script is where they were before: a page they load
is current.

**Transitions, not animations.** htmx swaps run inside a view
transition, 120ms, as one group. Naming the header and the rail as
groups of their own was tried and reverted: a named group morphs from
its old box to its new one, the rail is as tall as the page, and every
navigation that changed the page's height zoomed the chrome. Under
`prefers-reduced-motion` the transition pseudo-elements are given no
animation at all, since the blanket rule for the document does not
reach them.

**The system's edges.** The docked account block on a phone pads to
`env(safe-area-inset-bottom)`, which is zero in a browser and the home
indicator's strip when installed. The manifest stays `minimal-ui`: the
address bar is the way out of a page, and alborz has no navigation of
its own that replaces it.

## Consequences

Installed, alborz starts from cache and looks like an application; it
still holds nothing to read offline, and says so rather than pretending.
The rules that keep it that way are two: the worker's fetch handler
stores only `/assets/`, and the stream carries facts rather than
markup. Both are small enough to check by reading them.

Swipe actions on a list row are not here. They are the one part of a
phone mail client that cannot be got right without a phone in hand, and
a gesture that fires the wrong action on a mailbox is worse than no
gesture.
