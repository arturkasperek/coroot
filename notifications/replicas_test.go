//go:build e2e

package notifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/db/dbtest"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two Coroot replicas share the notifications not sent yet: only the primary
// sends them, once, even when its retry and a send of a new notification run
// at the same time.
func TestAlertNotificationIsSentOnceByTwoReplicas(t *testing.T) {
	var sent atomic.Int64
	// as slow as a real destination: sends that overlap would send it twice
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		time.Sleep(300 * time.Millisecond)
	}))
	defer webhook.Close()

	primary, dsn := dbtest.NewWithDSN(t)
	other := dbtest.Open(t, dsn)
	require.True(t, primary.GetPrimaryLock(context.Background()))

	project := &db.Project{Name: "notifications"}
	require.NoError(t, primary.SaveProject(project))
	alerts := true
	project.Settings.Integrations.Webhook = &db.IntegrationWebhook{Url: webhook.URL, Alerts: &alerts, AlertTemplate: "{{ .Status }} {{ .Summary }}"}
	require.NoError(t, primary.SaveProjectSettings(project))
	primary.PutAlertNotification(db.AlertNotification{
		ProjectId:   project.Id,
		AlertId:     "alert-1",
		RuleId:      "rule-1",
		Destination: db.IncidentNotificationDestination{IntegrationType: db.IntegrationTypeWebhook},
		Timestamp:   timeseries.Now(),
		Status:      model.CRITICAL,
		Details:     &db.AlertNotificationDetails{RuleName: "rule", Severity: "critical", Summary: "it broke"},
	})

	onPrimary, onOther := &AlertNotifier{db: primary}, &AlertNotifier{db: other}
	var wait sync.WaitGroup
	for _, send := range []func(){onPrimary.retry, onPrimary.sendAlerts, onOther.retry, onOther.retry} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			send()
		}()
	}
	wait.Wait()
	assert.Equal(t, int64(1), sent.Load(), "sent once")

	onPrimary.retry()
	onOther.retry()
	assert.Equal(t, int64(1), sent.Load(), "and not again")
}
