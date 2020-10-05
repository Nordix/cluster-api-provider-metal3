/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"

	//"fmt"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"

	capm3 "github.com/metal3-io/cluster-api-provider-metal3/api/v1alpha4"
	"github.com/metal3-io/cluster-api-provider-metal3/baremetal"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	//capi "sigs.k8s.io/cluster-api/api/v1alpha3"
	//"sigs.k8s.io/cluster-api/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Metal3RemediationReconciler reconciles a Metal3Remediation object
type Metal3RemediationReconciler struct {
	client.Client
	ManagerFactory baremetal.ManagerFactoryInterface
	Log            logr.Logger
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=metal3remediations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=metal3remediations/status,verbs=get;update;patch

func (r *Metal3RemediationReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	remediationLog := r.Log.WithValues("metal3remediation", req.NamespacedName)

	// Fetch the Metal3Remediation instance.
	metal3Remediation := &capm3.Metal3Remediation{}

	if err := r.Client.Get(ctx, req.NamespacedName, metal3Remediation); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	helper, err := patch.NewHelper(metal3Remediation, r.Client)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to init patch helper")
	}
	// Always patch metal3Remediation when exiting this function so we can persist any metal3Remediation changes.
	defer func() {
		err := helper.Patch(ctx, metal3Remediation)
		if err != nil {
			remediationLog.Error(err, "failed to Patch metal3Remediation")
		}
	}()

	// Fetch the Machine.
	capiMachine, err := util.GetOwnerMachine(ctx, r.Client, metal3Remediation.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "metal3Remediation's owner Machine could not be retrieved")
	}
	remediationLog = remediationLog.WithValues("machine", capiMachine.Name)

	// Fetch Metal3Machine
	metal3Machine := capm3.Metal3Machine{}
	key := client.ObjectKey{
		Name:      capiMachine.Spec.InfrastructureRef.Name,
		Namespace: capiMachine.Spec.InfrastructureRef.Namespace,
	}
	err = r.Get(ctx, key, &metal3Machine)
	if apierrors.IsNotFound(err) || err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "metal3machine not found")
	}

	remediationLog = remediationLog.WithValues("metal3machine", metal3Machine.Name)

	// Create a helper for managing the remediation object.
	remediationMgr, err := r.ManagerFactory.NewRemediationManager(metal3Remediation, &metal3Machine, capiMachine, remediationLog)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to create helper for managing the metal3remediation")
	}

	// Handle deleted remediation
	if !metal3Remediation.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, remediationMgr)
	}

	// Handle non-deleted remediation
	return r.reconcileNormal(ctx, remediationMgr)
}

func (r *Metal3RemediationReconciler) reconcileNormal(ctx context.Context,
	remediationMgr baremetal.RemediationManagerInterface,
) (ctrl.Result, error) {

	// If host is gone, exit early
	host, _, err := remediationMgr.GetUnhealthyHost(ctx)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "unable to find a host for unhealthy machine")
	}

	// If user has set bmh.Spec.Online to false or annotated the host with any poweroff annotations
	// do not try to remediate the host
	if !remediationMgr.OnlineStatus(host) || remediationMgr.HasRebootAnnotation(host) {
		r.Log.Info("Host is powered off, unable to remediatiate")
	}

	// If the Metal3Remediation doesn't have finalizer, add it.
	//remediationMgr.SetFinalizer()

	// TODO in normal situtation BMO should be able to power on the host.
	// handle the cases where BMO is unable to retain nodes power status
	// if host.Status.PoweredOn == false && host.Spec.Online == true {
	// // 	// HANDLE THIS CASE SEPARATELY
	// // 	return nil
	// }

	remediationType := remediationMgr.GetRemediationType()

	if remediationType == capm3.RebootRemediationStrategy {
		// If no phase set, default to pending
		if remediationMgr.GetRemediationPhase() == "" {
			remediationMgr.SetRemediationPhase(capm3.PhaseRunning)
		}

		switch remediationMgr.GetRemediationPhase() {
		case capm3.PhaseRunning:
			// host is not rebooted yet
			if remediationMgr.GetLastRemediatedTime() == nil {
				r.Log.Info("Rebooting the host")
				err := remediationMgr.SetAnnotation(ctx, "reboot")
				if err != nil {
					return ctrl.Result{}, errors.Wrap(err, "error setting reboot annotation")
				}
				now := metav1.Now()
				remediationMgr.SetLastRemediationTime(&now)
				remediationMgr.IncreaseRetryCount()
			}

			if remediationMgr.RetryLimitIsSet() && !remediationMgr.HasReachRetryLimit() {
				okToRemediate, nextRemediation := remediationMgr.TimeToRemediate(remediationMgr.GetTimeout().Duration)

				if okToRemediate {
					err := remediationMgr.SetAnnotation(ctx, "reboot")
					if err != nil {
						return ctrl.Result{}, errors.Wrapf(err, "error setting reboot annotation")
					}
					now := metav1.Now()
					remediationMgr.SetLastRemediationTime(&now)
					remediationMgr.IncreaseRetryCount()
				}

				if nextRemediation > 0 {
					// Not yet time to remediatate, requeue
					return ctrl.Result{RequeueAfter: nextRemediation}, nil
				}

			} else {
				remediationMgr.SetRemediationPhase(capm3.PhaseWaiting)
			}
		case capm3.PhaseWaiting:
			okToStop, nextCheck := remediationMgr.TimeToRemediate(remediationMgr.GetTimeout().Duration)

			if okToStop {
				// If machine is still unhealthy delete after last remediation attempt, delete unhealthy machine.
				remediationMgr.SetRemediationPhase(capm3.PhaseDeleting)
				remediationMgr.DeleteCapiMachine(ctx)

				// Remediation failed set unhealthy annotation on BMH
				err := remediationMgr.SetAnnotation(ctx, "unhealthy")
				if err != nil {
					return ctrl.Result{}, errors.Wrapf(err, "error setting unhealthy annotation")
				}
			}

			if nextCheck > 0 {
				// Not yet time to stop remediation, requeue
				return ctrl.Result{RequeueAfter: nextCheck}, nil
			}
		default:

		}
	}
	return ctrl.Result{}, nil
}

func (r *Metal3RemediationReconciler) reconcileDelete(ctx context.Context,
	remediationMgr baremetal.RemediationManagerInterface,
) (ctrl.Result, error) {

	// metal3remediation is marked for deletion and ready to be deleted,
	// so remove the finalizer.
	// remediationMgr.UnsetFinalizer()
	return ctrl.Result{}, nil
}

func (r *Metal3RemediationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&capm3.Metal3Remediation{}).
		Complete(r)
}
