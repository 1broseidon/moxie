package bot

import (
	"strconv"

	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/store"
)

func maxTelegramSourceEventID(jobs []store.PendingJob) int {
	maxID := 0
	for _, job := range jobs {
		if job.Source != string(chat.ProviderTelegram) || job.SourceEventID == "" {
			continue
		}
		id, err := strconv.Atoi(job.SourceEventID)
		if err != nil {
			continue
		}
		if id > maxID {
			maxID = id
		}
	}
	return maxID
}
