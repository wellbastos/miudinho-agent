package main

import (
	"flag"
	"os"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/controllers"
	"github.com/wellbastos/miudinho-agent/internal/alertmanager"
	"github.com/wellbastos/miudinho-agent/internal/config"
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
	))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         cfg.Execution.LeaderElection,
		LeaderElectionID:       "miudinho-agent.o11y.io",
	})
	if err != nil {
		os.Exit(1)
	}

	recorder := mgr.GetEventRecorderFor("miudinho-agent")
	if err := (&controllers.PredictiveIncidentReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Config:   cfg,
		Recorder: recorder,
	}).SetupWithManager(mgr); err != nil {
		os.Exit(1)
	}
	if err := (&controllers.AutoRemediationPolicyReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: recorder,
	}).SetupWithManager(mgr); err != nil {
		os.Exit(1)
	}
	if err := (&controllers.SLOPolicyReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Config:   cfg,
		Recorder: recorder,
	}).SetupWithManager(mgr); err != nil {
		os.Exit(1)
	}

	_ = mgr.AddHealthzCheck("healthz", healthz.Ping)
	_ = mgr.AddReadyzCheck("readyz", healthz.Ping)

	engine := rca.NewEngine(cfg)
	handler := alertmanager.NewHandler(mgr.GetClient())
	if err := mgr.Add(alertmanager.NewServer(cfg, handler, engine)); err != nil {
		os.Exit(1)
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		os.Exit(1)
	}
}
