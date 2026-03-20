package slack

import (
	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/scheduler"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

func isSlackJob(job store.PendingJob) bool {
	if job.Source == "subagent" {
		return false
	}
	if chat.ParseConversationID(job.ConversationID).Provider == chat.ProviderSlack {
		return true
	}
	return job.Source == string(chat.ProviderSlack)
}

func RecoverPendingJobs(api messenger, client *oneagent.Client, schedules *scheduler.Store) bool {
	return dispatch.RecoverPendingJobs(client, schedules, func(job *store.PendingJob) dispatch.Callbacks {
		return slackDispatchCallbacks(api, job)
	}, isSlackJob)
}

func RetryDeliverableJobs(api messenger, client *oneagent.Client, schedules *scheduler.Store) bool {
	return dispatch.RetryDeliverableJobs(client, schedules, func(job *store.PendingJob) dispatch.Callbacks {
		return slackDispatchCallbacks(api, job)
	}, isSlackJob)
}
