package web_test

import (
	"testing"
	"time"

	"git.mehdix.org/alborz/web"
)

// A cooldown says a warning once per while, and Heard says it again at once.
func TestOnceForSaysAgainAfterItsWhile(t *testing.T) {
	var s web.Session
	if !s.OnceFor("trouble", 10*time.Millisecond) {
		t.Fatal("the first hearing is not said")
	}
	if s.OnceFor("trouble", 10*time.Millisecond) {
		t.Error("the second hearing within the while is said again")
	}
	s.Heard("trouble")
	if !s.OnceFor("trouble", time.Minute) {
		t.Error("Heard did not end the cooldown")
	}
}
