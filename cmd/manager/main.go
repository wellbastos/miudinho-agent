package main

import (
	"flag"
	"os"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/controllers"
	"github.com/wellbastos/miudinho-agent/internal/alertmanager"
	"github.com/wellbastos/miudinho-agent/internal/config"
	"github.com/wellbastos/miudinho-agent/internal/incidentpoller"
	"github.com/wellbastos/miudinho-agent/internal/incidents"
	"github.com/wellbastos/miudinho-agent/internal/rca"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(sre.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	cfg := config.LoadFromEnv()
	leaderElection := cfg.Execution.LeaderElection

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8081", "metrics addr")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8080", "probe addr")
	flag.BoolVar(&leaderElection, "leader-elect", cfg.Execution.LeaderElection, "enable leader election")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	cfg.Execution.LeaderElection = leaderElection

	ctrl.SetLogger(zap.New(
		zap.UseFlagOptions(&opts),
		zap.JSONEncoder(),
		zap.WriteTo(os.Stdout),
	))

	setupLog := ctrl.Log.WithName("setup")
	setupLog.Info("starting miudinho-agent",
		"promUrl", cfg.Observability.PromURL,
		"alertmanagerApiUrl", cfg.Observability.AlertmanagerAPIURL,
		"alertmanagerOutboundUrl", cfg.Observability.AlertmanagerOutboundURL,
		"alertSources", cfg.AlertPolling.Sources,
		"alertPollInterval", cfg.AlertPolling.Interval,
		"alertWebhookAddr", cfg.HTTP.AlertWebhookAddr,
		"llmRoutingMode", cfg.LLM.RoutingMode,
		"executeActions", cfg.Execution.ExecuteActions,
		"autoObserveOnly", cfg.Execution.AutoObserveOnly,
		"leaderElection", cfg.Execution.LeaderElection,
	)

	// O rate limiter padrão do client-go (QPS=5, Burst=10) throttlea API calls
	// quando há muitos PredictiveIncidents em reconciliação simultânea, causando
	// "context canceled" enquanto aguarda um token.
	restCfg := ctrl.GetConfigOrDie()
	restCfg.QPS = 100
	restCfg.Burst = 200

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         cfg.Execution.LeaderElection,
		LeaderElectionID:       "miudinho-agent.o11y.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	recorder := mgr.GetEventRecorderFor("miudinho-agent")
	if err := (&controllers.PredictiveIncidentReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Config:    cfg,
		Recorder:  recorder,
		IgnoredNS: incidents.IgnoredNamespaceSet(cfg.AlertPolling.IgnoredNamespaces),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PredictiveIncident")
		os.Exit(1)
	}
	if err := (&controllers.AutoRemediationPolicyReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: recorder,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AutoRemediationPolicy")
		os.Exit(1)
	}
	if err := (&controllers.SLOPolicyReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Config:   cfg,
		Recorder: recorder,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SLOPolicy")
		os.Exit(1)
	}

	_ = mgr.AddHealthzCheck("healthz", healthz.Ping)
	_ = mgr.AddReadyzCheck("readyz", healthz.Ping)

	engine := rca.NewEngine(cfg)
	handler := alertmanager.NewHandler(mgr.GetClient(), cfg.AlertPolling.IgnoredNamespaces)
	if err := mgr.Add(alertmanager.NewServer(cfg, handler, engine)); err != nil {
		setupLog.Error(err, "unable to add alertmanager webhook server")
		os.Exit(1)
	}
	if err := mgr.Add(incidentpoller.New(mgr.GetClient(), cfg)); err != nil {
		setupLog.Error(err, "unable to add incident poller")
		os.Exit(1)
	}

	setupLog.Info("all components registered, starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "manager exited with error")
		os.Exit(1)
	}
}
