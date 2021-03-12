package controller

import (
	"context"
	"fmt"
	"github.com/rancher/aks-operator/internal/aks"
	"github.com/rancher/aks-operator/internal/utils"
	"reflect"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/containerservice/mgmt/2020-11-01/containerservice"
	"github.com/Azure/go-autorest/autorest/to"
	aksv1 "github.com/rancher/aks-operator/pkg/apis/aks.cattle.io/v1"
	v10 "github.com/rancher/aks-operator/pkg/generated/controllers/aks.cattle.io/v1"
	wranglerv1 "github.com/rancher/wrangler/pkg/generated/controllers/core/v1"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v15 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	controllerName           = "aks-controller"
	controllerRemoveName     = "aks-controller-remove"
	aksConfigNotCreatedPhase = ""
	aksConfigActivePhase     = "active"
	aksConfigUpdatingPhase   = "updating"
	aksConfigImportingPhase  = "importing"
	aksClusterConfigKind     = "AKSClusterConfig"
)

type Handler struct {
	aksCC           v10.AKSClusterConfigClient
	aksEnqueueAfter func(namespace, name string, duration time.Duration)
	aksEnqueue      func(namespace, name string)
	secrets         wranglerv1.SecretClient
	secretsCache    wranglerv1.SecretCache
}

func Register(
	ctx context.Context,
	secrets wranglerv1.SecretController,
	aks v10.AKSClusterConfigController) {

	controller := &Handler{
		aksCC:           aks,
		aksEnqueue:      aks.Enqueue,
		aksEnqueueAfter: aks.EnqueueAfter,
		secretsCache:    secrets.Cache(),
		secrets:         secrets,
	}

	// Register handlers
	aks.OnChange(ctx, controllerName, controller.recordError(controller.OnAksConfigChanged))
	aks.OnRemove(ctx, controllerRemoveName, controller.OnAksConfigRemoved)
}

func (h *Handler) OnAksConfigChanged(key string, config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error) {
	if config == nil || config.DeletionTimestamp != nil {
		return nil, nil
	}

	ctx := context.Background()
	switch config.Status.Phase {
	case aksConfigImportingPhase:
		return h.importCluster(ctx, config)
	case aksConfigNotCreatedPhase:
		return h.createCluster(ctx, config)
	case aksConfigActivePhase, aksConfigUpdatingPhase:
		return h.checkCluster(ctx, config)
	default:
		return config, fmt.Errorf("invalid phase: %v", config.Status.Phase)
	}
}

func (h *Handler) OnAksConfigRemoved(key string, config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error) {
	if config.Spec.Imported {
		logrus.Infof("Cluster [%s] is imported, will not delete AKS cluster", config.Spec.ClusterName)
		return config, nil
	}

	ctx := context.Background()
	logrus.Infof("Removing cluster [%s]", config.Spec.ClusterName)

	credentials, err := aks.GetSecrets(h.secretsCache, &config.Spec)
	if err != nil {
		return config, err
	}

	resourceClusterClient, err := aks.NewClusterClient(credentials)
	if err != nil {
		return config, err
	}

	if aks.ExistsCluster(ctx, resourceClusterClient, &config.Spec) {
		if err = aks.RemoveCluster(ctx, resourceClusterClient, &config.Spec); err != nil {
			return config, fmt.Errorf("error removing cluster [%s] message %v", config.Spec.ClusterName, err)
		}
	}

	logrus.Infof("Removing resource group [%s] for cluster [%s]", config.Spec.ResourceGroup, config.Spec.ClusterName)

	resourceGroupsClient, err := aks.NewResourceGroupClient(credentials)
	if err != nil {
		return config, err
	}

	if aks.ExistsResourceGroup(ctx, resourceGroupsClient, config.Spec.ResourceGroup) {
		if err = aks.RemoveResourceGroup(ctx, resourceGroupsClient, &config.Spec); err != nil {
			logrus.Errorf("Error removing resource group [%s] message: %v", config.Spec.ResourceGroup, err)
			return config, err
		}
	}

	logrus.Infof("Cluster [%s] was removed successfully", config.Spec.ClusterName)
	return config, nil
}

