package brands

import (
	"fmt"
	"strings"
	"testing"

	"github.com/emersion/go-message/textproto"
	"golang.org/x/net/publicsuffix"

	"git.mehdix.org/alborz/mail"
)

var money = []string{"payment", "invoice", "account", "password", "suspended"}

func evidence(name, from, subject string) *mail.Evidence {
	return &mail.Evidence{Name: name, From: from, Subject: subject, MoneyWords: money}
}

// saidOf is what the check says of a message: each line's key, grade and arguments, or "" for nothing.
func saidOf(e *mail.Evidence) string {
	var lines []string
	for _, t := range Check(e) {
		lines = append(lines, strings.TrimSpace(fmt.Sprintln(append([]any{t.Key, t.Grade}, t.Args...)...)))
	}
	return strings.Join(lines, "; ")
}

// The message the reader was fooled by.
func TestABrandNameAtAnotherDomainWarns(t *testing.T) {
	e := evidence("Aws Web Service", "support@motulkzn.aynimarketing.co.za", "Action required - Your AWS account is overdue for payment")
	// Its own domain passes every check: authentication vouches for the domain, not for the name.
	e.Auth = &mail.AuthResults{SPF: "pass", DKIM: "pass", DMARC: "pass", DKIMDomain: "aynimarketing.co.za"}
	if got, want := saidOf(e), "brands.elsewhere 2 AWS aynimarketing.co.za"; got != want {
		t.Errorf("said %q, want %q", got, want)
	}
	e.Subject = "Hello"
	if got, want := saidOf(e), "brands.elsewhere 1 AWS aynimarketing.co.za"; got != want {
		t.Errorf("without a money subject said %q, want %q", got, want)
	}
}

// A wrong warning is worse than none: real mail of the brands, as they write their names.
func TestABrandAtItsOwnDomainIsSilent(t *testing.T) {
	for _, tc := range [][2]string{
		{"Amazon Web Services", "no-reply-aws@amazon.com"},
		{"AWS Notifications", "no-reply@sns.amazonaws.com"},
		{"Amazon Web Services", "no-reply@signin.aws"},
		{"AWS", "no-reply@login.awsapps.com"},
		{"Amazon.de", "versandbestaetigung@amazon.de"},
		{"Amazon.co.uk", "auto-confirm@amazon.co.uk"},
		{"Prime Video", "no-reply@primevideo.com"},
		{"Microsoft account team", "account-security-noreply@accountprotection.microsoft.com"},
		{"Microsoft Teams", "noreply@teams.mail.microsoft"},
		{"Microsoft Azure", "azure-noreply@microsoft.com"},
		{"OneDrive", "no-reply@sharepointonline.com"},
		{"Google", "no-reply@accounts.google.com"},
		{"Google Calendar", "calendar-notification@google.com"},
		{"Ada Lovelace (via Google Docs)", "drive-shares-dm-noreply@google.com"},
		{"YouTube", "no-reply@youtube.com"},
		{"Apple", "no_reply@email.apple.com"},
		{"Apple", "noreply@post.applecard.apple"},
		{"iCloud", "noreply@icloud.com"},
		{"PayPal", "service@paypal.de"},
		{"PayPal", "paypal@mail.paypal-communication.com"},
		{"service@paypal.com", "service@paypal.com"},
		{"Stripe", "notifications@stripe.com"},
		{"Wise", "noreply@wise.com"},
		{"Klarna", "noreply@klarna.de"},
		{"American Express", "AmericanExpress@welcome.aexp.com"},
		{"Netflix", "info@account.netflix.com"},
		{"Spotify", "no-reply@spotify.com"},
		{"Disney+", "disneyplus@mail.disneyplus.com"},
		{"Steam Support", "noreply@steampowered.com"},
		{"eBay", "ebay@ebay.de"},
		{"Instagram", "security@mail.instagram.com"},
		{"Facebook", "security@facebookmail.com"},
		{"Meta for Business", "noreply@business.facebook.com"},
		{"LinkedIn", "messages-noreply@linkedin.com"},
		{"X", "info@x.com"},
		{"GitHub", "noreply@github.com"},
		{"Dropbox Sign", "noreply@mail.hellosign.com"},
		{"Ada Lovelace via DocuSign", "dse_na4@docusign.net"},
		{"Adobe Acrobat Sign", "adobesign@adobesign.com"},
		{"Zoom", "no-reply@zoom.us"},
		{"Slack", "feedback@slack.com"},
		{"Coinbase", "no-reply@coinbase.com"},
		{"DHL Paket", "noreply@dhl.de"},
		{"DHL", "info@dhl-news.com"},
		{"DHL Express", "noreply@mydhl.express.dhl"},
		{"Deutsche Post", "info@deutschepost.de"},
		{"UPS", "pkginfo@ups.com"},
		{"FedEx", "TrackingUpdates@fedex.com"},
		{"DPD", "noreply@dpd.de"},
		{"La Poste", "noreply@notif.laposte.fr"},
		{"Correos", "info@correos.es"},
		{"Royal Mail", "no-reply@royalmail.com"},
		{"Hetzner Online GmbH", "billing@hetzner.com"},
		{"OVHcloud", "noreply@ovh.com"},
		{"IONOS", "rechnung@ionos.de"},
		{"Cloudflare", "noreply@notify.cloudflare.com"},
		{"Telekom", "kundenservice@telekom.de"},
		{"Société Générale", "info@societegenerale.fr"},
		{"Deutsche Bank", "info@deutsche-bank.de"},
		{"BNP Paribas", "info@mabanque.bnpparibas"},
		{"Booking.com", "noreply@booking.com"},
		{"دیجی\u200cکالا", "info@digikala.com"},
	} {
		if got := saidOf(evidence(tc[0], tc[1], "Your invoice and payment")); got != "" {
			t.Errorf("%s <%s>: said %q", tc[0], tc[1], got)
		}
	}
}

