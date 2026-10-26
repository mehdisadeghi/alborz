# 97. Remembered session inactivity

## Status

Accepted.

## Context

The login checkbox promised a fixed number of days. The browser cookie and sealed
mail credentials expired from creation even when the reader kept using Alborz,
while the in-memory session tracked activity separately. Browser and installed
PWA users need predictable persistence across launches and server deployments.

## Decision

Offer “Keep me signed in on this device” with a separate explanation of the
60-day inactivity limit. Use the same policy for browser and installed PWA.

A remembered session has one persisted expiry, shared by its cookie and
credential validity. Authenticated user activity advances that expiry by 60
days. Polling, streams, push delivery and automatic lock checks do not renew it.
Check expiry before restoring or refreshing any session. Explicit sign-out,
device revocation and invalid mail credentials continue to revoke access.

Passkey locking remains independent: remembering an account does not prevent its
session from locking. Unremembered accounts retain their existing idle and
restart behavior. Remembered accounts survive restart when their database and
login key are preserved; notification connections resume when the browser or
PWA returns.

Renewal must not revive an expired session or credential.
Renewal failures must not issue a cookie promising a
lifetime that the server failed to persist.

## Consequences

Regular use can keep a trusted device signed in without a monthly password
prompt. An unused device loses its remembered access after 60 days. Users can
revoke devices earlier, and installations can protect local access with passkeys.
Clearing browser storage or losing the server's persistent data still requires
signing in again.