// recordError writes the error return by onChange to the failureMessage field on status. If there is no error, then
// empty string will be written to status
func (h *Handler) recordError(onChange func(key string, config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error)) func(key string, config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error) {
	return func(key string, config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error) {
		var err error
		var message string
		config, err = onChange(key, config)
		if config == nil {
			// AKS config is likely deleting
			return config, err
		}
		if err != nil {
			message = err.Error()
		}

		if config.Status.FailureMessage == message {
			return config, err
		}

		config = config.DeepCopy()
		if message != "" && config.Status.Phase == aksConfigActivePhase {
			// can assume an update is failing
			config.Status.Phase = aksConfigUpdatingPhase
		}
		config.Status.FailureMessage = message

		var recordErr error
		config, recordErr = h.aksCC.UpdateStatus(config)
		if recordErr != nil {
			logrus.Errorf("Error recording akscc [%s] failure message: %s", config.Name, recordErr.Error())
		}
		return config, err
	}
}

func (h *Handler) createCluster(ctx context.Context, config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error) {
	if err := h.validateConfig(config); err != nil {
		return config, err
	}

	if config.Spec.Imported {
		config = config.DeepCopy()
		config.Status.Phase = aksConfigImportingPhase
		return h.aksCC.UpdateStatus(config)
	}

	logrus.Infof("Creating cluster [%s]", config.Spec.ClusterName)

	credentials, err := aks.GetSecrets(h.secretsCache, &config.Spec)
	if err != nil {
		return config, err
	}

	resourceGroupsClient, err := aks.NewResourceGroupClient(credentials)
	if err != nil {
		return config, err
	}

	logrus.Infof("Checking if resource group [%s] exists", config.Spec.ResourceGroup)

	if !aks.ExistsResourceGroup(ctx, resourceGroupsClient, config.Spec.ResourceGroup) {
		logrus.Infof("Creating resource group [%s] for cluster [%s]", config.Spec.ResourceGroup, config.Spec.ClusterName)
		err = aks.CreateResourceGroup(ctx, resourceGroupsClient, &config.Spec)
		if err != nil {
			return config, fmt.Errorf("error creating resource group [%s] with message %v", config.Spec.ResourceGroup, err)
		}
		logrus.Infof("Resource group [%s] created successfully", config.Spec.ResourceGroup)
	}

	logrus.Infof("Creating AKS cluster [%s]", config.Spec.ClusterName)

	resourceClusterClient, err := aks.NewClusterClient(credentials)
	if err != nil {
		return config, err
	}

	result, err := aks.CreateOrUpdateCluster(ctx, credentials, resourceClusterClient, &config.Spec)
	if err != nil {
		return config, fmt.Errorf("error failed to create cluster: %v ", err)
	}

	clusterState := *result.ManagedClusterProperties.ProvisioningState
	if clusterState != "Succeeded" {
		return config, fmt.Errorf("error during provisioning cluster [%s] with status %v", config.Spec.ClusterName, clusterState)
	}

	logrus.Infof("Cluster [%s] created successfully", config.Spec.ClusterName)

	if err := h.createCASecret(ctx, config); err != nil {
		if !errors.IsAlreadyExists(err) {
			return config, err
		}
	}
	config = config.DeepCopy()
	config.Status.Phase = aksConfigActivePhase
	return h.aksCC.UpdateStatus(config)
}

func (h *Handler) importCluster(ctx context.Context, config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error) {

	logrus.Infof("Importing config for cluster [%s]", config.Spec.ClusterName)

	if err := h.createCASecret(ctx, config); err != nil {
		if !errors.IsAlreadyExists(err) {
			return config, err
		}
	}

	config = config.DeepCopy()
	config.Status.Phase = aksConfigActivePhase
	return h.aksCC.UpdateStatus(config)
}