func TestOtherPeopleAreSilent(t *testing.T) {
	for _, tc := range [][2]string{
		// Whole words, never parts of one.
		{"Dawson Lawson", "dawson@example.org"},
		{"Startups Weekly", "news@example.org"},
		{"GitKraken", "hello@example.org"},
		// Ordinary words and surnames count only alone.
		{"Penny Wise", "penny@example.org"},
		{"Schengen Visa Info", "info@example.org"},
		{"Chase Carter", "chase@example.org"},
		{"Andrew McAfee", "andrew@example.org"},
		{"Jim Apple", "jim@example.org"},
		{"Seattle Kraken", "tickets@example.org"},
		{"PTA Zoom Meeting", "pta@example.org"},
		{"Ing. Hans Meier", "hans@example.org"},
		{"La Poste Tunisienne", "info@example.org"},
		{"Mittelbrandenburgische Sparkasse", "info@mbs.de"},
		{"Universidad Industrial de Santander", "info@uis.edu.co"},
		// An honest word a lookalike reading would take for a name.
		{"Royal Mall", "info@example.org"},
		{"DHI Group", "info@example.org"},
		// Initials are not spaced letters unless they stand alone.
		{"A. W. S. Perera", "perera@example.org"},
		// A domain that holds the name is not borrowing it.
		{"Last Week in AWS", "corey@aws-news.example"},
		{"Amazon Watch", "info@amazonwatch.org"},
		{"Hermès", "news@hermes.com"},
		{"Correos de Chile", "info@correos.cl"},
		{"Sparkasse KölnBonn", "info@sparkasse-koelnbonn.de"},
		{"Apple Vacations", "deals@applevacations.com"},
		// The sender is one of the brands named.
		{"Microsoft via LinkedIn", "updates-noreply@linkedin.com"},
		// The name is the address, which names nothing.
		{"amazon.fan@example.org", "amazon.fan@example.org"},
		{"", "billing@example.org"},
	} {
		if got := saidOf(evidence(tc[0], tc[1], "Your invoice and payment")); got != "" {
			t.Errorf("%s <%s>: said %q", tc[0], tc[1], got)
		}
	}
}

