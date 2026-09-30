module git.mehdix.org/alborz

go 1.27.1

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/ProtonMail/go-crypto v1.5.0
	github.com/alborzmail/go-itip v0.0.0-20260929111949-05f20f0ab6b6
	github.com/alborzmail/go-recur v0.0.0-20260929133629-dcb73b0bbb94
	github.com/aymerick/douceur v0.2.0
	github.com/cention-sany/utf7 v0.0.0-20170124080048-26cad61bd60a
	github.com/emersion/go-ical v0.0.0-20250609112844-439c63cef608
	github.com/emersion/go-imap/v2 v2.0.0-beta.8
	github.com/emersion/go-mbox v1.0.4
	github.com/emersion/go-message v0.18.2
	github.com/emersion/go-msgauth v0.7.0
	github.com/emersion/go-sasl v0.0.0-20241020182733-b788ff22d5a6
	github.com/emersion/go-smtp v0.25.0
	github.com/emersion/go-vcard v0.1.0
	github.com/emersion/go-webdav v0.7.0
	github.com/go-webauthn/webauthn v0.18.2
	github.com/klauspost/compress v1.20.1
	github.com/microcosm-cc/bluemonday v1.0.27
	github.com/nicksnyder/go-i18n/v2 v2.6.1
	github.com/smallstep/pkcs7 v0.2.3
	gitlab.com/golang-commonmark/linkify v0.0.0-20200225224916-64bca66f6ad3
	go.etcd.io/bbolt v1.5.0
	go.guido-berhoerster.org/managesieve v0.8.1
	golang.org/x/crypto v0.57.0
	golang.org/x/image v0.45.0
	golang.org/x/net v0.58.0
	golang.org/x/text v0.42.0
	jaytaylor.com/html2text v0.0.0-20230321000545-74c2419ad056
)

require (
	github.com/clipperhouse/stringish v0.1.1 // indirect
	github.com/clipperhouse/uax29/v2 v2.3.0 // indirect
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/fxamacker/cbor/v2 v2.9.4 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/x v0.3.1 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/olekukonko/tablewriter v0.0.5 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/ssor/bom v0.0.0-20170718123548-6386211fdfcf // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

// Our fork of go-vcard carries what upstream has not merged: an escaped
// \; kept through a save in N and ADR (emersion/go-vcard#40), and
// unescaped in every other text value; REV with an offset, every
// CATEGORIES line, MEMBER and BDAY.
replace github.com/emersion/go-vcard => github.com/alborzmail/go-vcard v0.0.0-20260928213329-9000c5176aff

// Our fork of go-webdav carries the open upstream pull requests alborz
// relies on (#147 #174 #179 #180 #181 #182 #194 #208 #209 #211), our
// own server patches: PROPPATCH, dead properties, octets kept as sent,
// and client ones: collections made, changed and listed, ETags and syncs
// handed over as sent, files listed without a length; time ranges
// matched by RFC 4791 9.9 for recurring events, to-dos, journals and
// moved instances, Solar Hijri rules by their instances, and an object
// whose times it cannot read kept; an object a report holds that does not
// parse handed over unparsed; the calendar scales it expands advertised
// (RFC 7529 supported-rscale-set), and text matched by its collation.
replace github.com/emersion/go-webdav => github.com/alborzmail/go-webdav v0.0.0-20260930175655-2b3a5b003a75

// Our fork of go-msgauth carries the authres parser rework upstream has
// not merged (emersion/go-msgauth#53): quoted values and comments, and
// the fix for the method with no properties that rework dropped; and
// ParseARC, the instance an ARC-Authentication-Results field carries.
replace github.com/emersion/go-msgauth => github.com/alborzmail/go-msgauth v0.0.0-20260928213517-dae367b5cecf

// Our fork of go-mbox adds the reversible mboxrd quoting, which upstream
// does not have: a body line reading ">From " survives a round trip.
replace github.com/emersion/go-mbox => github.com/alborzmail/go-mbox v0.0.0-20260921114627-f2df45e279df

// Our fork of go-ical adds what upstream leaves to every caller:
// NewTimezone, the VTIMEZONE of a zone over a span, the zones a calendar
// refers to, alarm triggers, splitting by UID, dropping repeated zones;
// and recurrence by go-recur, RSCALE included, with a component and its
// overrides read as one series; a TZID read from the calendar's own
// VTIMEZONE where the zone database lacks it, and as floating where
// neither defines it; and a decoder that reports a malformed parameter or
// a file cut off inside a component instead of panicking or ending early.
replace github.com/emersion/go-ical => github.com/alborzmail/go-ical v0.0.0-20260930180944-1eca68c44cbd

// Our fork of go-imap carries what upstream has open: ID with fields
// RFC 2971 does not name, so Dovecot learns the reader's address
// (emersion/go-imap#693); Dovecot's BODYSTRUCTURE for message/global
// (#704); an error, not a clean end, for a body cut off mid-transfer
// (#676); GETMETADATA options before the mailbox, as RFC 5464 orders;
// LOGIN and AUTHENTICATE secrets redacted in the debug output.
replace github.com/emersion/go-imap/v2 => github.com/alborzmail/go-imap/v2 v2.0.0-20260928213848-1fb04bfa5d26
