package webex

import (
	"sync"
	"time"

	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/store"
)

func webexDispatchCallbacks(api messenger, job *store.PendingJob) dispatch.Callbacks {
	status := newRunningStatus(api, job)
	var once sync.Once
	stopInitial := func() {}
	if job.Status != "ready" && job.Status != "delivered" {
		done := make(chan struct{})
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			timer := time.NewTimer(1200 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
				status.show("")
			case <-done:
			}
		}()
		stopInitial = func() {
			once.Do(func() {
				close(done)
				<-stopped
			})
		}
	}
	return dispatch.Callbacks{
		OnActivity: func(string) {},
		OnResult: func(result string) error {
			stopInitial()
			job.Result = result
			return DeliverJobResult(api, job)
		},
		OnStatusClear: func() {
			stopInitial()
			status.clear()
		},
		OnDone: stopInitial,
	}
}
