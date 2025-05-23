/*
Copyright 2024 The Kubernetes Authors.
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

package v1beta1

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupWebhookWithManager sets up and registers the webhook with the manager.
// Deprecated: This method is going to be removed in a next release.
func (webhook *Metal3ClusterTemplate) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(webhook).
		WithDefaulter(webhook, admission.DefaulterRemoveUnknownOrOmitableFields).
		WithValidator(webhook).
		Complete()
}

var _ webhook.CustomDefaulter = &Metal3ClusterTemplate{}
var _ webhook.CustomDefaulter = &Metal3ClusterTemplate{}

// Deprecated: This method is going to be removed in a next release.
func (webhook *Metal3ClusterTemplate) Default(_ context.Context, _ runtime.Object) error {
	return nil
}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type.
// Deprecated: This method is going to be removed in a next release.
func (webhook *Metal3ClusterTemplate) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	c, ok := obj.(*Metal3ClusterTemplate)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Metal3ClusterTemplate but got a %T", obj))
	}
	return nil, webhook.validate(c)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type.
// Deprecated: This method is going to be removed in a next release.
func (webhook *Metal3ClusterTemplate) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldM3ct, ok := oldObj.(*Metal3ClusterTemplate)
	if !ok || oldM3ct == nil {
		return nil, apierrors.NewInternalError(errors.New("unable to convert existing object"))
	}

	if err := oldM3ct.Spec.Template.Spec.IsValid(); err != nil {
		return nil, err
	}

	newM3ct, ok := newObj.(*Metal3ClusterTemplate)
	if !ok || newM3ct == nil {
		return nil, apierrors.NewInternalError(errors.New("unable to convert new object"))
	}

	if err := newM3ct.Spec.Template.Spec.IsValid(); err != nil {
		return nil, err
	}

	return nil, webhook.validate(newM3ct)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type.
// Deprecated: This method is going to be removed in a next release.
func (webhook *Metal3ClusterTemplate) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// Deprecated: This method is going to be removed in a next release.
func (webhook *Metal3ClusterTemplate) validate(newM3C *Metal3ClusterTemplate) error {
	return newM3C.Spec.Template.Spec.IsValid()
}
