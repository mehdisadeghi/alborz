package alborzcarddav

import (
	"testing"

	"github.com/emersion/go-vcard"
)

func TestWithValuesKeepsTheParametersOfEditedFields(t *testing.T) {
	work := &vcard.Field{Value: "old@example.org", Params: vcard.Params{vcard.ParamType: {"work"}}}
	got := withValues([]*vcard.Field{work}, []string{" new@example.org ", "second@example.org", ""})
	if len(got) != 2 {
		t.Fatalf("got %d fields, want 2", len(got))
	}
	if got[0].Value != "new@example.org" || got[0].Params.Get(vcard.ParamType) != "work" {
		t.Errorf("the first address lost its parameters: %v %v", got[0].Value, got[0].Params)
	}
	if got[1].Value != "second@example.org" || len(got[1].Params) != 0 {
		t.Errorf("a new address carries parameters it was never given: %v", got[1].Params)
	}
	if got := withValues([]*vcard.Field{work}, []string{" "}); got != nil {
		t.Errorf("blank input kept a field: %v", got)
	}
}
