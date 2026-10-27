package dav

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDiscoveryWaitIsScopedAndCancelable(t *testing.T) {
	ready, pending := make(chan struct{}), make(chan struct{})
	close(ready)
	p := &Provider{discovery: map[string]<-chan struct{}{"ready.test": ready, "pending.test": pending}}
	for _, domain := range []string{"ready.test", "unlisted.test"} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := p.awaitDiscovery(ctx, domain)
		cancel()
		if err != nil {
			t.Errorf("%s waited on another domain's probe: %v", domain, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.awaitDiscovery(ctx, "pending.test"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled wait: %v", err)
	}
	p.discovery[""] = pending
	if err := p.awaitDiscovery(ctx, "unlisted.test"); !errors.Is(err, context.Canceled) {
		t.Errorf("default-domain wait: %v", err)
	}
}
