/*
Copyright 2019 The Kubernetes Authors.
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
	"fmt"

	"github.com/go-logr/logr"
	capm3 "github.com/metal3-io/cluster-api-provider-metal3/api/v1alpha4"
	capi "sigs.k8s.io/cluster-api/api/v1alpha3"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TemplateManagerInterface is an interface for a TemplateManager
type TemplateManagerInterface interface {
	FindMachine(context.Context) ([]*capm3.Metal3Machine, error)
	SyncDisableAutomatedClean(m3machines []*capm3.Metal3Machine) error
}

// MachineTemplateManager is responsible for performing metal3MachineTemplate reconciliation
type MachineTemplateManager struct {
	client client.Client

	Metal3Machine         *capm3.Metal3Machine
	Metal3MachineList     *capm3.Metal3MachineList
	Metal3MachineTemplate *capm3.Metal3MachineTemplate
	Log                   logr.Logger
}

// NewMachineTemplateManager returns a new helper for managing a metal3MachineTemplate
func NewMachineTemplateManager(client client.Client,
	metal3MachineList *capm3.Metal3MachineList,
	metal3Machine *capm3.Metal3Machine,
	metal3MachineTemplate *capm3.Metal3MachineTemplate,
	metal3MachineTemplateLog logr.Logger) (*MachineTemplateManager, error) {

	return &MachineTemplateManager{
		client: client,

		Metal3MachineTemplate: metal3MachineTemplate,
		Metal3MachineList:     metal3MachineList,
		Metal3Machine:         metal3Machine,
		Log:                   metal3MachineTemplateLog,
	}, nil
}

// FindMachine iterates through metal3Machines and returns a list of metal3machines referencing
// a metal3MachineTemplate object that they are cloned from.
func (m *MachineTemplateManager) FindMachine(ctx context.Context) ([]*capm3.Metal3Machine, error) {
	m.Log.Info("Fetching Metal3Machine objects")

	// get list of Metal3Machine objects
	m3ms := capm3.Metal3MachineList{}
	// without this ListOption, all namespaces would be including in the listing
	opts := &client.ListOptions{
		Namespace: m.Metal3MachineTemplate.Namespace,
	}

	err := m.client.List(ctx, &m3ms, opts)
	if err != nil {
		return nil, err
	}

	matchedM3Machines := []*capm3.Metal3Machine{}

	// Iterate over the metal3Machine objects to find those
	// cloned from the same metal3MachineTemplate object.
	for i, m3m := range m3ms.Items {
		clonedFromName := m3m.GetAnnotations()[capi.TemplateClonedFromNameAnnotation]
		clonedFromGroupKind := m3m.GetAnnotations()[capi.TemplateClonedFromGroupKindAnnotation]

		if clonedFromName == m.Metal3MachineTemplate.Name && clonedFromGroupKind == m.Metal3MachineTemplate.GroupVersionKind().Group {
			m.Log.Info("Metal3Machine clonedFromName annotation value matched Metal3MachineTemplate name", "metal3Machine", m3m.Name)
			matchedM3Machines = append(matchedM3Machines, &m3ms.Items[i])
		}
	}
	m.Log.Info(fmt.Sprintf("%d metal3Machines found with clonedFromName annoatation matching the name of metal3MachineTemplate", len(matchedM3Machines)))
	if len(matchedM3Machines) == 0 {
		return nil, nil
	}
	return matchedM3Machines, nil
}

// SyncDisableAutomatedClean synchronizes DisableAutomatedClean of metal3MachineTemplate
// with DisableAutomatedClean on the metal3Machine
func (m *MachineTemplateManager) SyncDisableAutomatedClean(m3machines []*capm3.Metal3Machine) error {
	for _, m3m := range m3machines {
		m3m.Spec.DisableAutomatedClean = m.Metal3MachineTemplate.Spec.Template.Spec.DisableAutomatedClean
	}
	return nil
}
