package slack

import (
	"log"
	"os"
	"path/filepath"

	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/scheduler"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

func isSlackJob(job store.PendingJob) bool {
	if chat.ParseConversationID(job.ConversationID).Provider == chat.ProviderSlack {
		return true
	}
	return job.Source == string(chat.ProviderSlack)
}

func withSlackJobsOnly(run func() bool) bool {
	entries, err := os.ReadDir(store.JobsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return run()
		}
		log.Printf("slack recovery read jobs dir failed: %v", err)
		return false
	}

	holdDir, err := os.MkdirTemp(store.ConfigDir(), "slack-jobs-hold-")
	if err != nil {
		log.Printf("slack recovery hold dir failed: %v", err)
		return false
	}
	defer os.RemoveAll(holdDir)

	moved := make([]string, 0)
	restore := func() {
		for _, name := range moved {
			from := filepath.Join(holdDir, name)
			to := filepath.Join(store.JobsDir(), name)
			if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
				log.Printf("slack recovery restore failed for %s: %v", name, err)
			}
		}
	}
	defer restore()

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var job store.PendingJob
		if err := store.ReadJSON(filepath.Join("jobs", entry.Name()), &job); err != nil {
			continue
		}
		if isSlackJob(job) {
			continue
		}
		from := filepath.Join(store.JobsDir(), entry.Name())
		to := filepath.Join(holdDir, entry.Name())
		if err := os.Rename(from, to); err != nil {
			log.Printf("slack recovery isolate failed for %s: %v", entry.Name(), err)
			return false
		}
		moved = append(moved, entry.Name())
	}

	return run()
}

func RecoverPendingJobs(api messenger, client *oneagent.Client, schedules *scheduler.Store) bool {
	return withSlackJobsOnly(func() bool {
		return dispatch.RecoverPendingJobs(client, schedules, func(job *store.PendingJob) dispatch.Callbacks {
			return slackDispatchCallbacks(api, job)
		})
	})
}

func RetryDeliverableJobs(api messenger, client *oneagent.Client, schedules *scheduler.Store) bool {
	return withSlackJobsOnly(func() bool {
		return dispatch.RetryDeliverableJobs(client, schedules, func(job *store.PendingJob) dispatch.Callbacks {
			return slackDispatchCallbacks(api, job)
		})
	})
}
