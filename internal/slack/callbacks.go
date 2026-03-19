package slack

import (
	"sync"
	"time"

	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/store"
)

func slackDispatchCallbacks(api messenger, job *store.PendingJob) dispatch.Callbacks {
	status := newRunningStatus(api, job)
	var once sync.Once
	stopInitial := func() {}
	if job.Status != "ready" && job.Status != "delivered" {
		done := make(chan struct{})
		go func() {
			select {
			case <-time.After(1200 * time.Millisecond):
				status.show("")
			case <-done:
			}
		}()
		stopInitial = func() {
			once.Do(func() { close(done) })
		}
	}
	return dispatch.Callbacks{
		OnActivity: func(activity string) {
			stopInitial()
			status.show(activity)
		},
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
