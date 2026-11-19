package mail

import (
	"fmt"
	"strings"
	"testing"
)

type words struct{}

func (words) Tf(key string, args ...any) string {
	return fmt.Sprintf(key+strings.Repeat(" %v", len(args)), args...)
}

// A scam's own domain passes every check, and the reader's inbox holds no domain named aws: real AWS
// mail is from amazon.com. Of its own mail the name check knows no brand.
func TestBrandNameAtAnotherDomainIsNoSignWithoutABook(t *testing.T) {
	book := senderBook{received: map[string]string{"amazon": "amazon.com", "amazonaws": "amazonaws.com"}}
	const from = "support@motulkzn.aynimarketing.co.za"
	e := &Evidence{
		Auth:       &AuthResults{SPF: "pass", DKIM: "pass", DMARC: "pass", DKIMDomain: "aynimarketing.co.za"},
		Subject:    "Action required - Your AWS account is overdue for payment",
		Name:       "Aws Web Service",
		From:       from,
		Named:      book.named("Aws Web Service", from),
		MoneyWords: []string{"payment", " account"},
	}
	if e.Named != nil {
		t.Fatalf("the reader's own mail named %+v", e.Named)
	}
	got := Indicators(e)
	if len(got) != 1 || got[0].Key != "indicator.moneysubject" || got[0].Grade != Fact {
		t.Errorf("indicators = %+v, want the money subject as a fact", got)
	}
	if mark := Mark(got, false); mark != Fact || len(Warnings(got)) != 0 {
		t.Errorf("mark %d, warnings %+v; the core alone has nothing to say", mark, Warnings(got))
	}
}

// A check a build option brings is run with the core's, and its warning beside a money subject shows both.
func TestAddedCheckWarnsBesideTheMoneySubject(t *testing.T) {
	held := added
	t.Cleanup(func() { added = held })
	AddCheck(func(e *Evidence) []Indicator {
		if !e.MoneySubject() {
			return []Indicator{{Key: "x.quiet"}}
		}
		return []Indicator{{Key: "x.loud", Grade: Alarm}}
	})
	e := &Evidence{Subject: "Your payment", MoneyWords: []string{"payment"}}
	got := Indicators(e)
	if len(got) != 2 || got[0].Key != "x.loud" || got[1].Key != "indicator.moneysubject" || got[1].Grade != Caution {
		t.Errorf("indicators = %+v, want the alarm, then the money subject as a caution", got)
	}
	if Mark(got, false) != Alarm || Mark(got, true) != Fact {
		t.Error("the alarm did not mark the message, or marked one kept as not junk")
	}
	e.Subject = "Hello"
	if got := Warnings(Indicators(e)); len(got) != 0 {
		t.Errorf("a fact warned: %+v", got)
	}
}

// A domain stays left to right in a right-to-left sentence, and nothing a sender wrote is markup.
func TestIndicatorTextIsolatesAndEscapes(t *testing.T) {
	got := Indicator{Key: "k", Args: []any{"<b>", LTR("a&b.example"), 3}}.Text(words{})
	if want := `k &lt;b&gt; <bdi class="ltr">a&amp;b.example</bdi> 3`; string(got) != want {
		t.Errorf("text = %s, want %s", got, want)
	}
}
