/*
Copyright 2021 The Kubernetes Authors.

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
	"net"
	"strconv"
	"strings"

	"github.com/go-logr/logr"

	bmov1alpha1 "github.com/metal3-io/baremetal-operator/apis/metal3.io/v1alpha1"
	infrav1 "github.com/metal3-io/cluster-api-provider-metal3/api/v1beta1"
	ipamv1 "github.com/metal3-io/ip-address-manager/api/v1alpha1"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	m3machine   = "metal3machine"
	host        = "baremetalhost"
	capimachine = "machine"
)

var BMHNameBasedPreallocation = false

// DataManagerInterface is an interface for a DataManager.
type DataManagerInterface interface {
	SetFinalizer()
	UnsetFinalizer()
	Reconcile(ctx context.Context) error
	ReleaseLeases(ctx context.Context) error
}

// DataManager is responsible for performing machine reconciliation.
type DataManager struct {
	client client.Client
	Data   *infrav1.Metal3Data
	Log    logr.Logger
}

// NewDataManager returns a new helper for managing a Metal3Data object.
func NewDataManager(client client.Client,
	data *infrav1.Metal3Data, dataLog logr.Logger) (*DataManager, error) {
	return &DataManager{
		client: client,
		Data:   data,
		Log:    dataLog,
	}, nil
}

// SetFinalizer sets finalizer.
func (m *DataManager) SetFinalizer() {
	// If the Metal3Data doesn't have finalizer, add it.
	if !Contains(m.Data.Finalizers, infrav1.DataFinalizer) {
		m.Data.Finalizers = append(m.Data.Finalizers,
			infrav1.DataFinalizer,
		)
	}
}

// UnsetFinalizer unsets finalizer.
func (m *DataManager) UnsetFinalizer() {
	// Remove the finalizer.
	m.Data.Finalizers = Filter(m.Data.Finalizers,
		infrav1.DataFinalizer,
	)
}

func createM3IPClaim(name, namespace, poolName string, baremetalhost *bmov1alpha1.BareMetalHost, data *infrav1.Metal3Data) *ipamv1.IPClaim {
	var ObjMeta *metav1.ObjectMeta
	var claimSpec *ipamv1.IPClaimSpec
	if BMHNameBasedPreallocation {
		ObjMeta = &metav1.ObjectMeta{
			Name:      name + "-" + poolName,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Controller: pointer.BoolPtr(true),
					APIVersion: baremetalhost.APIVersion,
					Kind:       baremetalhost.Kind,
					Name:       baremetalhost.Name,
					UID:        baremetalhost.UID,
				},
			},
			Labels: baremetalhost.Labels,
		}
		claimSpec = &ipamv1.IPClaimSpec{
			Pool: corev1.ObjectReference{
				Name:      poolName,
				Namespace: baremetalhost.Namespace,
			},
		}
	} else {
		ObjMeta = &metav1.ObjectMeta{
			Name:      name + "-" + poolName,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Controller: pointer.BoolPtr(true),
					APIVersion: data.APIVersion,
					Kind:       data.Kind,
					Name:       data.Name,
					UID:        data.UID,
				},
			},
			Labels: data.Labels,
		}
		claimSpec = &ipamv1.IPClaimSpec{
			Pool: corev1.ObjectReference{
				Name:      poolName,
				Namespace: data.Namespace,
			},
		}
	}
	return &ipamv1.IPClaim{
		ObjectMeta: *ObjMeta,
		Spec:       *claimSpec,
	}
}

// clearError clears error message from Metal3Data status.
func (m *DataManager) clearError(ctx context.Context) {
	m.Data.Status.ErrorMessage = nil
}

// setError sets error message to Metal3Data status.
func (m *DataManager) setError(ctx context.Context, msg string) {
	m.Data.Status.ErrorMessage = &msg
}

// Reconcile handles Metal3Data events.
func (m *DataManager) Reconcile(ctx context.Context) error {
	m.clearError(ctx)

	if err := m.createSecrets(ctx); err != nil {
		if ok := errors.As(err, &hasRequeueAfterError); ok {
			return err
		}
		m.setError(ctx, errors.Cause(err).Error())
		return err
	}

	return nil
}

// CreateSecrets creates the secret if they do not exist.
func (m *DataManager) createSecrets(ctx context.Context) error {
	var metaDataErr, networkDataErr error

	if m.Data.Spec.Template.Name == "" {
		return nil
	}
	if m.Data.Spec.Template.Namespace == "" {
		m.Data.Spec.Template.Namespace = m.Data.Namespace
	}
	// Fetch the Metal3DataTemplate object to get the templates
	m3dt, err := fetchM3DataTemplate(ctx, &m.Data.Spec.Template, m.client,
		m.Log, m.Data.Labels[clusterv1.ClusterLabelName],
	)
	if err != nil {
		return err
	}
	if m3dt == nil {
		return nil
	}
	m.Log.Info("Fetched Metal3DataTemplate")

	// Fetch the Metal3Machine, to get the related info
	m3m, err := m.getM3Machine(ctx, m3dt)
	if err != nil {
		return err
	}
	if m3m == nil {
		return errors.New("Metal3Machine associated with Metal3DataTemplate is not found")
	}
	m.Log.Info("Fetched Metal3Machine")

	// If the MetaData is given as part of Metal3DataTemplate
	if m3dt.Spec.MetaData != nil {
		m.Log.Info("Metadata is part of Metal3DataTemplate")
		// If the secret name is unset, set it
		if m.Data.Spec.MetaData == nil || m.Data.Spec.MetaData.Name == "" {
			m.Data.Spec.MetaData = &corev1.SecretReference{
				Name:      m3m.Name + "-metadata",
				Namespace: m.Data.Namespace,
			}
		}

		// Try to fetch the secret. If it exists, we do not modify it, to be able
		// to reprovision a node in the exact same state.
		m.Log.Info("Checking if secret exists", "secret", m.Data.Spec.MetaData.Name)
		_, metaDataErr = checkSecretExists(ctx, m.client, m.Data.Spec.MetaData.Name,
			m.Data.Namespace,
		)

		if metaDataErr != nil && !apierrors.IsNotFound(metaDataErr) {
			return metaDataErr
		}
		if apierrors.IsNotFound(metaDataErr) {
			m.Log.Info("MetaData secret creation needed", "secret", m.Data.Spec.MetaData.Name)
		}
	}

	// If the NetworkData is given as part of Metal3DataTemplate
	if m3dt.Spec.NetworkData != nil {
		m.Log.Info("NetworkData is part of Metal3DataTemplate")
		// If the secret name is unset, set it
		if m.Data.Spec.NetworkData == nil || m.Data.Spec.NetworkData.Name == "" {
			m.Data.Spec.NetworkData = &corev1.SecretReference{
				Name:      m3m.Name + "-networkdata",
				Namespace: m.Data.Namespace,
			}
		}

		// Try to fetch the secret. If it exists, we do not modify it, to be able
		// to reprovision a node in the exact same state.
		m.Log.Info("Checking if secret exists", "secret", m.Data.Spec.NetworkData.Name)
		_, networkDataErr = checkSecretExists(ctx, m.client, m.Data.Spec.NetworkData.Name,
			m.Data.Namespace,
		)
		if networkDataErr != nil && !apierrors.IsNotFound(networkDataErr) {
			return networkDataErr
		}
		if apierrors.IsNotFound(networkDataErr) {
			m.Log.Info("NetworkData secret creation needed", "secret", m.Data.Spec.NetworkData.Name)
		}
	}

	// No secret needs creation
	if metaDataErr == nil && networkDataErr == nil {
		m.Log.Info("Metal3Data Reconciled")
		m.Data.Status.Ready = true
		return nil
	}

	// Fetch the Machine.
	capiMachine, err := util.GetOwnerMachine(ctx, m.client, m3m.ObjectMeta)

	if err != nil {
		return errors.Wrapf(err, "Metal3Machine's owner Machine could not be retrieved")
	}
	if capiMachine == nil {
		m.Log.Info("Waiting for Machine Controller to set OwnerRef on Metal3Machine")
		return &RequeueAfterError{RequeueAfter: requeueAfter}
	}
	m.Log.Info("Fetched Machine")

	// Fetch the BMH associated with the M3M
	bmh, err := getHost(ctx, m3m, m.client, m.Log)
	if err != nil {
		return err
	}
	if bmh == nil {
		return &RequeueAfterError{RequeueAfter: requeueAfter}
	}
	m.Log.Info("Fetched BMH")

	// Fetch all the Metal3IPPools and set the OwnerReference. Check if the
	// IP address has been allocated, if so, fetch the address, gateway and prefix.
	poolAddresses, err := m.getAddressesFromPool(ctx, *m3dt)
	if err != nil {
		return err
	}
	var ownerRefs []metav1.OwnerReference
	if BMHNameBasedPreallocation {
		// Create the owner Ref for the secret using BMH name
		ownerRefs = []metav1.OwnerReference{
			{
				APIVersion: bmh.APIVersion,
				Kind:       bmh.Kind,
				Name:       bmh.Name,
				UID:        bmh.UID,
			},
		}
	} else {
		// Create the owner Ref for the secret using Data name
		ownerRefs = []metav1.OwnerReference{
			{
				Controller: pointer.BoolPtr(true),
				APIVersion: m.Data.APIVersion,
				Kind:       m.Data.Kind,
				Name:       m.Data.Name,
				UID:        m.Data.UID,
			},
		}
	}

	// The MetaData secret must be created
	if apierrors.IsNotFound(metaDataErr) {
		m.Log.Info("Creating Metadata secret")
		metadata, err := renderMetaData(m.Data, m3dt, m3m, capiMachine, bmh, poolAddresses)
		if err != nil {
			return err
		}
		if err := createSecret(ctx, m.client, m.Data.Spec.MetaData.Name,
			m.Data.Namespace, m3dt.Labels[clusterv1.ClusterLabelName],
			ownerRefs, map[string][]byte{"metaData": metadata},
		); err != nil {
			return err
		}
	}

	// The NetworkData secret must be created
	if apierrors.IsNotFound(networkDataErr) {
		m.Log.Info("Creating Networkdata secret")
		networkData, err := renderNetworkData(m.Data, m3dt, bmh, poolAddresses)
		if err != nil {
			return err
		}
		if err := createSecret(ctx, m.client, m.Data.Spec.NetworkData.Name,
			m.Data.Namespace, m3dt.Labels[clusterv1.ClusterLabelName],
			ownerRefs, map[string][]byte{"networkData": networkData},
		); err != nil {
			return err
		}
	}

	m.Log.Info("Metal3Data reconciled")
	m.Data.Status.Ready = true
	return nil
}

// ReleaseLeases releases addresses from pool.
func (m *DataManager) ReleaseLeases(ctx context.Context) error {
	if m.Data.Spec.Template.Name == "" {
		return nil
	}
	if m.Data.Spec.Template.Namespace == "" {
		m.Data.Spec.Template.Namespace = m.Data.Namespace
	}
	// Fetch the Metal3DataTemplate object to get the templates
	m3dt, err := fetchM3DataTemplate(ctx, &m.Data.Spec.Template, m.client,
		m.Log, m.Data.Labels[clusterv1.ClusterLabelName],
	)
	if err != nil {
		return err
	}
	if m3dt == nil {
		return nil
	}
	m.Log.Info("Fetched Metal3DataTemplate")

	return m.releaseAddressesFromPool(ctx, *m3dt)
}

// addressFromPool contains the elements coming from an IPPool.
type addressFromPool struct {
	address    ipamv1.IPAddressStr
	prefix     int
	gateway    ipamv1.IPAddressStr
	dnsServers []ipamv1.IPAddressStr
}

// getAddressesFromPool will fetch each Metal3IPPool referenced at least once,
// set the Ownerreference if not set, and check if the Metal3IPAddress has been
// allocated. If not, it will requeue once all IPPools were fetched. If all have
// been allocated it will return a map containing the IPPool name and the address,
// prefix and gateway from that IPPool.
func (m *DataManager) getAddressesFromPool(ctx context.Context,
	m3dt infrav1.Metal3DataTemplate,
) (map[string]addressFromPool, error) {
	var err error
	requeue := false
	itemRequeue := false
	addresses := make(map[string]addressFromPool)
	if m3dt.Spec.MetaData != nil {
		for _, pool := range m3dt.Spec.MetaData.IPAddressesFromPool {
			m.Log.Info("Fetch IPAddresses from IPPool", "Pool Name", pool.Name)
			m.Log.Info("IP Addresses", "IP Address", pool.Key)
			addresses, itemRequeue, err = m.getAddressFromPool(ctx, pool.Name, addresses)
			requeue = requeue || itemRequeue
			if err != nil {
				return addresses, err
			}
		}
		for _, pool := range m3dt.Spec.MetaData.PrefixesFromPool {
			m.Log.Info("Fetch Prefixes from IPPool ", "Pool Name", pool.Name)
			m.Log.Info("Prefixes", "Prefix", pool.Key)
			addresses, itemRequeue, err = m.getAddressFromPool(ctx, pool.Name, addresses)
			requeue = requeue || itemRequeue
			if err != nil {
				return addresses, err
			}
		}
		for _, pool := range m3dt.Spec.MetaData.GatewaysFromPool {
			m.Log.Info("Fetch Gateways from IPPool ", "Pool Name", pool.Name)
			m.Log.Info("Gateways", "Gateway", pool.Key)
			addresses, itemRequeue, err = m.getAddressFromPool(ctx, pool.Name, addresses)
			requeue = requeue || itemRequeue
			if err != nil {
				return addresses, err
			}
		}
		for _, pool := range m3dt.Spec.MetaData.DNSServersFromPool {
			m.Log.Info("Fetch DNSServers from IPPool ", "Pool Name", pool.Name)
			m.Log.Info("DNSServers ", "DNSServer", pool.Key)
			addresses, itemRequeue, err = m.getAddressFromPool(ctx, pool.Name, addresses)
			requeue = requeue || itemRequeue
			if err != nil {
				return addresses, err
			}
		}
	}
	if m3dt.Spec.NetworkData != nil {
		for _, network := range m3dt.Spec.NetworkData.Networks.IPv4 {
			m.Log.Info("Fetch network data from IPPool for IPv4", "Pool Name", network.IPAddressFromIPPool)
			addresses, itemRequeue, err = m.getAddressFromPool(ctx, network.IPAddressFromIPPool, addresses)
			requeue = requeue || itemRequeue
			if err != nil {
				return addresses, err
			}
			// network.IPAddressFromIPPool
			for _, route := range network.Routes {
				if route.Gateway.FromIPPool != nil {
					m.Log.Info("Fetch Route Gateway from IPPool for IPv4", "Pool Name", route.Gateway.FromIPPool)
					addresses, itemRequeue, err = m.getAddressFromPool(ctx, *route.Gateway.FromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return addresses, err
					}
				}
				if route.Services.DNSFromIPPool != nil {
					m.Log.Info("Fetch DNS from IPPool for IPv4", "Pool Name", route.Services.DNSFromIPPool)
					m.Log.Info("DNS Entries", "DNS", route.Services.DNS)
					addresses, itemRequeue, err = m.getAddressFromPool(ctx, *route.Services.DNSFromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return addresses, err
					}
				}
			}
		}

		for _, network := range m3dt.Spec.NetworkData.Networks.IPv6 {
			m.Log.Info("Fetch network data from IPPool for IPv6", "IPPool", network.IPAddressFromIPPool)
			addresses, itemRequeue, err = m.getAddressFromPool(ctx, network.IPAddressFromIPPool, addresses)
			requeue = requeue || itemRequeue
			if err != nil {
				return addresses, err
			}
			for _, route := range network.Routes {
				if route.Gateway.FromIPPool != nil {
					m.Log.Info("Fetch Route Gateway from IPPool for IPv6", "Pool Name", route.Gateway.FromIPPool)
					addresses, itemRequeue, err = m.getAddressFromPool(ctx, *route.Gateway.FromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return addresses, err
					}
				}
				if route.Services.DNSFromIPPool != nil {
					m.Log.Info("Fetch DNS from IPPool for IPv6", "Pool Name", route.Services.DNSFromIPPool)
					m.Log.Info("DNS Entries", "DNS", route.Services.DNS)
					addresses, itemRequeue, err = m.getAddressFromPool(ctx, *route.Services.DNSFromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return addresses, err
					}
				}
			}
		}

		for _, network := range m3dt.Spec.NetworkData.Networks.IPv4DHCP {
			for _, route := range network.Routes {
				if route.Gateway.FromIPPool != nil {
					m.Log.Info("Fetch Route Gateway from IPPool for IPv4DHCP", "Pool Name", route.Gateway.FromIPPool)
					addresses, itemRequeue, err = m.getAddressFromPool(ctx, *route.Gateway.FromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return addresses, err
					}
				}
				if route.Services.DNSFromIPPool != nil {
					m.Log.Info("Fetch DNS from IPPool for IPv4DHCP", "Pool Name", route.Services.DNSFromIPPool)
					m.Log.Info("DNS Entries", "DNS", route.Services.DNS)
					addresses, itemRequeue, err = m.getAddressFromPool(ctx, *route.Services.DNSFromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return addresses, err
					}
				}
			}
		}

		for _, network := range m3dt.Spec.NetworkData.Networks.IPv6DHCP {
			for _, route := range network.Routes {
				if route.Gateway.FromIPPool != nil {
					m.Log.Info("Fetch Network Gateway from IPPool for IPv6DHCP", "Pool Name", route.Gateway.FromIPPool)
					addresses, itemRequeue, err = m.getAddressFromPool(ctx, *route.Gateway.FromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return addresses, err
					}
				}
				if route.Services.DNSFromIPPool != nil {
					m.Log.Info("Fetch DNS from IPPool for IPv6DHCP", "Pool Name", route.Services.DNSFromIPPool)
					m.Log.Info("DNS Entries", "DNS", route.Services.DNS)
					addresses, itemRequeue, err = m.getAddressFromPool(ctx, *route.Services.DNSFromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return addresses, err
					}
				}
			}
		}

		for _, network := range m3dt.Spec.NetworkData.Networks.IPv6SLAAC {
			for _, route := range network.Routes {
				if route.Gateway.FromIPPool != nil {
					m.Log.Info("Fetch Gateway from IPPool for IPv6SLAAC", "Pool Name", route.Gateway.FromIPPool)
					addresses, itemRequeue, err = m.getAddressFromPool(ctx, *route.Gateway.FromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return addresses, err
					}
				}
				if route.Services.DNSFromIPPool != nil {
					m.Log.Info("Fetch DNS from IPPool for IPv6SAAC", "Pool Name", route.Services.DNSFromIPPool)
					m.Log.Info("DNS Entries", "DNS", route.Services.DNS)
					addresses, itemRequeue, err = m.getAddressFromPool(ctx, *route.Services.DNSFromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return addresses, err
					}
				}
			}
		}
		if m3dt.Spec.NetworkData.Services.DNSFromIPPool != nil {
			m.Log.Info("Fetch DNS from IPPool")
			m.Log.Info("DNS Entries", "DNS", m3dt.Spec.NetworkData.Services.DNS)
			addresses, itemRequeue, err = m.getAddressFromPool(ctx, *m3dt.Spec.NetworkData.Services.DNSFromIPPool, addresses)
			requeue = requeue || itemRequeue
			if err != nil {
				return addresses, err
			}
		}
	}
	if requeue {
		return addresses, &RequeueAfterError{RequeueAfter: requeueAfter}
	}
	return addresses, nil
}

// releaseAddressesFromPool removes the OwnerReference on the IPPool objects.
func (m *DataManager) releaseAddressesFromPool(ctx context.Context,
	m3dt infrav1.Metal3DataTemplate,
) error {
	var err error
	requeue := false
	itemRequeue := false
	addresses := make(map[string]bool)
	if m3dt.Spec.MetaData != nil {
		for _, pool := range m3dt.Spec.MetaData.IPAddressesFromPool {
			addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, pool.Name, addresses)
			requeue = requeue || itemRequeue
			fmt.Println(requeue)
			if err != nil {
				return err
			}
		}
		for _, pool := range m3dt.Spec.MetaData.PrefixesFromPool {
			addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, pool.Name, addresses)
			requeue = requeue || itemRequeue
			if err != nil {
				return err
			}
		}
		for _, pool := range m3dt.Spec.MetaData.GatewaysFromPool {
			addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, pool.Name, addresses)
			requeue = requeue || itemRequeue
			if err != nil {
				return err
			}
		}
		for _, pool := range m3dt.Spec.MetaData.DNSServersFromPool {
			addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, pool.Name, addresses)
			requeue = requeue || itemRequeue
			if err != nil {
				return err
			}
		}
	}
	if m3dt.Spec.NetworkData != nil {
		for _, network := range m3dt.Spec.NetworkData.Networks.IPv4 {
			addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, network.IPAddressFromIPPool, addresses)
			requeue = requeue || itemRequeue
			if err != nil {
				return err
			}
			// network.IPAddressFromIPPool
			for _, route := range network.Routes {
				if route.Gateway.FromIPPool != nil {
					addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, *route.Gateway.FromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return err
					}
				}
				if route.Services.DNSFromIPPool != nil {
					addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, *route.Services.DNSFromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return err
					}
				}
			}
		}

		for _, network := range m3dt.Spec.NetworkData.Networks.IPv6 {
			addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, network.IPAddressFromIPPool, addresses)
			requeue = requeue || itemRequeue
			if err != nil {
				return err
			}
			for _, route := range network.Routes {
				if route.Gateway.FromIPPool != nil {
					addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, *route.Gateway.FromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return err
					}
				}
				if route.Services.DNSFromIPPool != nil {
					addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, *route.Services.DNSFromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return err
					}
				}
			}
		}

		for _, network := range m3dt.Spec.NetworkData.Networks.IPv4DHCP {
			for _, route := range network.Routes {
				if route.Gateway.FromIPPool != nil {
					addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, *route.Gateway.FromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return err
					}
				}
				if route.Services.DNSFromIPPool != nil {
					addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, *route.Services.DNSFromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return err
					}
				}
			}
		}

		for _, network := range m3dt.Spec.NetworkData.Networks.IPv6DHCP {
			for _, route := range network.Routes {
				if route.Gateway.FromIPPool != nil {
					addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, *route.Gateway.FromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return err
					}
				}
				if route.Services.DNSFromIPPool != nil {
					addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, *route.Services.DNSFromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return err
					}
				}
			}
		}

		for _, network := range m3dt.Spec.NetworkData.Networks.IPv6SLAAC {
			for _, route := range network.Routes {
				if route.Gateway.FromIPPool != nil {
					addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, *route.Gateway.FromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return err
					}
				}
				if route.Services.DNSFromIPPool != nil {
					addresses, itemRequeue, err = m.releaseAddressFromPool(ctx, *route.Services.DNSFromIPPool, addresses)
					requeue = requeue || itemRequeue
					if err != nil {
						return err
					}
				}
			}
		}
		if m3dt.Spec.NetworkData.Services.DNSFromIPPool != nil {
			_, itemRequeue, err = m.releaseAddressFromPool(ctx, *m3dt.Spec.NetworkData.Services.DNSFromIPPool, addresses)
			requeue = requeue || itemRequeue
			if err != nil {
				return err
			}
		}
	}
	if requeue {
		return &RequeueAfterError{RequeueAfter: requeueAfter}
	}
	return nil
}

// getAddressFromPool adds an ownerReference on the referenced Metal3IPPool
// objects. It then tries to fetch the Metal3IPAddress if it exists, and asks
// for requeue if not ready.
func (m *DataManager) getAddressFromPool(ctx context.Context, poolName string,
	addresses map[string]addressFromPool,
) (map[string]addressFromPool, bool, error) {
	if addresses == nil {
		addresses = make(map[string]addressFromPool)
	}
	if entry, ok := addresses[poolName]; ok {
		if entry.address != "" {
			return addresses, false, nil
		}
	}
	addresses[poolName] = addressFromPool{}
	fmt.Println("===inside getAddressFromPool ===")
	if m.Data.Spec.Template.Name == "" {
		return addresses, false, nil
	}
	if m.Data.Spec.Template.Namespace == "" {
		m.Data.Spec.Template.Namespace = m.Data.Namespace
	}
	fmt.Println("===inside getAddressFromPool FETCHING DataTemplate ===")
	// Fetch the Metal3DataTemplate object to get the templates
	m3dt, err := fetchM3DataTemplate(ctx, &m.Data.Spec.Template, m.client,
		m.Log, m.Data.Labels[clusterv1.ClusterLabelName],
	)
	if err != nil {
		fmt.Println("===inside getAddressFromPool, err is not nil after fetchM3DataTemplate ")
		fmt.Println(err)
		return addresses, false, err
	}
	if m3dt == nil {
		fmt.Println("===inside getAddressFromPool, m3dt is nil after fetchM3DataTemplate ")
		return addresses, false, nil
	}
	m.Log.Info("Fetched Metal3DataTemplate")
	fmt.Println(m3dt.Name)

	// Fetch the Metal3Machine, to get the related info
	fmt.Println("===inside getAddressFromPool FETCHING getM3Machine ===")
	m3m, err := m.getM3Machine(ctx, m3dt)
	if err != nil {
		fmt.Println("===inside getAddressFromPool, err is not nil after getM3Machine ")
		fmt.Println(err)
		return nil, false, err
	}
	if m3m == nil {
		fmt.Println("===inside getAddressFromPool, m3m is nil after getM3Machine ")
		return nil, false, err
	}
	m.Log.Info("Fetched Metal3Machine")
	fmt.Println(m3m.Name)

	// Fetch the BMH associated with the M3M
	fmt.Println("===inside getAddressFromPool calling getHost ===")
	bmh, err := getHost(ctx, m3m, m.client, m.Log)
	if err != nil {
		fmt.Println("===inside getAddressFromPool, err is not nil after getHost ")
		fmt.Println(err)
		return nil, false, err
	}
	if bmh == nil {
		fmt.Println("===inside getAddressFromPool, bmh is nil after getHost ")
		return nil, false, &RequeueAfterError{RequeueAfter: requeueAfter}
	}
	m.Log.Info("Fetched BMH")
	fmt.Println(bmh.Name)
	fmt.Println("inside getAddressFromPool WILL TRY FETCHING IPCLAIM WITH BMH NAME FIRST TIME")
	ipClaim, err := fetchM3IPClaim(ctx, m.client, m.Log, bmh.Name+"-"+poolName, bmh.Namespace)
	if err != nil {
		fmt.Println("===inside getAddressFromPool, err is not nil when fetchM3IPClaim with bmh name ===")
		if ok := errors.As(err, &hasRequeueAfterError); !ok {
			fmt.Println("===inside getAddressFromPool, fetchM3IPClaim with bmh name ok is not ok when hasRequeueAfterError ===")
			fmt.Println(addresses, err)
			return addresses, false, err
		}
		fmt.Println("WILL TRY FETCHING IPCLAIM WITH DATA NAME THIS TIME")
		ipClaim, err = fetchM3IPClaim(ctx, m.client, m.Log, m.Data.Name+"-"+poolName, m.Data.Namespace)
		if err != nil {
			fmt.Println("===inside getAddressFromPool, err is not nil when fetchM3IPClaim with data name ===")
			fmt.Println(err)
			if ok := errors.As(err, &hasRequeueAfterError); !ok {
				fmt.Println("===inside getAddressFromPool, fetchM3IPClaim with data name ok is not ok when hasRequeueAfterError ===")
				fmt.Println(addresses, err)
				return addresses, false, err
			}
			fmt.Println("NO IPCLAIMS EXIST, CREATING ONE BASED ON THE BMHNameBasedPreallocation")
			if BMHNameBasedPreallocation {
				fmt.Println("===inside getAddressFromPool, BMHNameBasedPreallocation IS TRUE, creating ipclaim with bmh name ===")
				fmt.Println(bmh.Name+"-"+poolName, bmh.Namespace)
				// Create the claim
				err = createObject(ctx, m.client, createM3IPClaim(bmh.Name, bmh.Namespace, poolName, bmh, m.Data))
				if err != nil {
					fmt.Println("===inside getAddressFromPool, createM3IPClaim with bmh name err is not nil ===")
					fmt.Println(err)
					if ok := errors.As(err, &hasRequeueAfterError); !ok {
						fmt.Println("inside  getAddressFromPool createM3IPClaim with bmh name ok is not ok while hasRequeueAfterError ")
						fmt.Println(addresses, err)
						return addresses, false, err
					}
				}
			} else {
				// Create the claim
				fmt.Println("===inside getAddressFromPool, BMHNameBasedPreallocation IS FALSE, creating ipclaim with data name ===")
				fmt.Println(m.Data.Name+"-"+poolName, m.Data.Namespace)
				err = createObject(ctx, m.client, createM3IPClaim(m.Data.Name, m.Data.Namespace, poolName, bmh, m.Data))
				if err != nil {
					fmt.Println("===inside getAddressFromPool, createM3IPClaim with data name err is not nil ===")
					fmt.Println(err)
					if ok := errors.As(err, &hasRequeueAfterError); !ok {
						fmt.Println("inside  getAddressFromPool createM3IPClaim with data name ok is not ok while hasRequeueAfterError ")
						fmt.Println(addresses, err)
						return addresses, false, err
					}
				}
			}
		}
	}

	if ipClaim.Status.ErrorMessage != nil {
		m.Data.Status.ErrorMessage = pointer.StringPtr(fmt.Sprintf(
			"IP Allocation for %v failed : %v", poolName, ipClaim.Status.ErrorMessage,
		))
		return addresses, false, errors.New(*m.Data.Status.ErrorMessage)
	}

	// verify if allocation is there, if not requeue
	if ipClaim.Status.Address == nil {
		return addresses, true, nil
	}

	// get Metal3IPAddress object
	ipAddress := &ipamv1.IPAddress{}
	var addressNamespacedName *types.NamespacedName
	if BMHNameBasedPreallocation {
		addressNamespacedName = &types.NamespacedName{
			Name:      ipClaim.Status.Address.Name,
			Namespace: bmh.Namespace,
		}
	} else {
		addressNamespacedName = &types.NamespacedName{
			Name:      ipClaim.Status.Address.Name,
			Namespace: m.Data.Namespace,
		}
	}

	if err := m.client.Get(ctx, *addressNamespacedName, ipAddress); err != nil {
		if apierrors.IsNotFound(err) {
			return addresses, true, nil
		}
		return addresses, false, err
	}

	gateway := ipamv1.IPAddressStr("")
	if ipAddress.Spec.Gateway != nil {
		gateway = *ipAddress.Spec.Gateway
	}

	addresses[poolName] = addressFromPool{
		address:    ipAddress.Spec.Address,
		prefix:     ipAddress.Spec.Prefix,
		gateway:    gateway,
		dnsServers: ipAddress.Spec.DNSServers,
	}

	return addresses, false, nil
}

// releaseAddressFromPool removes the owner reference on existing referenced
// Metal3IPPool objects.
func (m *DataManager) releaseAddressFromPool(ctx context.Context, poolName string,
	addresses map[string]bool,
) (map[string]bool, bool, error) {
	if addresses == nil {
		addresses = make(map[string]bool)
	}
	if _, ok := addresses[poolName]; ok {
		return addresses, false, nil
	}
	addresses[poolName] = false

	fmt.Println("===inside releaseAddressFromPool ===")

	if m.Data.Spec.Template.Name == "" {
		return addresses, false, nil
	}
	if m.Data.Spec.Template.Namespace == "" {
		m.Data.Spec.Template.Namespace = m.Data.Namespace
	}
	// Fetch the Metal3DataTemplate object to get the templates
	fmt.Println("===inside releaseAddressFromPool FETCHING DataTemplate ===")
	m3dt, err := fetchM3DataTemplate(ctx, &m.Data.Spec.Template, m.client,
		m.Log, m.Data.Labels[clusterv1.ClusterLabelName],
	)
	if err != nil {
		fmt.Println("===inside releaseAddressFromPool, err is not nil after fetchM3DataTemplate ")
		fmt.Println(err)
		return addresses, false, err
	}
	if m3dt == nil {
		fmt.Println("===inside releaseAddressFromPool, m3dt is nil after fetchM3DataTemplate ")
		return addresses, false, nil
	}
	m.Log.Info("Fetched Metal3DataTemplate")
	fmt.Println(m3dt.Name)

	// Fetch the Metal3Machine, to get the related info
	fmt.Println("===inside releaseAddressFromPool FETCHING m3m ===")
	m3m, err := m.getM3Machine(ctx, m3dt)
	if err != nil {
		fmt.Println("===inside releaseAddressFromPool, err is not nil after getM3Machine ")
		fmt.Println(err)
		return nil, false, err
	}
	if m3m == nil {
		fmt.Println("===inside releaseAddressFromPool, m3m is nil after getM3Machine ")
		return nil, false, err
	}
	m.Log.Info("Fetched Metal3Machine")
	fmt.Println(m3m.Name)

	// Fetch the BMH associated with the M3M
	fmt.Println("===inside releaseAddressFromPool calling getHost ===")
	bmh, err := getHost(ctx, m3m, m.client, m.Log)
	if err != nil {
		fmt.Println("===inside releaseAddressFromPool, err is not nil after getHost ")
		fmt.Println(err)
		return nil, false, err
	}
	if bmh == nil {
		fmt.Println("===inside releaseAddressFromPool, bmh is nil after getHost ")
		return nil, false, &RequeueAfterError{RequeueAfter: requeueAfter}
	}
	m.Log.Info("Fetched BMH")
	fmt.Println(bmh.Name)
	fmt.Println("inside releaseAddressFromPool WILL TRY FETCHING IPCLAIM WITH BMH NAME FIRST TIME")
	ipClaim, err := fetchM3IPClaim(ctx, m.client, m.Log, bmh.Name+"-"+poolName,
		bmh.Namespace,
	)
	if err != nil {
		fmt.Println("===inside releaseAddressFromPool, err is not nil when fetchM3IPClaim with bmh name ===")
		if ok := errors.As(err, &hasRequeueAfterError); !ok {
			fmt.Println("===inside releaseAddressFromPool, fetchM3IPClaim with bmh name ok is not ok when hasRequeueAfterError ===")
			fmt.Println(addresses, err)
			return addresses, false, err
		}
		fmt.Println("===inside releaseAddressFromPool, outside fetchM3IPClaim with bmh name ok is not ok when hasRequeueAfterError ===")
		addresses[poolName] = true
		return addresses, false, nil
	}
	fmt.Println("inside releaseAddressFromPool WILL TRY DELETING IPCLAIM WITH BMH NAME FIRST TIME")
	fmt.Println("!!!!!!!!!!11IPCLAIM TO BE DELETED IS!!!!!!!!!!!!!!!!!!")
	fmt.Println(ipClaim)
	err = deleteObject(ctx, m.client, ipClaim)
	if err != nil {
		fmt.Println("inside releaseAddressFromPool ERR IS NOT NILL WHEN TRY DELETING IPCLAIM WITH BMH NAME FIRST TIME")
		fmt.Println(err)
		return addresses, false, err
	}

	if !BMHNameBasedPreallocation {
		fmt.Println("inside releaseAddressFromPool BMHNameBasedPreallocation is FALSE WILL TRY DELETING IPCLAIM WITH DATA NAME THIS TIME")
		ipClaim, err = fetchM3IPClaim(ctx, m.client, m.Log, m.Data.Name+"-"+poolName, m.Data.Namespace)
		if err != nil {
			fmt.Println("===inside releaseAddressFromPool, err is not nil when fetchM3IPClaim with DATA name ===")
			if ok := errors.As(err, &hasRequeueAfterError); !ok {
				fmt.Println("===inside releaseAddressFromPool, fetchM3IPClaim with DATA name ok is not ok when hasRequeueAfterError ===")
				fmt.Println(addresses, err)
				return addresses, false, err
			}
			fmt.Println("===inside releaseAddressFromPool, outside fetchM3IPClaim with DATA name ok is not ok when hasRequeueAfterError ===")
			addresses[poolName] = true
			return addresses, false, nil
		}
		fmt.Println("inside releaseAddressFromPool WILL TRY DELETING IPCLAIM WITH DATA NAME THIS TIME")
		err = deleteObject(ctx, m.client, ipClaim)
		if err != nil {
			fmt.Println("inside releaseAddressFromPool ERR IS NOT NILL WHEN TRY DELETING IPCLAIM WITH DATA NAME THIS TIME")
			fmt.Println(err)
			return addresses, false, err
		}
	}

	addresses[poolName] = true
	return addresses, false, nil
}

// renderNetworkData renders the networkData into an object that will be
// marshalled into the secret.
func renderNetworkData(m3d *infrav1.Metal3Data, m3dt *infrav1.Metal3DataTemplate,
	bmh *bmov1alpha1.BareMetalHost, poolAddresses map[string]addressFromPool,
) ([]byte, error) {
	if m3dt.Spec.NetworkData == nil {
		return nil, nil
	}
	var err error

	networkData := map[string][]interface{}{}

	networkData["links"], err = renderNetworkLinks(m3dt.Spec.NetworkData.Links, bmh)
	if err != nil {
		return nil, err
	}

	networkData["networks"], err = renderNetworkNetworks(
		m3dt.Spec.NetworkData.Networks, m3d, poolAddresses,
	)
	if err != nil {
		return nil, err
	}

	networkData["services"], err = renderNetworkServices(m3dt.Spec.NetworkData.Services, poolAddresses)
	if err != nil {
		return nil, err
	}

	return yaml.Marshal(networkData)
}

// renderNetworkServices renders the services.
func renderNetworkServices(services infrav1.NetworkDataService, poolAddresses map[string]addressFromPool) ([]interface{}, error) {
	data := []interface{}{}

	for _, service := range services.DNS {
		data = append(data, map[string]interface{}{
			"type":    "dns",
			"address": service,
		})
	}

	if services.DNSFromIPPool != nil {
		poolAddress, ok := poolAddresses[*services.DNSFromIPPool]
		if !ok {
			return nil, errors.New("Pool not found in cache")
		}
		for _, service := range poolAddress.dnsServers {
			data = append(data, map[string]interface{}{
				"type":    "dns",
				"address": service,
			})
		}
	}

	return data, nil
}

// renderNetworkLinks renders the different types of links.
func renderNetworkLinks(networkLinks infrav1.NetworkDataLink, bmh *bmov1alpha1.BareMetalHost) ([]interface{}, error) {
	data := []interface{}{}

	// Ethernet links
	for _, link := range networkLinks.Ethernets {
		macAddress, err := getLinkMacAddress(link.MACAddress, bmh)
		if err != nil {
			return nil, err
		}
		data = append(data, map[string]interface{}{
			"type":                 link.Type,
			"id":                   link.Id,
			"mtu":                  link.MTU,
			"ethernet_mac_address": macAddress,
		})
	}

	// Bond links
	for _, link := range networkLinks.Bonds {
		macAddress, err := getLinkMacAddress(link.MACAddress, bmh)
		if err != nil {
			return nil, err
		}
		data = append(data, map[string]interface{}{
			"type":                 "bond",
			"id":                   link.Id,
			"mtu":                  link.MTU,
			"ethernet_mac_address": macAddress,
			"bond_mode":            link.BondMode,
			"bond_links":           link.BondLinks,
		})
	}

	// Vlan links
	for _, link := range networkLinks.Vlans {
		macAddress, err := getLinkMacAddress(link.MACAddress, bmh)
		if err != nil {
			return nil, err
		}
		data = append(data, map[string]interface{}{
			"type":             "vlan",
			"id":               link.Id,
			"mtu":              link.MTU,
			"vlan_mac_address": macAddress,
			"vlan_id":          link.VlanID,
			"vlan_link":        link.VlanLink,
		})
	}

	return data, nil
}

// renderNetworkNetworks renders the different types of network.
func renderNetworkNetworks(networks infrav1.NetworkDataNetwork,
	m3d *infrav1.Metal3Data, poolAddresses map[string]addressFromPool,
) ([]interface{}, error) {
	data := []interface{}{}

	// IPv4 networks static allocation
	for _, network := range networks.IPv4 {
		poolAddress, ok := poolAddresses[network.IPAddressFromIPPool]
		if !ok {
			return nil, errors.New("Pool not found in cache")
		}
		ip := ipamv1.IPAddressv4Str(poolAddress.address)
		mask := translateMask(poolAddress.prefix, true)
		routes, err := getRoutesv4(network.Routes, poolAddresses)
		if err != nil {
			return nil, err
		}
		data = append(data, map[string]interface{}{
			"type":       "ipv4",
			"id":         network.ID,
			"link":       network.Link,
			"netmask":    mask,
			"ip_address": ip,
			"routes":     routes,
		})
	}

	// IPv6 networks static allocation
	for _, network := range networks.IPv6 {
		poolAddress, ok := poolAddresses[network.IPAddressFromIPPool]
		if !ok {
			return nil, errors.New("Pool not found in cache")
		}
		ip := ipamv1.IPAddressv6Str(poolAddress.address)
		mask := translateMask(poolAddress.prefix, false)
		routes, err := getRoutesv6(network.Routes, poolAddresses)
		if err != nil {
			return nil, err
		}
		data = append(data, map[string]interface{}{
			"type":       "ipv6",
			"id":         network.ID,
			"link":       network.Link,
			"netmask":    mask,
			"ip_address": ip,
			"routes":     routes,
		})
	}

	// IPv4 networks DHCP allocation
	for _, network := range networks.IPv4DHCP {
		routes, err := getRoutesv4(network.Routes, poolAddresses)
		if err != nil {
			return nil, err
		}
		data = append(data, map[string]interface{}{
			"type":   "ipv4_dhcp",
			"id":     network.ID,
			"link":   network.Link,
			"routes": routes,
		})
	}

	// IPv6 networks DHCP allocation
	for _, network := range networks.IPv6DHCP {
		routes, err := getRoutesv6(network.Routes, poolAddresses)
		if err != nil {
			return nil, err
		}
		data = append(data, map[string]interface{}{
			"type":   "ipv6_dhcp",
			"id":     network.ID,
			"link":   network.Link,
			"routes": routes,
		})
	}

	// IPv6 networks SLAAC allocation
	for _, network := range networks.IPv6SLAAC {
		routes, err := getRoutesv6(network.Routes, poolAddresses)
		if err != nil {
			return nil, err
		}
		data = append(data, map[string]interface{}{
			"type":   "ipv6_slaac",
			"id":     network.ID,
			"link":   network.Link,
			"routes": routes,
		})
	}

	return data, nil
}

// getRoutesv4 returns the IPv4 routes.
func getRoutesv4(netRoutes []infrav1.NetworkDataRoutev4,
	poolAddresses map[string]addressFromPool,
) ([]interface{}, error) {
	routes := []interface{}{}
	for _, route := range netRoutes {
		gateway := ipamv1.IPAddressv4Str("")
		if route.Gateway.String != nil {
			gateway = *route.Gateway.String
		} else if route.Gateway.FromIPPool != nil {
			poolAddress, ok := poolAddresses[*route.Gateway.FromIPPool]
			if !ok {
				return []interface{}{}, errors.New("Failed to fetch pool from cache")
			}
			gateway = ipamv1.IPAddressv4Str(poolAddress.gateway)
		}
		services := []interface{}{}
		for _, service := range route.Services.DNS {
			services = append(services, map[string]interface{}{
				"type":    "dns",
				"address": service,
			})
		}
		if route.Services.DNSFromIPPool != nil {
			poolAddress, ok := poolAddresses[*route.Services.DNSFromIPPool]
			if !ok {
				return []interface{}{}, errors.New("Pool not found in cache")
			}
			for _, service := range poolAddress.dnsServers {
				services = append(services, map[string]interface{}{
					"type":    "dns",
					"address": service,
				})
			}
		}
		mask := translateMask(route.Prefix, true)
		routes = append(routes, map[string]interface{}{
			"network":  route.Network,
			"netmask":  mask,
			"gateway":  gateway,
			"services": services,
		})
	}
	return routes, nil
}

// getRoutesv6 returns the IPv6 routes.
func getRoutesv6(netRoutes []infrav1.NetworkDataRoutev6,
	poolAddresses map[string]addressFromPool,
) ([]interface{}, error) {
	routes := []interface{}{}
	for _, route := range netRoutes {
		gateway := ipamv1.IPAddressv6Str("")
		if route.Gateway.String != nil {
			gateway = *route.Gateway.String
		} else if route.Gateway.FromIPPool != nil {
			poolAddress, ok := poolAddresses[*route.Gateway.FromIPPool]
			if !ok {
				return []interface{}{}, errors.New("Failed to fetch pool from cache")
			}
			gateway = ipamv1.IPAddressv6Str(poolAddress.gateway)
		}
		services := []interface{}{}
		for _, service := range route.Services.DNS {
			services = append(services, map[string]interface{}{
				"type":    "dns",
				"address": service,
			})
		}
		if route.Services.DNSFromIPPool != nil {
			poolAddress, ok := poolAddresses[*route.Services.DNSFromIPPool]
			if !ok {
				return []interface{}{}, errors.New("Pool not found in cache")
			}
			for _, service := range poolAddress.dnsServers {
				services = append(services, map[string]interface{}{
					"type":    "dns",
					"address": service,
				})
			}
		}
		mask := translateMask(route.Prefix, false)
		routes = append(routes, map[string]interface{}{
			"network":  route.Network,
			"netmask":  mask,
			"gateway":  gateway,
			"services": services,
		})
	}
	return routes, nil
}

// translateMask transforms a mask given as integer into a dotted-notation string.
func translateMask(maskInt int, ipv4 bool) interface{} {
	if ipv4 {
		// Get the mask by concatenating the IPv4 prefix of net package and the mask
		address := net.IP(append([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255, 255},
			[]byte(net.CIDRMask(maskInt, 32))...,
		)).String()
		return ipamv1.IPAddressv4Str(address)
	}
	// get the mask
	address := net.IP(net.CIDRMask(maskInt, 128)).String()
	return ipamv1.IPAddressv6Str(address)
}

// getLinkMacAddress returns the mac address.
func getLinkMacAddress(mac *infrav1.NetworkLinkEthernetMac, bmh *bmov1alpha1.BareMetalHost) (
	string, error,
) {
	macAddress := ""
	var err error

	// if a string was given
	if mac.String != nil {
		macAddress = *mac.String

		// Otherwise fetch the mac from the interface name
	} else if mac.FromHostInterface != nil {
		macAddress, err = getBMHMacByName(*mac.FromHostInterface, bmh)
	}

	return macAddress, err
}

// renderMetaData renders the MetaData items.
func renderMetaData(m3d *infrav1.Metal3Data, m3dt *infrav1.Metal3DataTemplate,
	m3m *infrav1.Metal3Machine, machine *clusterv1.Machine, bmh *bmov1alpha1.BareMetalHost,
	poolAddresses map[string]addressFromPool,
) ([]byte, error) {
	if m3dt.Spec.MetaData == nil {
		return nil, nil
	}
	metadata := make(map[string]string)

	// Mac addresses
	for _, entry := range m3dt.Spec.MetaData.FromHostInterfaces {
		value, err := getBMHMacByName(entry.Interface, bmh)
		if err != nil {
			return nil, err
		}
		metadata[entry.Key] = value
	}

	// IP addresses
	for _, entry := range m3dt.Spec.MetaData.IPAddressesFromPool {
		poolAddress, ok := poolAddresses[entry.Name]
		if !ok {
			return nil, errors.New("Pool not found in cache")
		}
		metadata[entry.Key] = string(poolAddress.address)
	}

	// Prefixes
	for _, entry := range m3dt.Spec.MetaData.PrefixesFromPool {
		poolAddress, ok := poolAddresses[entry.Name]
		if !ok {
			return nil, errors.New("Pool not found in cache")
		}
		metadata[entry.Key] = strconv.Itoa(poolAddress.prefix)
	}

	// Gateways
	for _, entry := range m3dt.Spec.MetaData.GatewaysFromPool {
		poolAddress, ok := poolAddresses[entry.Name]
		if !ok {
			return nil, errors.New("Pool not found in cache")
		}
		metadata[entry.Key] = string(poolAddress.gateway)
	}

	// Indexes
	for _, entry := range m3dt.Spec.MetaData.Indexes {
		if entry.Step == 0 {
			entry.Step = 1
		}
		metadata[entry.Key] = entry.Prefix + strconv.Itoa(entry.Offset+m3d.Spec.Index*entry.Step) + entry.Suffix
	}

	// Namespaces
	for _, entry := range m3dt.Spec.MetaData.Namespaces {
		metadata[entry.Key] = m3d.Namespace
	}

	// Object names
	for _, entry := range m3dt.Spec.MetaData.ObjectNames {
		switch strings.ToLower(entry.Object) {
		case m3machine:
			metadata[entry.Key] = m3m.Name
		case capimachine:
			metadata[entry.Key] = machine.Name
		case host:
			metadata[entry.Key] = bmh.Name
		default:
			return nil, errors.New("Unknown object type")
		}
	}

	// Labels
	for _, entry := range m3dt.Spec.MetaData.FromLabels {
		switch strings.ToLower(entry.Object) {
		case m3machine:
			metadata[entry.Key] = m3m.Labels[entry.Label]
		case capimachine:
			metadata[entry.Key] = machine.Labels[entry.Label]
		case host:
			metadata[entry.Key] = bmh.Labels[entry.Label]
		default:
			return nil, errors.New("Unknown object type")
		}
	}

	// Annotations
	for _, entry := range m3dt.Spec.MetaData.FromAnnotations {
		switch strings.ToLower(entry.Object) {
		case m3machine:
			metadata[entry.Key] = m3m.Annotations[entry.Annotation]
		case capimachine:
			metadata[entry.Key] = machine.Annotations[entry.Annotation]
		case host:
			metadata[entry.Key] = bmh.Annotations[entry.Annotation]
		default:
			return nil, errors.New("Unknown object type")
		}
	}

	// Strings
	for _, entry := range m3dt.Spec.MetaData.Strings {
		metadata[entry.Key] = entry.Value
	}
	providerid := fmt.Sprintf("%s/%s/%s", m3m.GetNamespace(), bmh.GetName(), m3m.GetName())
	metadata["providerid"] = providerid
	return yaml.Marshal(metadata)
}

// getBMHMacByName returns the mac address of the interface matching the name.
func getBMHMacByName(name string, bmh *bmov1alpha1.BareMetalHost) (string, error) {
	if bmh == nil || bmh.Status.HardwareDetails == nil || bmh.Status.HardwareDetails.NIC == nil {
		return "", errors.New("Nics list not populated")
	}
	for _, nics := range bmh.Status.HardwareDetails.NIC {
		if nics.Name == name {
			return nics.MAC, nil
		}
	}
	return "", fmt.Errorf("nic name not found %v", name)
}

func (m *DataManager) getM3Machine(ctx context.Context, m3dt *infrav1.Metal3DataTemplate) (*infrav1.Metal3Machine, error) {
	if m.Data.Spec.Claim.Name == "" {
		fmt.Println("inside  m.Data.Spec.Claim.Name emptyl ")
		return nil, errors.New("Claim name not set")
	}

	capm3DataClaim := &infrav1.Metal3DataClaim{}
	claimNamespacedName := types.NamespacedName{
		Name:      m.Data.Spec.Claim.Name,
		Namespace: m.Data.Namespace,
	}
	fmt.Println("================inside getM3Machine method, BEFORE TRYING TO GET capm3DataClaim")
	fmt.Println(capm3DataClaim, claimNamespacedName)
	if err := m.client.Get(ctx, claimNamespacedName, capm3DataClaim); err != nil {
		fmt.Println("inside getM3Machine method, GET capm3DataClaim err is not nil, printing error below")
		fmt.Println(err)
		if apierrors.IsNotFound(err) {
			fmt.Println("inside getM3Machine, GET capm3DataClaim IS NOT FOUND ERROR, REQUEUEING")
			return nil, &RequeueAfterError{RequeueAfter: requeueAfter}
		}
		fmt.Println("inside getM3Machine, GET capm3DataClaim err is not nil but not is not found err, returning m3m nil and err")
		fmt.Println(err)
		return nil, err
	}

	metal3MachineName := ""
	for _, ownerRef := range capm3DataClaim.OwnerReferences {
		fmt.Println("inside  getM3Machine method for loop")
		fmt.Println(capm3DataClaim)
		oGV, err := schema.ParseGroupVersion(ownerRef.APIVersion)
		if err != nil {
			fmt.Println("inside  getM3Machine method for loop oGV error")
			return nil, err
		}
		// not matching on UID since when pivoting it might change
		// Not matching on API version as this might change
		if ownerRef.Kind == "Metal3Machine" &&
			oGV.Group == infrav1.GroupVersion.Group {
			fmt.Println("ownerref kind is m3m and ogv is infra group")
			metal3MachineName = ownerRef.Name
			fmt.Println(metal3MachineName)
			break
		}
	}
	if metal3MachineName == "" {
		fmt.Println("m3m name is emptyy")
		return nil, errors.New("Metal3Machine not found in owner references")
	}
	fmt.Println("inside getM3Machine method, calling now getM3Machine method with metal3MachineName and m3dt passed")
	fmt.Println(metal3MachineName, m.Data.Namespace, m3dt)
	return getM3Machine(ctx, m.client, m.Log,
		metal3MachineName, m.Data.Namespace, m3dt, true,
	)
}

func fetchM3IPClaim(ctx context.Context, cl client.Client, mLog logr.Logger,
	name, namespace string,
) (*ipamv1.IPClaim, error) {
	// Fetch the Metal3Data
	fmt.Println("===Inside fetchM3IPClaim===")
	metal3IPClaim := &ipamv1.IPClaim{}
	metal3ClaimName := types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}
	if err := cl.Get(ctx, metal3ClaimName, metal3IPClaim); err != nil {
		fmt.Println("===Inside fetchM3IPClaim get err is not nil===")
		if apierrors.IsNotFound(err) {
			fmt.Println(err)
			fmt.Println("===Inside fetchM3IPClaim get api error is not found===")
			mLog.Info("Address claim not found, requeuing")
			return nil, &RequeueAfterError{RequeueAfter: requeueAfter}
		}
		err := errors.Wrap(err, "Failed to get address claim")
		return nil, err
	}
	fmt.Println("===returing metal3IPClaim from fetchM3IPClaim===")
	fmt.Println(metal3IPClaim)
	return metal3IPClaim, nil
}