func TestBorrowedNamesWarn(t *testing.T) {
	for _, tc := range []struct{ name, from, want string }{
		{"PayPal", "service@example.org", "PayPal example.org"},
		{"service@paypal.com", "x@example.org", "PayPal example.org"},
		{"Amazon.de", "bestellung@example.org", "Amazon example.org"},
		{"Apple Support", "x@icloud.com", "Apple icloud.com"},
		{"Apple ID", "x@example.org", "Apple example.org"},
		{"Microsoft 365 Billing", "x@outlook.com", "Microsoft outlook.com"},
		// Anyone has an address there, whatever name the domain holds.
		{"Google Security", "x@googlemail.com", "Google googlemail.com"},
		{"Microsoft Support", "x@contoso.onmicrosoft.com", "Microsoft onmicrosoft.com"},
		{"La Poste", "x@laposte.net", "La Poste laposte.net"},
		// A label below the registrable name is the sender's to choose.
		{"PayPal", "x@paypal.example.org", "PayPal example.org"},
		{"UPS", "x@groups.example.org", "UPS example.org"},
		{"UPS Delivery", "x@startups.example", "UPS startups.example"},
		// Alone, beside service words in any of the six languages.
		{"Visa Card Services", "x@example.org", "Visa example.org"},
		{"Wise Kundenservice", "x@example.org", "Wise example.org"},
		{"Service client La Poste", "x@example.org", "La Poste example.org"},
		{"X", "x@example.org", "X example.org"},
		{"پشتیبانی دیجی\u200cکالا", "x@example.org", "دیجی\u200cکالا example.org"},
		// A reseller writes under its own name or is in the address book.
		{"AWS via Example Reseller", "aws@example.org", "AWS example.org"},
		{"Meta Business Support", "x@example.org", "Meta Business example.org"},
		{"Disney+", "x@example.org", "Disney+ example.org"},
	} {
		if got, want := saidOf(evidence(tc.name, tc.from, "Hello")), "brands.elsewhere 1 "+tc.want; got != want {
			t.Errorf("%s <%s>: said %q, want %q", tc.name, tc.from, got, want)
		}
	}
}

func TestNamesAreReadAsTheyLook(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"PAYPAL", "PayPal"},
		{"paypal", "PayPal"},
		{"ＰａｙＰａｌ", "PayPal"},         // fullwidth
		{"𝐏𝐚𝐲𝐏𝐚𝐥", "PayPal"},         // mathematical bold
		{"Pay\u200bPal", "PayPal"},   // zero-width space
		{"Pay\u00adPal", "PayPal"},   // soft hyphen
		{"PayPaI", "PayPal"},         // capital I for l
		{"Paypa1", "PayPal"},         // 1 for l
		{"РауРаl", "PayPal"},         // Cyrillic Р, а, у
		{"Ρaypal", "PayPal"},         // Greek Rho
		{"Amaz0n", "Amazon"},         // 0 for o
		{"Аmazon", "Amazon"},         // Cyrillic А
		{"Amázon", "Amazon"},         // an accent
		{"Netfl1x", "Netflix"},       // 1 for i
		{"NetfIix", "Netflix"},       // capital I for l
		{"Nеtflіх", "Netflix"},       // Cyrillic е, і, х
		{"Micrоsоft", "Microsoft"},   // Cyrillic о
		{"Gооgle", "Google"},         // Cyrillic о
		{"GoogIe Support", "Google"}, // capital I for l
		{"L1nkedIn", "LinkedIn"},     // 1 for i
		{"Coinbａse", "Coinbase"},     // one fullwidth letter
		{"A W S", "AWS"},             // spaced letters
		{"A.W.S. Billing", "AWS"},    // dotted letters beside a service word
		{"P a y P a l", "PayPal"},    // spaced letters
		{"D-H-L", "DHL"},             // hyphenated letters
		{"Royal Ma1l", "Royal Mail"}, // 1 for i in a name of two words
		{"Société Générale", "Société Générale"},
		{"SOCIETE GENERALE", "Société Générale"},
		{"Disney Plus", "Disney Plus"},
	} {
		if got, want := saidOf(evidence(tc.name, "x@example.org", "Hello")), "brands.elsewhere 1 "+tc.want+" example.org"; got != want {
			t.Errorf("%q: said %q, want %q", tc.name, got, want)
		}
	}
	// Not read: too short to go by looks, or spelled as honest words are.
	for _, name := range []string{"DH1", "АWS", "Netfllx", "Linkedln", "rnicrosoft", "PAYPAI", "PayPals", "Pay Pal"} {
		if got := saidOf(evidence(name, "x@example.org", "Hello")); got != "" {
			t.Errorf("%q: said %q", name, got)
		}
	}
}

