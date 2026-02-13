package routing

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestEventsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx, events := RegisterForQueryEvents(ctx)
	goch := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 100 {
			PublishQueryEvent(ctx, &QueryEvent{Extra: fmt.Sprint(i)})
		}
		close(goch)
		for i := 100; i < 1000; i++ {
			PublishQueryEvent(ctx, &QueryEvent{Extra: fmt.Sprint(i)})
		}
	}()
	go func() {
		defer wg.Done()
		i := 0
		for e := range events {
			if i < 100 {
				if e.Extra != fmt.Sprint(i) {
					t.Errorf("expected %d, got %s", i, e.Extra)
				}
			}
			i++
		}
		if i < 100 {
			t.Errorf("expected at least 100 events, got %d", i)
		}
	}()
	<-goch
	cancel()
	wg.Wait()
}