func (h *Handler) checkCluster(ctx context.Context, config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error) {

	logrus.Infof("Checking configuration for cluster [%s]", config.Spec.ClusterName)
	upstreamSpec, err := BuildUpstreamClusterState(ctx, h.secretsCache, &config.Spec)
	if err != nil {
		return config, err
	}
	logrus.Infof("Comparing saved and running configuration for cluster [%s]", config.Spec.ClusterName)
	_, err = updateUpstreamClusterState(ctx, h.secretsCache, &config.Spec, upstreamSpec)
	if err != nil {
		return config, err
	}

	logrus.Infof("Configuration status for cluster [%s] was checked", config.Spec.ClusterName)

	// no new updates, set to active
	if config.Status.Phase != aksConfigActivePhase {
		config = config.DeepCopy()
		config.Status.Phase = aksConfigActivePhase
		return h.aksCC.UpdateStatus(config)
	}

	return config, nil
}

func (h *Handler) validateConfig(config *aksv1.AKSClusterConfig) error {
	// Check for existing AKSClusterConfigs with the same display name
	aksConfigs, err := h.aksCC.List(config.Namespace, v15.ListOptions{})
	if err != nil {
		return fmt.Errorf("cannot list AKSClusterConfig for display name check")
	}
	for _, c := range aksConfigs.Items {
		if c.Spec.ClusterName == config.Spec.ClusterName && c.Name != config.Name  {
			return fmt.Errorf("cannot create cluster [%s] because an AKSClusterConfig exists with the same name", config.Spec.ClusterName)
		}
	}

	cannotBeNilError := "field [%s] must be provided for cluster [%s] config"
	if config.Spec.ResourceLocation == "" {
		return fmt.Errorf(cannotBeNilError, "resourceLocation", config.ClusterName)
	}
	if config.Spec.ResourceGroup == "" {
		return fmt.Errorf(cannotBeNilError, "resourceGroup", config.ClusterName)
	}
	if config.Spec.ClusterName == "" {
		return fmt.Errorf(cannotBeNilError, "clusterName", config.ClusterName)
	}
	if config.Spec.SubscriptionID == "" {
		return fmt.Errorf(cannotBeNilError, "subscriptionId", config.ClusterName)
	}
	if config.Spec.TenantID == "" {
		return fmt.Errorf(cannotBeNilError, "tenantId", config.ClusterName)
	}
	if config.Spec.AzureCredentialSecret == "" {
		return fmt.Errorf(cannotBeNilError, "azureCredentialSecret", config.ClusterName)
	}
	creds, err := aks.GetSecrets(h.secretsCache, &config.Spec)
	if err != nil {
		return fmt.Errorf("could not get secrets with error: %v", err)
	}

	if creds.ClientID == "" || creds.ClientSecret == "" {
		return fmt.Errorf(cannotBeNilError, "clientId and clientSecret", config.ClusterName)
	}
	if config.Spec.Imported {
		return nil
	}
	if config.Spec.KubernetesVersion == nil {
		return fmt.Errorf(cannotBeNilError, "kubernetesVersion", config.ClusterName)
	}

	if len(config.Spec.NodePools) > 0 {
		for _, np := range config.Spec.NodePools {
			if np.Name == nil {
				return fmt.Errorf(cannotBeNilError, "NodePool.Name", config.ClusterName)
			}
			if np.Count == nil {
				return fmt.Errorf(cannotBeNilError, "NodePool.Count", config.ClusterName)
			}
			if np.MaxPods == nil {
				return fmt.Errorf(cannotBeNilError, "NodePool.MaxPods", config.ClusterName)
			}
			if np.VMSize == "" {
				return fmt.Errorf(cannotBeNilError, "NodePool.VMSize", config.ClusterName)
			}
			if np.OsDiskSizeGB == nil {
				return fmt.Errorf(cannotBeNilError, "NodePool.OsDiskSizeGB", config.ClusterName)
			}
			if np.OsDiskType == "" {
				return fmt.Errorf(cannotBeNilError, "NodePool.OSDiskType", config.ClusterName)
			}
			if np.Mode == "" {
				return fmt.Errorf(cannotBeNilError, "NodePool.Mode", config.ClusterName)
			}
			if np.OsType == "" {
				return fmt.Errorf(cannotBeNilError, "NodePool.OsType", config.ClusterName)
			}
		}
	}

	if config.Spec.NetworkPolicy != nil &&
		*config.Spec.NetworkPolicy != string(containerservice.NetworkPolicyAzure) &&
		*config.Spec.NetworkPolicy != string(containerservice.NetworkPolicyCalico) {
		return fmt.Errorf("wrong network policy value for [%s] cluster config", config.ClusterName)
	}
	return nil
}

