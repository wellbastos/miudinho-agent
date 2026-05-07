package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordResolvedAlert(t *testing.T) {
	before := testutil.ToFloat64(resolvedAlertsTotal.WithLabelValues("alertmanager", "default", "checkout", "critical"))

	RecordResolvedAlert("alertmanager", "default", "checkout", "critical")

	after := testutil.ToFloat64(resolvedAlertsTotal.WithLabelValues("alertmanager", "default", "checkout", "critical"))
	if after != before+1 {
		t.Fatalf("expected resolved alerts counter to increment by 1, got before=%v after=%v", before, after)
	}
}

func TestRecordReconcile(t *testing.T) {
	totalBefore := testutil.ToFloat64(reconcileTotal.WithLabelValues("predictiveincident", "alertmanager", "Resolved", "success"))
	durationBefore := ReconcileDurationSampleSumForTest("predictiveincident", "alertmanager", "success")

	RecordReconcile("predictiveincident", "alertmanager", "Resolved", "success", 150*time.Millisecond)

	totalAfter := testutil.ToFloat64(reconcileTotal.WithLabelValues("predictiveincident", "alertmanager", "Resolved", "success"))
	durationAfter := ReconcileDurationSampleSumForTest("predictiveincident", "alertmanager", "success")
	if totalAfter != totalBefore+1 {
		t.Fatalf("expected reconcile counter to increment by 1, got before=%v after=%v", totalBefore, totalAfter)
	}
	if durationAfter <= durationBefore {
		t.Fatalf("expected reconcile histogram sample sum to increase, got before=%v after=%v", durationBefore, durationAfter)
	}
}

func TestRecordAlertPollAndEscalationMetrics(t *testing.T) {
	beforePoll := testutil.ToFloat64(alertPollRequestsTotal.WithLabelValues("prometheus", "success"))
	beforeCollected := testutil.ToFloat64(alertsCollectedTotal.WithLabelValues("prometheus"))
	beforeDedup := testutil.ToFloat64(alertsDeduplicatedTotal)
	beforeSync := testutil.ToFloat64(polledIncidentsSyncedTotal.WithLabelValues("created"))
	beforeEscalation := testutil.ToFloat64(escalationNotificationsTotal.WithLabelValues("google_chat", "success"))

	RecordAlertPoll("prometheus", "success", 50*time.Millisecond, 3)
	RecordAlertDeduplicated(2)
	RecordPolledIncidentSync("created")
	RecordEscalationNotification("google_chat", "success")

	afterPoll := testutil.ToFloat64(alertPollRequestsTotal.WithLabelValues("prometheus", "success"))
	afterCollected := testutil.ToFloat64(alertsCollectedTotal.WithLabelValues("prometheus"))
	afterDedup := testutil.ToFloat64(alertsDeduplicatedTotal)
	afterSync := testutil.ToFloat64(polledIncidentsSyncedTotal.WithLabelValues("created"))
	afterEscalation := testutil.ToFloat64(escalationNotificationsTotal.WithLabelValues("google_chat", "success"))

	if afterPoll != beforePoll+1 {
		t.Fatalf("expected poll counter increment, before=%v after=%v", beforePoll, afterPoll)
	}
	if afterCollected != beforeCollected+3 {
		t.Fatalf("expected collected counter increment, before=%v after=%v", beforeCollected, afterCollected)
	}
	if afterDedup != beforeDedup+2 {
		t.Fatalf("expected deduplicated counter increment, before=%v after=%v", beforeDedup, afterDedup)
	}
	if afterSync != beforeSync+1 {
		t.Fatalf("expected incident sync counter increment, before=%v after=%v", beforeSync, afterSync)
	}
	if afterEscalation != beforeEscalation+1 {
		t.Fatalf("expected escalation counter increment, before=%v after=%v", beforeEscalation, afterEscalation)
	}
}
