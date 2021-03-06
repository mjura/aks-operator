package controller

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"regexp"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/containerservice/mgmt/2020-11-01/containerservice"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/rancher/aks-operator/pkg/aks"
	aksv1 "github.com/rancher/aks-operator/pkg/apis/aks.cattle.io/v1"
	v10 "github.com/rancher/aks-operator/pkg/generated/controllers/aks.cattle.io/v1"
	"github.com/rancher/aks-operator/pkg/utils"
	wranglerv1 "github.com/rancher/wrangler/pkg/generated/controllers/core/v1"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v15 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	aksClusterConfigKind     = "AKSClusterConfig"
	controllerName           = "aks-controller"
	controllerRemoveName     = "aks-controller-remove"
	aksConfigCreatingPhase   = "creating"
	aksConfigNotCreatedPhase = ""
	aksConfigActivePhase     = "active"
	aksConfigUpdatingPhase   = "updating"
	aksConfigImportingPhase  = "importing"
	poolNameMaxLength        = 6
	wait                     = 30
)

// Cluster Status
const (
	// ClusterStatusSucceeded The Succeeeded state indicates the cluster has been
	// created and is fully usable, return code 0
	ClusterStatusSucceeded = "Succeeded"

	// ClusterStatusFailed The Failed state indicates the cluster is unusable, return code 1
	ClusterStatusFailed = "Failed"

	// ClusterStatusInProgress The InProgress state indicates that some work is
	// actively being done on the cluster, such as upgrading the master or
	// node software, return code 3
	ClusterStatusInProgress = "InProgress"

	// ClusterStatusUpgrading The Upgrading state indicates the cluster is updating
	ClusterStatusUpgrading = "Upgrading"

	// ClusterStatusCanceled The Canceled state indicates that create or update was canceled, return code 2
	ClusterStatusCanceled = "Canceled"

	// ClusterStatusDeleting The Deleting state indicates that cluster was removed, return code 4
	ClusterStatusDeleting = "Deleting"

	// NodePoolCreating The Creating state indicates that cluster was removed, return code 4
	NodePoolCreating = "Creating"

	// NodePoolScaling The Scaling state indicates that cluster was removed, return code 4
	NodePoolScaling = "Scaling"

	// NodePoolDeleting The Deleting state indicates that cluster was removed, return code 4
	NodePoolDeleting = "Deleting"

	// NodePoolUpgrading The Upgrading state indicates that cluster was upgraded
	NodePoolUpgrading = "Upgrading"
)

var matchWorkspaceGroup = regexp.MustCompile("/resourcegroups/(.+?)/")
var matchWorkspaceName = regexp.MustCompile("/workspaces/(.+?)")

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

	switch config.Status.Phase {
	case aksConfigImportingPhase:
		return h.importCluster(config)
	case aksConfigNotCreatedPhase:
		return h.createCluster(config)
	case aksConfigCreatingPhase:
		return h.waitForCluster(config)
	case aksConfigActivePhase, aksConfigUpdatingPhase:
		return h.checkAndUpdate(config)
	default:
		return config, fmt.Errorf("invalid phase: %v", config.Status.Phase)
	}
}