// A sender that names nobody, a subject that names a brand and asks about money.
func TestTheSubjectIsReadWhereTheNameNamesNobody(t *testing.T) {
	for _, tc := range []struct{ name, from, subject, want string }{
		{"Billing Department", "x@example.org", "Your Netflix payment was declined", "brands.subject 1 Netflix example.org"},
		{"Kundenservice", "x@example.org", "Your PayPal account is suspended", "brands.subject 1 PayPal example.org"},
		{"Customer Service", "x@example.org", "Netflix: new films this week", ""},
		{"Example Shop", "orders@example.org", "Your PayPal payment for order 7", ""},
		{"", "friend@example.org", "Netflix password", ""},
		{"Billing", "billing@netflix.com", "Your Netflix payment was declined", ""},
		// A word listed alone is too common in subjects.
		{"Support", "x@example.org", "Your visa payment", ""},
	} {
		if got := saidOf(evidence(tc.name, tc.from, tc.subject)); got != tc.want {
			t.Errorf("%s <%s> %q: said %q, want %q", tc.name, tc.from, tc.subject, got, tc.want)
		}
	}
}

func TestKnownSendersAndReportsAreSilent(t *testing.T) {
	e := evidence("AWS via Example Reseller", "aws@example.org", "Your AWS invoice")
	e.Known = func(addr string) bool { return addr == "aws@example.org" }
	if got := saidOf(e); got != "" {
		t.Errorf("an address in the reader's books: said %q", got)
	}
	e = evidence("Microsoft Outlook", "MicrosoftExchange329e71ec88ae4615bbc36ab6ce41109e@example.org", "Undeliverable: invoice")
	if got := saidOf(e); got == "" {
		t.Fatal("the name alone was not read")
	}
	e.Header.Set("Content-Type", `multipart/report; report-type=delivery-status; boundary="b"`)
	if got := saidOf(e); got != "" {
		t.Errorf("a delivery status report: said %q", got)
	}
}

func TestAReplyToAtAThirdDomainIsALine(t *testing.T) {
	for _, tc := range []struct{ replyTo, want string }{
		{"Refunds <refunds@example.net>", "brands.elsewhere 1 PayPal example.org; brands.replyto 1 example.net"},
		{"help@mail.example.org", "brands.elsewhere 1 PayPal example.org"},
		{"service@paypal.com", "brands.elsewhere 1 PayPal example.org"},
		{"", "brands.elsewhere 1 PayPal example.org"},
	} {
		e := evidence("PayPal", "service@example.org", "Hello")
		var h textproto.Header
		if tc.replyTo != "" {
			h.Set("Reply-To", tc.replyTo)
		}
		e.Header = h
		if got := saidOf(e); got != tc.want {
			t.Errorf("Reply-To %q: said %q, want %q", tc.replyTo, got, tc.want)
		}
	}
}

// The book as shipped: a domain is written as a host is compared, and none is open to anyone.
func TestTheBookIsWellFormed(t *testing.T) {
	if len(held.names) < 100 {
		t.Fatalf("the book holds %d names", len(held.names))
	}
	for _, n := range held.names {
		for _, d := range n.domains {
			if d != strings.ToLower(d) || strings.ContainsAny(d, " @/") || !strings.Contains(d, ".") {
				t.Errorf("%s: domain %q is not a lower-case host", n.said, d)
			}
			if _, err := publicsuffix.EffectiveTLDPlusOne(d); err != nil {
				t.Errorf("%s: domain %q is a public suffix", n.said, d)
			}
			for _, open := range held.open {
				if within(d, open) || within(open, d) {
					t.Errorf("%s: domain %q is open to anyone", n.said, d)
				}
			}
		}
		// A brand at its first listed domain is silent under each of its names.
		if got := saidOf(evidence(n.said, "x@"+n.domains[0], "Hello")); got != "" {
			t.Errorf("%s at %s: said %q", n.said, n.domains[0], got)
		}
	}
}

func TestAMalformedBookIsRefused(t *testing.T) {
	for _, source := range []string{
		`[[brand]]` + "\n" + `names = ["A"]`,
		`[[brand]]` + "\n" + `domains = ["a.example"]`,
		`[[brand]]` + "\n" + `names = ["A"]` + "\n" + `domain = ["a.example"]`,
		`[[brand]]` + "\n" + `names = ["PayPal", "PAYPAL"]` + "\n" + `domains = ["a.example"]`,
		`[[brand]]` + "\n" + `names = ["+"]` + "\n" + `domains = ["a.example"]`,
	} {
		if _, err := read(source); err == nil {
			t.Errorf("read took %q", source)
		}
	}
}
