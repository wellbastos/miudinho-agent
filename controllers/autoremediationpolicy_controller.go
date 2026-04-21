package controllers

import (
	"context"
	"time"

	sre "github.com/yourorg/miudinho-agent/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type AutoRemediationPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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
	_ = r.Status().Update(ctx, pol)
	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}