// createCASecret creates a secret containing ca and endpoint. These can be used to create a kubeconfig via
// the go sdk
func (h *Handler) createCASecret(ctx context.Context, config *aksv1.AKSClusterConfig) error {
	kubeConfig, err := GetClusterKubeConfig(ctx, h.secretsCache, &config.Spec)
	if err != nil {
		return err
	}
	endpoint := kubeConfig.Host
	ca := string(kubeConfig.CAData)

	_, err = h.secrets.Create(
		&v1.Secret{
			ObjectMeta: v15.ObjectMeta{
				Name:      config.Name,
				Namespace: config.Namespace,
				OwnerReferences: []v15.OwnerReference{
					{
						APIVersion: aksv1.SchemeGroupVersion.String(),
						Kind:       aksClusterConfigKind,
						UID:        config.UID,
						Name:       config.Name,
					},
				},
			},
			Data: map[string][]byte{
				"endpoint": []byte(endpoint),
				"ca":       []byte(ca),
			},
		})
	return err
}

func GetClusterKubeConfig(ctx context.Context, secretsCache wranglerv1.SecretCache, spec *aksv1.AKSClusterConfigSpec) (restConfig *rest.Config, err error) {
	credentials, err := aks.GetSecrets(secretsCache, spec)
	if err != nil {
		return nil, err
	}
	resourceClusterClient, err := aks.NewClusterClient(credentials)
	if err != nil {
		return nil, err
	}
	accessProfile, err := resourceClusterClient.GetAccessProfile(ctx, spec.ResourceGroup, spec.ClusterName, "clusterAdmin")
	if err != nil {
		return nil, err
	}

	config, err := clientcmd.RESTConfigFromKubeConfig(*accessProfile.KubeConfig)
	if err != nil {
		return nil, err
	}
	return config, nil
}

