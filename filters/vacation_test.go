package filters

import "testing"

func TestReplyTextHoldingTheBoundarySurvives(t *testing.T) {
	v := Reply{Name: "away", Text: "Away.\n--alborz-reply\n--=_alborz-reply--\nBack Monday.", Days: 7}
	got, ok := readReply(v.script())
	if !ok {
		t.Fatalf("the page does not read back its own script:\n%s", v.script())
	}
	if got.Text != v.Text {
		t.Errorf("text read back as %q, want %q", got.Text, v.Text)
	}
}
