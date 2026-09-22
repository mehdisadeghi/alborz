module git.mehdix.org/alborz

go 1.27.1

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/ProtonMail/go-crypto v1.1.6
	github.com/aymerick/douceur v0.2.0
	github.com/dromara/carbon/v2 v2.6.17
	github.com/emersion/go-ical v0.0.0-20250609112844-439c63cef608
	github.com/emersion/go-imap/v2 v2.0.0-beta.8
	github.com/emersion/go-mbox v1.0.4
	github.com/emersion/go-message v0.18.2
	github.com/emersion/go-msgauth v0.7.0
	github.com/emersion/go-sasl v0.0.0-20241020182733-b788ff22d5a6
	github.com/emersion/go-smtp v0.25.0
	github.com/emersion/go-vcard v0.1.0
	github.com/emersion/go-webdav v0.7.0
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611
	github.com/go-webauthn/webauthn v0.18.1
	github.com/labstack/echo/v4 v4.15.4
	github.com/labstack/gommon v0.5.0
	github.com/microcosm-cc/bluemonday v1.0.27
	github.com/nicksnyder/go-i18n/v2 v2.6.1
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
	github.com/cloudflare/circl v1.3.7 // indirect
	github.com/fxamacker/cbor/v2 v2.9.3 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/x v0.3.1 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/olekukonko/tablewriter v0.0.5 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/ssor/bom v0.0.0-20170718123548-6386211fdfcf // indirect
	github.com/teambition/rrule-go v1.8.2 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasttemplate v1.2.2 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

// Our fork of go-vcard carries what upstream has not merged: an escaped
// \; kept through a save in N and ADR (emersion/go-vcard#40), and
// unescaped in every other text value.
replace github.com/emersion/go-vcard => github.com/mehdisadeghi/go-vcard v0.0.0-20260921114506-5470ed03a481

// Our fork of go-webdav carries the open upstream pull requests alborz
// relies on (#147 #174 #179 #180 #181 #182 #194 #208 #209 #211) and our
// own server patches: PROPPATCH, dead properties, octets kept as sent.
replace github.com/emersion/go-webdav => github.com/mehdisadeghi/go-webdav v0.0.0-20260921101523-6a35b64d0a5f

// Our fork of go-msgauth carries the authres parser rework upstream has
// not merged (emersion/go-msgauth#53): quoted values and comments.
replace github.com/emersion/go-msgauth => github.com/mehdisadeghi/go-msgauth v0.0.0-20260921123823-04a6405fe7bc

// Our fork of go-mbox adds the reversible mboxrd quoting, which upstream
// does not have: a body line reading ">From " survives a round trip.
replace github.com/emersion/go-mbox => github.com/mehdisadeghi/go-mbox v0.0.0-20260921114627-f2df45e279df

// Our fork of go-ical adds NewTimezone, the VTIMEZONE of a zone over a
// span, which upstream leaves to every caller.
replace github.com/emersion/go-ical => github.com/mehdisadeghi/go-ical v0.0.0-20260921115030-4aff18c21c48

// Our fork of go-imap carries what upstream has open: ID with fields
// RFC 2971 does not name, so Dovecot learns the reader's address
// (emersion/go-imap#693); Dovecot's BODYSTRUCTURE for message/global
// (#704); an error, not a clean end, for a body cut off mid-transfer
// (#676).
replace github.com/emersion/go-imap/v2 => github.com/mehdisadeghi/go-imap/v2 v2.0.0-20250317164603-f648aa3a4b46