// BuildUpstreamClusterState creates AKSClusterConfigSpec from existing cluster configuration
func BuildUpstreamClusterState(ctx context.Context, secretsCache wranglerv1.SecretCache, spec *aksv1.AKSClusterConfigSpec) (*aksv1.AKSClusterConfigSpec, error) {
	upstreamSpec := &aksv1.AKSClusterConfigSpec{}

	upstreamSpec.Imported = true

	credentials, err := aks.GetSecrets(secretsCache, spec)
	if err != nil {
		return nil, err
	}
	resourceClusterClient, err := aks.NewClusterClient(credentials)
	if err != nil {
		return nil, err
	}
	clusterState, err := resourceClusterClient.Get(ctx, spec.ResourceGroup, spec.ClusterName)
	if err != nil {
		return nil, err
	}

	// set Kubernetes version
	if clusterState.KubernetesVersion == nil {
		return nil, fmt.Errorf("cannot detect cluster [%s] upstream kubernetes version", spec.ClusterName)
	}
	upstreamSpec.KubernetesVersion = clusterState.KubernetesVersion

	// set tags
	upstreamSpec.Tags = make(map[string]string)
	if len(clusterState.Tags) != 0 {
		upstreamSpec.Tags = to.StringMap(clusterState.Tags)
	}

	// set AgentPool profile
	for _, np := range *clusterState.AgentPoolProfiles {
		var upstreamNP aksv1.AKSNodePool
		upstreamNP.Name = np.Name
		upstreamNP.Count = np.Count
		upstreamNP.MaxPods = np.MaxPods
		upstreamNP.VMSize = string(np.VMSize)
		upstreamNP.OsDiskSizeGB = np.OsDiskSizeGB
		upstreamNP.OsDiskType = string(np.OsDiskType)
		upstreamNP.Mode = string(np.Mode)
		upstreamNP.OsType = string(np.OsType)
		upstreamNP.OrchestratorVersion = np.OrchestratorVersion
		upstreamNP.AvailabilityZones = np.AvailabilityZones
		if np.EnableAutoScaling != nil {
			upstreamNP.EnableAutoScaling = np.EnableAutoScaling
			upstreamNP.MaxCount = np.MaxCount
			upstreamNP.MinCount = np.MinCount
		}
		upstreamSpec.NodePools = append(upstreamSpec.NodePools, upstreamNP)
	}

	// set network configuration
	networkProfile := clusterState.NetworkProfile
	if networkProfile != nil {
		upstreamSpec.NetworkPlugin = to.StringPtr(string(networkProfile.NetworkPlugin))
		upstreamSpec.NetworkDNSServiceIP = networkProfile.DNSServiceIP
		upstreamSpec.NetworkDockerBridgeCIDR = networkProfile.DockerBridgeCidr
		upstreamSpec.NetworkServiceCIDR = networkProfile.ServiceCidr
		upstreamSpec.NetworkPolicy = to.StringPtr(string(networkProfile.NetworkPolicy))
		upstreamSpec.NetworkPodCIDR = networkProfile.PodCidr
		upstreamSpec.LoadBalancerSKU = to.StringPtr(string(networkProfile.LoadBalancerSku))
	}

	// set linux account profile
	linuxProfile := clusterState.LinuxProfile
	if linuxProfile != nil {
		upstreamSpec.LinuxAdminUsername = linuxProfile.AdminUsername
		sshKeys := *linuxProfile.SSH.PublicKeys
		upstreamSpec.LinuxSSHPublicKey = sshKeys[0].KeyData
	}

	// set API server access profile
	upstreamSpec.PrivateCluster = to.BoolPtr(false)
	if clusterState.APIServerAccessProfile != nil {
		if clusterState.APIServerAccessProfile.EnablePrivateCluster != nil {
			upstreamSpec.PrivateCluster = clusterState.APIServerAccessProfile.EnablePrivateCluster
		}
		if clusterState.APIServerAccessProfile.AuthorizedIPRanges != nil {
			upstreamSpec.AuthorizedIPRanges = clusterState.APIServerAccessProfile.AuthorizedIPRanges
		}
	}

	return upstreamSpec, err
}

