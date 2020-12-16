/*
Copyright 2020 The Kubernetes Authors.

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

package baremetal

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/cache"

	bmh "github.com/metal3-io/baremetal-operator/apis/metal3.io/v1alpha1"
	capm3 "github.com/metal3-io/cluster-api-provider-metal3/api/v1alpha4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	capi "sigs.k8s.io/cluster-api/api/v1alpha3"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	rebootAnnotation = "reboot.metal3.io"
)

// RemediationManagerInterface is an interface for a RemediationManager
type RemediationManagerInterface interface {
	SetFinalizer()
	UnsetFinalizer()
	TimeToRemediate(timeout time.Duration) (bool, time.Duration)
	SetAnnotation(ctx context.Context, annotation string) error
	GetUnhealthyHost(ctx context.Context) (*bmh.BareMetalHost, *patch.Helper, error)
	HasRebootAnnotation(host *bmh.BareMetalHost) bool
	OnlineStatus(host *bmh.BareMetalHost) bool
	GetRemediationType() capm3.RemediationType
	RetryLimitIsSet() bool
	SetRemediationPhase(phase string)
	GetRemediationPhase() string
	GetLastRemediatedTime() *metav1.Time
	SetLastRemediationTime(remediationTime *metav1.Time)
	HasReachRetryLimit() bool
	GetTimeout() *metav1.Duration
	IncreaseRetryCount()
	DeleteCapiMachine(ctx context.Context) error
}

// RemediationManager is responsible for performing remediation reconciliation
type RemediationManager struct {
	Client            client.Client
	Metal3Remediation *capm3.Metal3Remediation
	Metal3Machine     *capm3.Metal3Machine
	Machine           *capi.Machine
	Log               logr.Logger
}

// NewRemediationManager returns a new helper for managing a Metal3Remediation object
func NewRemediationManager(client client.Client,
	metal3remediation *capm3.Metal3Remediation, metal3Machine *capm3.Metal3Machine, machine *capi.Machine,
	remediationLog logr.Logger) (*RemediationManager, error) {

	return &RemediationManager{
		Client:            client,
		Metal3Remediation: metal3remediation,
		Metal3Machine:     metal3Machine,
		Machine:           machine,
		Log:               remediationLog,
	}, nil
}

// SetFinalizer sets finalizer
func (r *RemediationManager) SetFinalizer() {
	// If the Metal3Remediation doesn't have finalizer, add it.
	if !Contains(r.Metal3Remediation.Finalizers, capm3.RemediationFinalizer) {
		r.Metal3Remediation.Finalizers = append(r.Metal3Remediation.Finalizers,
			capm3.RemediationFinalizer,
		)
	}
}

// UnsetFinalizer unsets finalizer
func (r *RemediationManager) UnsetFinalizer() {
	// Cluster is deleted so remove the finalizer.
	r.Metal3Remediation.Finalizers = Filter(r.Metal3Remediation.Finalizers,
		capm3.RemediationFinalizer,
	)
}

// timeToRemediate checks if it is time to execute a next remediation step.
func (r *RemediationManager) TimeToRemediate(timeout time.Duration) (bool, time.Duration) {
	now := time.Now()

	// status is not updated yet
	if r.Metal3Remediation.Status.LastRemediated == nil {
		return false, timeout
	}

	//if r.Metal3Remediation.Status.LastRemediated.Add(r.Metal3Remediation.Spec.Strategy.Timeout.Duration).Before(now)
	if r.Metal3Remediation.Status.LastRemediated.Add(timeout).Before(now) {
		return true, time.Duration(0)
	}

	lastRemediated := now.Sub(r.Metal3Remediation.Status.LastRemediated.Time)
	nextRemediation := timeout - lastRemediated + time.Second
	return false, nextRemediation
}

// SetPauseAnnotation sets the pause annotations on associated bmh
func (r *RemediationManager) SetAnnotation(ctx context.Context, annotation string) error {
	// look for associated BMH
	host, helper, err := r.GetUnhealthyHost(ctx)
	if err != nil {
		return err
	}
	if host == nil {
		return nil
	}

	if annotation == "reboot" {
		r.Log.Info("Adding Reboot annotation to BareMetalHost")
		host.Annotations[rebootAnnotation] = "reboot.metal3.io="
	}

	if annotation == "unhealthy" {
		r.Log.Info("Adding Reboot annotation to BareMetalHost")
		host.Annotations[capm3.UnhealthyAnnotation] = "capi.metal3.io/unhealthy="
	}

	for annotation := range host.GetAnnotations() {
		r.Log.Info("ANNOTATION", annotation)
	}

	// Setting annotation with BMH status
	newAnnotation, err := json.Marshal(&host.Status)
	if err != nil {
		return errors.Wrap(err, "failed to marshall status annotation")
	}
	host.Annotations[bmh.StatusAnnotation] = string(newAnnotation)
	return helper.Patch(ctx, host)
}

// getUnhealthyHost gets the associated host for unhealthy machine. Returns nil if not found. Assumes the
// host is in the same namespace as the unhealthy machine.
func (r *RemediationManager) GetUnhealthyHost(ctx context.Context) (*bmh.BareMetalHost, *patch.Helper, error) {
	host, err := getUnhealthyHost(ctx, r.Metal3Machine, r.Client, r.Log)
	if err != nil || host == nil {
		return host, nil, err
	}
	helper, err := patch.NewHelper(host, r.Client)
	return host, helper, err
}

func getUnhealthyHost(ctx context.Context, m3Machine *capm3.Metal3Machine, cl client.Client,
	rLog logr.Logger,
) (*bmh.BareMetalHost, error) {
	annotations := m3Machine.ObjectMeta.GetAnnotations()
	if annotations == nil {
		return nil, nil
	}
	hostKey, ok := annotations[HostAnnotation]
	if !ok {
		return nil, nil
	}
	hostNamespace, hostName, err := cache.SplitMetaNamespaceKey(hostKey)
	if err != nil {
		rLog.Error(err, "Error parsing annotation value", "annotation key", hostKey)
		return nil, err
	}

	host := bmh.BareMetalHost{}
	key := client.ObjectKey{
		Name:      hostName,
		Namespace: hostNamespace,
	}
	err = cl.Get(ctx, key, &host)
	if apierrors.IsNotFound(err) {
		rLog.Info("Annotated host not found", "host", hostKey)
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &host, nil
}

// hasRebootAnnotation checks for existence of reboot annotations and returns true if at least one exist
func (r *RemediationManager) HasRebootAnnotation(host *bmh.BareMetalHost) bool {
	for annotation := range host.Annotations {
		if isRebootAnnotation(annotation) {
			return true
		}
	}
	return false
}

// isRebootAnnotation returns true if the provided annotation is a reboot annotation (either suffixed or not)
func isRebootAnnotation(annotation string) bool {
	return strings.HasPrefix(annotation, rebootAnnotation+"/") || annotation == rebootAnnotation
}

// onlineStatus checks if hosts Online field in spec is set to false
func (r *RemediationManager) OnlineStatus(host *bmh.BareMetalHost) bool {
	return host.Spec.Online
}

// getRemediationType reutrn type of remediation strategy
func (r *RemediationManager) GetRemediationType() capm3.RemediationType {
	if r.Metal3Remediation.Spec.Strategy.Type != "" {
		return r.Metal3Remediation.Spec.Strategy.Type
	}
	return ""
}

// retryLimitIsSet returns true if retryLimit is set, false if not
func (r *RemediationManager) RetryLimitIsSet() bool {
	return r.Metal3Remediation.Spec.Strategy.RetryLimit > 0
}

// HasReachRetryLimit returns true if retryLimit is reached
func (r *RemediationManager) HasReachRetryLimit() bool {
	return r.Metal3Remediation.Spec.Strategy.RetryLimit == r.Metal3Remediation.Status.RetryCount
}

// SetRemediationPhase sets the state of the remediation
func (r *RemediationManager) SetRemediationPhase(phase string) {
	r.Log.Info("Switching remediation phase", "remediationPhase", phase)
	r.Metal3Remediation.Status.Phase = phase
}

// GetRemediationPhase returns current status of the remediation
func (r *RemediationManager) GetRemediationPhase() string {
	return r.Metal3Remediation.Status.Phase
}

// GetLastRemediatedTime returns last remediation time
func (r *RemediationManager) GetLastRemediatedTime() *metav1.Time {
	return r.Metal3Remediation.Status.LastRemediated
}

// SetLastRemediationTime sets the last remediation timestamp on Status
func (r *RemediationManager) SetLastRemediationTime(remediationTime *metav1.Time) {
	r.Log.Info("Last remediation time", "remediationTime", remediationTime)
	r.Metal3Remediation.Status.LastRemediated = remediationTime
}

// GetTimeout returns timeout duration from remediation request Spec
func (r *RemediationManager) GetTimeout() *metav1.Duration {
	return r.Metal3Remediation.Spec.Strategy.Timeout
}

// IncreaseRetryCount increases the retry count on Status
func (r *RemediationManager) IncreaseRetryCount() {
	r.Metal3Remediation.Status.RetryCount++
}

func (r *RemediationManager) DeleteCapiMachine(ctx context.Context) error {
	machine, err := r.getCapiMachineRef(ctx)

	if err != nil {
		r.Log.Info("Unable to retrive the CAPI machine")
		return err
	}

	if machine.GetDeletionTimestamp() == nil {
		// Issue a delete for remediation request.
		if err := r.Client.Delete(ctx, machine); err != nil && !apierrors.IsNotFound(err) {
			r.Log.Error(err, "failed to delete %v %q for Machine %q", machine.GroupVersionKind(), machine.GetName(), r.Machine.Name)
			return err
		}
	}
	return nil
}

// getExternalRemediationRequest gets reference to External Remediation Request, unstructured object.
func (r *RemediationManager) getCapiMachineRef(ctx context.Context) (*unstructured.Unstructured, error) {
	machine := new(unstructured.Unstructured)
	machine.SetAPIVersion(r.Machine.APIVersion)
	machine.SetKind(r.Machine.Kind)
	machine.SetName(r.Machine.Name)
	key := client.ObjectKey{Name: machine.GetName(), Namespace: r.Machine.Namespace}

	if err := r.Client.Get(ctx, key, machine); err != nil {
		return nil, err
	}
	return machine, nil
}