func (h *Handler) OnAksConfigRemoved(key string, config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error) {
	if config.Spec.Imported {
		logrus.Infof("Cluster [%s] is imported, will not delete AKS cluster", config.Spec.ClusterName)
		return config, nil
	}
	if config.Status.Phase == aksConfigNotCreatedPhase {
		// The most likely context here is that the cluster already existed in AKS, so we shouldn't delete it
		logrus.Warnf("Cluster [%s] never advanced to creating status, will not delete AKS cluster", config.Name)
		return config, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	logrus.Infof("Cluster [%s] was removed successfully", config.Spec.ClusterName)
	logrus.Infof("Resource group [%s] for cluster [%s] still exists, please remove it if needed", config.Spec.ResourceGroup, config.Spec.ClusterName)

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

func (h *Handler) createCluster(config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error) {
	if err := h.validateConfig(config); err != nil {
		return config, err
	}

	if config.Spec.Imported {
		config = config.DeepCopy()
		config.Status.Phase = aksConfigImportingPhase
		return h.aksCC.UpdateStatus(config)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	err = aks.CreateOrUpdateCluster(ctx, credentials, resourceClusterClient, &config.Spec)
	if err != nil {
		return config, fmt.Errorf("error failed to create cluster: %v ", err)
	}

	config = config.DeepCopy()
	config.Status.Phase = aksConfigCreatingPhase
	return h.aksCC.UpdateStatus(config)
}

func (h *Handler) importCluster(config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

func (h *Handler) checkAndUpdate(config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	credentials, err := aks.GetSecrets(h.secretsCache, &config.Spec)
	if err != nil {
		return config, err
	}

	resourceClusterClient, err := aks.NewClusterClient(credentials)
	if err != nil {
		return config, err
	}

	result, err := resourceClusterClient.Get(ctx, config.Spec.ResourceGroup, config.Spec.ClusterName)
	if err != nil {
		return config, err
	}

	clusterState := *result.ManagedClusterProperties.ProvisioningState
	if clusterState == ClusterStatusFailed {
		return config, fmt.Errorf("update failed for cluster [%s], status: %s", config.Spec.ClusterName, clusterState)
	}
	if clusterState == ClusterStatusInProgress || clusterState == ClusterStatusUpgrading {
		// upstream cluster is already updating, must wait until sending next update
		logrus.Infof("Waiting for cluster [%s] to finish updating", config.Name)
		if config.Status.Phase != aksConfigUpdatingPhase {
			config = config.DeepCopy()
			config.Status.Phase = aksConfigUpdatingPhase
			return h.aksCC.UpdateStatus(config)
		}
		h.aksEnqueueAfter(config.Namespace, config.Name, 30*time.Second)
		return config, nil
	}

	for _, np := range *result.AgentPoolProfiles {
		if status := to.String(np.ProvisioningState); status == NodePoolCreating ||
			status == NodePoolScaling || status == NodePoolDeleting || status == NodePoolUpgrading {
			if config.Status.Phase != aksConfigUpdatingPhase {
				config = config.DeepCopy()
				config.Status.Phase = aksConfigUpdatingPhase
				config, err = h.aksCC.UpdateStatus(config)
				if err != nil {
					return config, err
				}
			}
			switch status {
			case NodePoolDeleting:
				logrus.Infof("Waiting for cluster [%s] to delete node pool [%s]", config.Name, to.String(np.Name))
			default:
				logrus.Infof("Waiting for cluster [%s] to update node pool [%s]", config.Name, to.String(np.Name))
			}
			h.aksEnqueueAfter(config.Namespace, config.Name, 30*time.Second)
			return config, nil
		}
	}

	logrus.Infof("Checking configuration for cluster [%s]", config.Spec.ClusterName)
	upstreamSpec, err := BuildUpstreamClusterState(ctx, h.secretsCache, &config.Spec)
	if err != nil {
		return config, err
	}
	return h.updateUpstreamClusterState(ctx, h.secretsCache, config, upstreamSpec)
}

func (h *Handler) validateConfig(config *aksv1.AKSClusterConfig) error {
	// Check for existing AKSClusterConfigs with the same display name
	aksConfigs, err := h.aksCC.List(config.Namespace, v15.ListOptions{})
	if err != nil {
		return fmt.Errorf("cannot list AKSClusterConfig for display name check")
	}
	for _, c := range aksConfigs.Items {
		if c.Spec.ClusterName == config.Spec.ClusterName && c.Name != config.Name {
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
	if config.Spec.AzureCredentialSecret == "" {
		return fmt.Errorf(cannotBeNilError, "azureCredentialSecret", config.ClusterName)
	}

	if _, err = aks.GetSecrets(h.secretsCache, &config.Spec); err != nil {
		return fmt.Errorf("couldn't get secret [%s] with error: %v", config.Spec.AzureCredentialSecret, err)
	}

	if config.Spec.Imported {
		return nil
	}
	if config.Spec.KubernetesVersion == nil {
		return fmt.Errorf(cannotBeNilError, "kubernetesVersion", config.ClusterName)
	}

	systemMode := false
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
		if np.Mode == "System" {
			systemMode = true
		}
		if np.OsType == "" {
			return fmt.Errorf(cannotBeNilError, "NodePool.OsType", config.ClusterName)
		}
		if np.OsType == "Windows" {
			return fmt.Errorf("windows node pools are not currently supported")
		}
	}
	if !systemMode || len(config.Spec.NodePools) < 1 {
		return fmt.Errorf("at least one NodePool with mode System is required")
	}

	if config.Spec.NetworkPolicy != nil &&
		*config.Spec.NetworkPolicy != string(containerservice.NetworkPolicyAzure) &&
		*config.Spec.NetworkPolicy != string(containerservice.NetworkPolicyCalico) {
		return fmt.Errorf("wrong network policy value for [%s] cluster config", config.ClusterName)
	}
	return nil
}

func (h *Handler) waitForCluster(config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	credentials, err := aks.GetSecrets(h.secretsCache, &config.Spec)
	if err != nil {
		return config, err
	}

	resourceClusterClient, err := aks.NewClusterClient(credentials)
	if err != nil {
		return config, err
	}

	result, err := resourceClusterClient.Get(ctx, config.Spec.ResourceGroup, config.Spec.ClusterName)
	if err != nil {
		return config, err
	}

	clusterState := *result.ManagedClusterProperties.ProvisioningState
	if clusterState == ClusterStatusFailed {
		return config, fmt.Errorf("creation for cluster [%s] status: %s", config.Spec.ClusterName, clusterState)
	}
	if clusterState == ClusterStatusSucceeded {
		if err = h.createCASecret(ctx, config); err != nil {
			if !errors.IsAlreadyExists(err) {
				return config, err
			}
		}
		logrus.Infof("Cluster [%s] created successfully", config.Spec.ClusterName)
		config = config.DeepCopy()
		config.Status.Phase = aksConfigActivePhase
		return h.aksCC.UpdateStatus(config)
	}

	logrus.Infof("Waiting for cluster [%s] to finish creating", config.Name)
	h.aksEnqueueAfter(config.Namespace, config.Name, wait*time.Second)

	return config, nil
}

// enqueueUpdate enqueues the config if it is already in the updating phase. Otherwise, the
// phase is updated to "updating". This is important because the object needs to reenter the
// onChange handler to start waiting on the update.
func (h *Handler) enqueueUpdate(config *aksv1.AKSClusterConfig) (*aksv1.AKSClusterConfig, error) {
	if config.Status.Phase == aksConfigUpdatingPhase {
		h.aksEnqueue(config.Namespace, config.Name)
		return config, nil
	}
	config = config.DeepCopy()
	config.Status.Phase = aksConfigUpdatingPhase
	return h.aksCC.UpdateStatus(config)
}

// createCASecret creates a secret containing ca and endpoint. These can be used to create a kubeconfig via
// the go sdk
func (h *Handler) createCASecret(ctx context.Context, config *aksv1.AKSClusterConfig) error {
	kubeConfig, err := GetClusterKubeConfig(ctx, h.secretsCache, &config.Spec)
	if err != nil {
		return err
	}
	endpoint := kubeConfig.Host
	ca := base64.StdEncoding.EncodeToString(kubeConfig.CAData)

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

	// set addons profile
	addonProfile := clusterState.AddonProfiles
	if addonProfile != nil && addonProfile["httpApplicationRouting"] != nil {
		upstreamSpec.HTTPApplicationRouting = addonProfile["httpApplicationRouting"].Enabled
	}

	// set addon monitoring profile
	if addonProfile["omsagent"] != nil {
		upstreamSpec.Monitoring = addonProfile["omsagent"].Enabled
		logAnalyticsWorkspaceResourceID := addonProfile["omsagent"].Config["logAnalyticsWorkspaceResourceID"]

		logAnalyticsWorkspaceGroup := matchWorkspaceGroup.FindStringSubmatch(to.String(logAnalyticsWorkspaceResourceID))[1]
		upstreamSpec.LogAnalyticsWorkspaceGroup = to.StringPtr(logAnalyticsWorkspaceGroup)

		logAnalyticsWorkspaceName := matchWorkspaceName.FindStringSubmatch(to.String(logAnalyticsWorkspaceResourceID))[1]
		upstreamSpec.LogAnalyticsWorkspaceName = to.StringPtr(logAnalyticsWorkspaceName)
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
func (h *Handler) updateUpstreamClusterState(ctx context.Context, secretsCache wranglerv1.SecretCache,
	config *aksv1.AKSClusterConfig, upstreamSpec *aksv1.AKSClusterConfigSpec) (*aksv1.AKSClusterConfig, error) {
	credentials, err := aks.GetSecrets(secretsCache, &config.Spec)
	if err != nil {
		return config, err
	}

	resourceClusterClient, err := aks.NewClusterClient(credentials)
	if err != nil {
		return config, err
	}

	// check tags for update
	if config.Spec.Tags != nil {
		if !reflect.DeepEqual(config.Spec.Tags, upstreamSpec.Tags) {
			logrus.Infof("Updating tags for cluster [%s]", config.Spec.ClusterName)
			tags := containerservice.TagsObject{
				Tags: *to.StringMapPtr(config.Spec.Tags),
			}
			_, err = resourceClusterClient.UpdateTags(ctx, config.Spec.ResourceGroup, config.Spec.ClusterName, tags)
			if err != nil {
				return config, err
			}
			return h.enqueueUpdate(config)
		}
	}

	if config.Spec.NodePools != nil {
		agentPoolClient, err := aks.NewAgentPoolClient(credentials)
		if err != nil {
			return config, err
		}

		downstreamNodePools, err := utils.BuildNodePoolMap(config.Spec.NodePools, config.Spec.ClusterName)
		if err != nil {
			return config, err
		}

		// check for updated NodePools
		upstreamNodePools, _ := utils.BuildNodePoolMap(upstreamSpec.NodePools, config.Spec.ClusterName)
		updateNodePool := false
		for npName, np := range downstreamNodePools {
			upstreamNodePool, ok := upstreamNodePools[npName]
			if ok {
				// There is a matching node pool in the cluster already, so update it if needed
				if to.Int32(np.Count) != to.Int32(upstreamNodePool.Count) {
					logrus.Infof("Updating node count in node pool [%s] for cluster [%s]", to.String(np.Name), config.Spec.ClusterName)
					updateNodePool = true
				}
				if np.EnableAutoScaling != nil && to.Bool(np.EnableAutoScaling) != to.Bool(upstreamNodePool.EnableAutoScaling) {
					logrus.Infof("Updating autoscaling in node pool [%s] for cluster [%s]", to.String(np.Name), config.Spec.ClusterName)
					updateNodePool = true
				}
				if np.OrchestratorVersion != nil && to.String(np.OrchestratorVersion) != to.String(upstreamNodePool.OrchestratorVersion) {
					logrus.Infof("Updating orchestrator version in node pool [%s] for cluster [%s]", to.String(np.Name), config.Spec.ClusterName)
					updateNodePool = true
				}
			} else {
				logrus.Infof("Adding node pool [%s] for cluster [%s]", to.String(np.Name), config.Spec.ClusterName)
				updateNodePool = true
			}

			if updateNodePool {
				err = aks.CreateOrUpdateAgentPool(ctx, agentPoolClient, &config.Spec, np)
				if err != nil {
					return config, fmt.Errorf("failed to update cluster: %v", err)
				}
				return h.enqueueUpdate(config)
			}
		}

		// check for removed NodePools
		for npName := range upstreamNodePools {
			if _, ok := downstreamNodePools[npName]; !ok {
				logrus.Infof("Removing node pool [%s] from cluster [%s]", npName, config.Spec.ClusterName)
				err = aks.RemoveAgentPool(ctx, agentPoolClient, &config.Spec, upstreamNodePools[npName])
				if err != nil {
					return config, fmt.Errorf("failed to remove node pool: %v", err)
				}
				return h.enqueueUpdate(config)
			}
		}
	}

	updateAksCluster := false
	// check Kubernetes version for update
	if config.Spec.KubernetesVersion != nil {
		if to.String(config.Spec.KubernetesVersion) != to.String(upstreamSpec.KubernetesVersion) {
			logrus.Infof("Updating kubernetes version for cluster [%s]", config.Spec.ClusterName)
			updateAksCluster = true
		}
	}

	// check authorized IP ranges to access AKS
	if config.Spec.AuthorizedIPRanges != nil {
		if !reflect.DeepEqual(config.Spec.AuthorizedIPRanges, upstreamSpec.AuthorizedIPRanges) {
			logrus.Infof("Updating authorized IP ranges for cluster [%s]", config.Spec.ClusterName)
			updateAksCluster = true
		}
	}

	// check addon HTTP Application Routing
	if config.Spec.HTTPApplicationRouting != nil {
		if to.Bool(config.Spec.HTTPApplicationRouting) != to.Bool(upstreamSpec.HTTPApplicationRouting) {
			logrus.Infof("Updating HTTP application routing for cluster [%s]", config.Spec.ClusterName)
		}
	}

	// check addon monitoring
	if config.Spec.Monitoring != nil {
		if to.Bool(config.Spec.Monitoring) != to.Bool(upstreamSpec.Monitoring) {
			logrus.Infof("Updating monitoring addon for cluster [%s]", config.Spec.ClusterName)
			updateAksCluster = true
		}
	}

	if updateAksCluster {
		resourceGroupsClient, err := aks.NewResourceGroupClient(credentials)
		if err != nil {
			return config, err
		}

		if !aks.ExistsResourceGroup(ctx, resourceGroupsClient, config.Spec.ResourceGroup) {
			logrus.Infof("Resource group [%s] does not exist, creating", config.Spec.ResourceGroup)
			if err = aks.CreateResourceGroup(ctx, resourceGroupsClient, &config.Spec); err != nil {
				return config, fmt.Errorf("error during updating resource group %v", err)
			}
			logrus.Infof("Resource group [%s] updated successfully", config.Spec.ResourceGroup)
		}

		err = aks.CreateOrUpdateCluster(ctx, credentials, resourceClusterClient, &config.Spec)
		if err != nil {
			return config, fmt.Errorf("failed to update cluster: %v", err)
		}
		return h.enqueueUpdate(config)
	}

	// no new updates, set to active
	if config.Status.Phase != aksConfigActivePhase {
		logrus.Infof("Cluster [%s] finished updating", config.Name)
		config = config.DeepCopy()
		config.Status.Phase = aksConfigActivePhase
		return h.aksCC.UpdateStatus(config)
	}

	logrus.Infof("Configuration for cluster [%s] was verified", config.Spec.ClusterName)
	return config, err
}