// updateUpstreamClusterState compares the upstream spec with the config spec, then updates the upstream AKS cluster to
// match the config spec. Function returns after a update is finished.
func updateUpstreamClusterState(ctx context.Context, secretsCache wranglerv1.SecretCache, spec *aksv1.AKSClusterConfigSpec, upstreamSpec *aksv1.AKSClusterConfigSpec) (*aksv1.AKSClusterConfigSpec, error) {
	credentials, err := aks.GetSecrets(secretsCache, spec)
	if err != nil {
		return spec, err
	}

	resourceClusterClient, err := aks.NewClusterClient(credentials)
	if err != nil {
		return spec, err
	}


	// check tags for update
	if spec.Tags != nil {
		if !reflect.DeepEqual(spec.Tags, upstreamSpec.Tags) {
			logrus.Infof("Updating tags for cluster [%s]", spec.ClusterName)
			tags := containerservice.TagsObject{
				Tags: *to.StringMapPtr(spec.Tags),
			}
			_, err = resourceClusterClient.UpdateTags(ctx, spec.ResourceGroup, spec.ClusterName, tags)
			if err != nil {
				return spec, err
			}
		}
	}

	agentPoolClient, err := aks.NewAgentPoolClient(credentials)
	if err != nil {
		return spec, err
	}

	needsUpdate := false
	// check NodePools for update
	if spec.NodePools != nil {
		upstreamNodePools := utils.BuildNodePoolMap(upstreamSpec.NodePools)
		for _, np := range spec.NodePools {
			upstreamNodePool, ok := upstreamNodePools[*np.Name]
			if ok {
				// There is a matching nodepool in the cluster already, so update it if needed
				if to.Int32(np.Count) != to.Int32(upstreamNodePool.Count) {
					logrus.Infof("Updating node count in node pool [%s] for cluster [%s]", to.String(np.Name), spec.ClusterName)
					needsUpdate = true
				}
				if to.Bool(np.EnableAutoScaling) != to.Bool(upstreamNodePool.EnableAutoScaling) {
					logrus.Infof("Updating autoscaling in node pool [%s] for cluster [%s]", to.String(np.Name), spec.ClusterName)
					needsUpdate = true
				}
				if to.String(np.OrchestratorVersion) != to.String(upstreamNodePool.OrchestratorVersion) {
					logrus.Infof("Updating orchestrator version in node pool [%s] for cluster [%s]", to.String(np.Name), spec.ClusterName)
					// needsUpdate = true
				}
			} else {
				needsUpdate = true
			}

			result, err := aks.CreateOrUpdateAgentPool(ctx, agentPoolClient, spec, &np)
			if err != nil {
				return nil, fmt.Errorf("failed to update cluster: %v", err)
			}

			if result.ManagedClusterAgentPoolProfileProperties.ProvisioningState == nil ||
				*result.ManagedClusterAgentPoolProfileProperties.ProvisioningState == "Succeeded" {
				logrus.Infof("Cluster Agent Pool [%s] was updated successfully", spec.ClusterName)
			}
		}
	}

	// check Kubernetes version for update
	if spec.KubernetesVersion != nil {
		if to.String(spec.KubernetesVersion) != to.String(upstreamSpec.KubernetesVersion) {
			logrus.Infof("Updating kubernetes version for cluster [%s]", spec.ClusterName)
			needsUpdate = true
		}
	}

	// check authorized IP ranges to access AKS
	if spec.AuthorizedIPRanges != nil {
		if !reflect.DeepEqual(spec.AuthorizedIPRanges, upstreamSpec.AuthorizedIPRanges) {
			logrus.Infof("Updating authorized IP ranges for cluster [%s]", spec.ClusterName)
			needsUpdate = true
		}
	}

	if !needsUpdate {
		return nil, nil
	}

	resourceGroupsClient, err := aks.NewResourceGroupClient(credentials)
	if err != nil {
		return nil, err
	}

	if !aks.ExistsResourceGroup(ctx, resourceGroupsClient, spec.ResourceGroup) {
		logrus.Infof("Resource group [%s] does not exist, creating", spec.ResourceGroup)
		if err := aks.CreateResourceGroup(ctx, resourceGroupsClient, spec); err != nil {
			return nil, fmt.Errorf("error during updating resource group %v", err)
		}
		logrus.Infof("Resource group [%s] updated successfully", spec.ResourceGroup)
	}

	result, err := aks.CreateOrUpdateCluster(ctx, credentials, resourceClusterClient, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to update cluster: %v", err)
	}

	if result.ManagedClusterProperties.ProvisioningState == nil ||
		*result.ManagedClusterProperties.ProvisioningState == "Succeeded" {
		logrus.Infof("Cluster [%s] was updated successfully", spec.ClusterName)
		return nil, nil
	}
	
	return nil, fmt.Errorf("cluster [%s] was updated with error: %s", spec.ClusterName, *result.ManagedClusterProperties.ProvisioningState)
}

