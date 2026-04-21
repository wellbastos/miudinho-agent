package controllers

import (
	"context"
	"time"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type AutoRemediationPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *AutoRemediationPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sre.AutoRemediationPolicy{}).
		Complete(r)
}

func (r *AutoRemediationPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pol := &sre.AutoRemediationPolicy{}
	if err := r.Get(ctx, req.NamespacedName, pol); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	pol.Status.ObservedGeneration = pol.Generation
	if err := r.Status().Update(ctx, pol); err != nil {
		return ctrl.Result{}, err
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(pol, "Normal", "Synced", "Policy observed generation updated to %d", pol.Generation)
	}
	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}
