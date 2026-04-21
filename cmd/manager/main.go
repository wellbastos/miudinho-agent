package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"time"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/controllers"
	"github.com/wellbastos/miudinho-agent/internal/alertmanager"
	"github.com/wellbastos/miudinho-agent/internal/rca"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(sre.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8081", "metrics addr")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8080", "probe addr")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         false,
	})
	if err != nil {
		os.Exit(1)
	}

	if err := (&controllers.PredictiveIncidentReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr); err != nil {
		os.Exit(1)
	}
	if err := (&controllers.AutoRemediationPolicyReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr); err != nil {
		os.Exit(1)
	}
	if err := (&controllers.SLOPolicyReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr); err != nil {
		os.Exit(1)
	}

	_ = mgr.AddHealthzCheck("healthz", healthz.Ping)
	_ = mgr.AddReadyzCheck("readyz", healthz.Ping)

	webhookAddr := getenv("ALERT_WEBHOOK_ADDR", ":8090")
	go func() {
		engine := rca.NewEngine(systemPrompt(), approverPrompt())
		h := alertmanager.NewHandler(mgr.GetClient())

		mux := http.NewServeMux()
		mux.HandleFunc("/api/v1/alerts", h.HandleAlerts)
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := rca.WithTimeout()
			defer cancel()
			engine.Healthcheck(ctx)

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"engine": engine.Snapshot(),
			})
		})

		srv := &http.Server{
			Addr:              webhookAddr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		_ = srv.ListenAndServe()
	}()

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		os.Exit(1)
	}
}

func getenv(k, def string) string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v
}

func systemPrompt() string {
	return getenv("SYSTEM_PROMPT", `Você é um Agente SRE de produção. Responda sempre em JSON válido com classification, confidence, summary, evidence, prom_queries, actions, rollback_or_next_steps, escalation. Para predictive, priorize low-risk. Confidence < 0.70 => sem mudanças.`)
}

func approverPrompt() string {
	return getenv("APPROVER_PROMPT", `Você é o Change Approver. Responda somente JSON com approved, risk_level, reasons, required_changes. Bloqueie ações arriscadas e qualquer confidence < 0.70.`)
}
