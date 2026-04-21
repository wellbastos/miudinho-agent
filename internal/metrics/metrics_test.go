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
