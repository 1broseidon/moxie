package main

import (
	"strings"
	"testing"
	"time"

	"github.com/1broseidon/moxie/internal/scheduler"
)

func TestResolveScheduleTrigger(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		at      string
		every   string
		cron    string
		want    scheduler.Trigger
		wantErr string
	}{
		{name: "in", in: "5m", want: scheduler.TriggerAt},
		{name: "at", at: "2026-03-18T10:00:00-05:00", want: scheduler.TriggerAt},
		{name: "every", every: "30m", want: scheduler.TriggerInterval},
		{name: "cron", cron: "0 1 * * *", want: scheduler.TriggerCalendar},
		{name: "missing", wantErr: "missing schedule trigger: use --in, --at, --every, or --cron"},
		{name: "multiple", in: "5m", every: "30m", wantErr: "use exactly one of --in, --at, --every, or --cron"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveScheduleTrigger(tt.in, tt.at, tt.every, tt.cron)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("resolveScheduleTrigger() err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveScheduleTrigger() err = %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolveScheduleTrigger() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestFormatScheduleIntervalCompactsCanonicalDuration(t *testing.T) {
	if got := formatScheduleInterval("1h30m0s"); got != "1h30m" {
		t.Fatalf("formatScheduleInterval() = %q, want %q", got, "1h30m")
	}
	if got := formatScheduleInterval("2h0m0s"); got != "2h" {
		t.Fatalf("formatScheduleInterval() = %q, want %q", got, "2h")
	}
	if got := formatScheduleInterval("bad"); got != "bad" {
		t.Fatalf("formatScheduleInterval() = %q, want %q", got, "bad")
	}
}

func TestRenderScheduleIntervalUsesFriendlyDuration(t *testing.T) {
	prevLocal := time.Local
	loc := time.FixedZone("CDT", -5*60*60)
	time.Local = loc
	t.Cleanup(func() {
		time.Local = prevLocal
	})

	sc := scheduler.Schedule{
		ID:        "sch-interval",
		Action:    scheduler.ActionDispatch,
		Text:      "Check queue depth",
		CreatedAt: time.Date(2026, 3, 17, 21, 0, 0, 0, loc),
		NextRun:   time.Date(2026, 3, 17, 21, 30, 0, 0, loc),
		Spec: scheduler.ScheduleSpec{
			Trigger:  scheduler.TriggerInterval,
			Interval: "30m0s",
		},
		ConversationID: "telegram:123",
		Backend:        "claude",
		ThreadID:       "thread-1",
		Sync: scheduler.ScheduleSync{
			ManagedBy: scheduler.ManagedByInProcess,
			State:     scheduler.SyncStateFallback,
		},
	}

	headline := formatScheduleHeadline(sc)
	if !strings.Contains(headline, "every 30m next 2026-03-17 21:30 CDT") {
		t.Fatalf("formatScheduleHeadline() = %q", headline)
	}

	rendered := renderSchedule(sc)
	if !strings.Contains(rendered, "Trigger: every 30m") {
		t.Fatalf("renderSchedule() missing interval trigger: %q", rendered)
	}
	if strings.Contains(rendered, "30m0s") {
		t.Fatalf("renderSchedule() leaked canonical interval: %q", rendered)
	}
}
