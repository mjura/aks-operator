package aks

import (
	"context"
	"fmt"
	"github.com/Azure/azure-sdk-for-go/services/resources/mgmt/2019-10-01/resources"

	"github.com/Azure/azure-sdk-for-go/services/containerservice/mgmt/2020-11-01/containerservice"
	"github.com/Azure/go-autorest/autorest/to"
	aksv1 "github.com/rancher/aks-operator/pkg/apis/aks.cattle.io/v1"
)

func CreateResourceGroup(ctx context.Context, groupsClient *resources.GroupsClient, spec *aksv1.AKSClusterConfigSpec) error {
	_, err := groupsClient.CreateOrUpdate(
		ctx,
		spec.ResourceGroup,
		resources.Group{
			Name:     to.StringPtr(spec.ResourceGroup),
			Location: to.StringPtr(spec.ResourceLocation),
		})

	return err
}

// createOrUpdateCluster creates a new managed Kubernetes cluster
func CreateOrUpdateCluster(ctx context.Context, cred *Credentials, clusterClient *containerservice.ManagedClustersClient, spec *aksv1.AKSClusterConfigSpec) (c containerservice.ManagedCluster, err error) {

	dnsPrefix := spec.DNSPrefix
	if dnsPrefix == nil {
		dnsPrefix = to.StringPtr(spec.ClusterName)
	}

	tags := make(map[string]*string)
	for key, val := range spec.Tags {
		if val != "" {
			tags[key] = to.StringPtr(val)
		}
	}
	clusterName := spec.ClusterName
	tags["displayName"] = to.StringPtr(clusterName)

	kubernetesVersion := spec.KubernetesVersion

	var vmNetSubnetID *string
	networkProfile := &containerservice.NetworkProfile{}
	if hasCustomVirtualNetwork(spec) {
		virtualNetworkResourceGroup := spec.ResourceGroup

		//if virtual network resource group is set, use it, otherwise assume it is the same as the cluster
		if spec.VirtualNetworkResourceGroup != nil {
			virtualNetworkResourceGroup = *spec.VirtualNetworkResourceGroup
		}

		vmNetSubnetID = to.StringPtr(fmt.Sprintf(
			"/subscriptions/%v/resourceGroups/%v/providers/Microsoft.Network/virtualNetworks/%v/subnets/%v",
			spec.SubscriptionID,
			virtualNetworkResourceGroup,
			spec.VirtualNetwork,
			spec.Subnet,
		))

		networkProfile.DNSServiceIP = spec.NetworkDNSServiceIP
		networkProfile.DockerBridgeCidr = spec.NetworkDockerBridgeCIDR
		networkProfile.ServiceCidr = spec.NetworkServiceCIDR

		if spec.NetworkPlugin != nil {
			networkProfile.NetworkPlugin = containerservice.Kubenet
		} else {
			networkProfile.NetworkPlugin = containerservice.NetworkPlugin(*spec.NetworkPlugin)
		}

		// if network plugin is 'kubenet', set PodCIDR
		if networkProfile.NetworkPlugin == containerservice.Azure {
			networkProfile.PodCidr = spec.NetworkPodCIDR
		}

		if spec.LoadBalancerSKU != nil {
			loadBalancerSku := containerservice.LoadBalancerSku(*spec.LoadBalancerSKU)
			networkProfile.LoadBalancerSku = loadBalancerSku
		}

		if spec.NetworkPolicy != nil {
			networkProfile.NetworkPolicy = containerservice.NetworkPolicy(*spec.NetworkPolicy)
		}
	}

	var agentPoolProfiles []containerservice.ManagedClusterAgentPoolProfile
	if hasAgentPoolProfile(spec) {
		for _, np := range spec.NodePools {
			if np.OrchestratorVersion == nil {
				np.OrchestratorVersion = spec.KubernetesVersion
			}
			agentProfile := containerservice.ManagedClusterAgentPoolProfile{
				Name:                np.Name,
				Count:               np.Count,
				MaxPods:             np.MaxPods,
				OsDiskSizeGB:        np.OsDiskSizeGB,
				OsType:              containerservice.OSType(np.OsType),
				VMSize:              containerservice.VMSizeTypes(np.VMSize),
				Mode:                containerservice.AgentPoolMode(np.Mode),
				OrchestratorVersion: np.OrchestratorVersion,
			}
			if np.AvailabilityZones != nil {
				agentProfile.AvailabilityZones = np.AvailabilityZones
			}
			if np.EnableAutoScaling != nil && *np.EnableAutoScaling {
				agentProfile.EnableAutoScaling = np.EnableAutoScaling
				agentProfile.MaxCount = np.MaxCount
				agentProfile.MinCount = np.MinCount
			}
			if hasCustomVirtualNetwork(spec) {
				agentProfile.VnetSubnetID = vmNetSubnetID
			}
			agentPoolProfiles = append(agentPoolProfiles, agentProfile)
		}
	}

	var linuxProfile *containerservice.LinuxProfile
	if hasLinuxProfile(spec) {
		linuxProfile = &containerservice.LinuxProfile{
			AdminUsername: spec.LinuxAdminUsername,
			SSH: &containerservice.SSHConfiguration{
				PublicKeys: &[]containerservice.SSHPublicKey{
					{
						KeyData: spec.LinuxSSHPublicKey,
					},
				},
			},
		}
	}

	if err != nil {
		return c, fmt.Errorf("couldn't get secrets with error: %v", err)
	}
	managedCluster := containerservice.ManagedCluster{
		Name:     to.StringPtr(spec.ClusterName),
		Location: to.StringPtr(spec.ResourceLocation),
		Tags:     tags,
		ManagedClusterProperties: &containerservice.ManagedClusterProperties{
			KubernetesVersion: kubernetesVersion,
			DNSPrefix:         dnsPrefix,
			AgentPoolProfiles: &agentPoolProfiles,
			LinuxProfile:      linuxProfile,
			NetworkProfile:    networkProfile,
			ServicePrincipalProfile: &containerservice.ManagedClusterServicePrincipalProfile{
				ClientID: to.StringPtr(cred.ClientID),
				Secret:   to.StringPtr(cred.ClientSecret),
			},
		},
	}

	if spec.AuthorizedIPRanges != nil {
		managedCluster.APIServerAccessProfile = &containerservice.ManagedClusterAPIServerAccessProfile{
			AuthorizedIPRanges: spec.AuthorizedIPRanges,
		}
	}
	if spec.PrivateCluster != nil && *spec.PrivateCluster {
		managedCluster.APIServerAccessProfile = &containerservice.ManagedClusterAPIServerAccessProfile{
			EnablePrivateCluster: spec.PrivateCluster,
		}
	}

	future, err := clusterClient.CreateOrUpdate(
		ctx,
		spec.ResourceGroup,
		spec.ClusterName,
		managedCluster,
	)
	if err != nil {
		return c, fmt.Errorf("[AKS] cannot create AKS cluster: %v", err)
	}

	if err = future.WaitForCompletionRef(ctx, clusterClient.Client); err != nil {
		return c, fmt.Errorf("can't get the AKS cluster create or update future response: %v", err)
	}

	return future.Result(*clusterClient)
}

// CreateOrUpdateAgentPool
func CreateOrUpdateAgentPool(ctx context.Context, agentPoolClient *containerservice.AgentPoolsClient, spec *aksv1.AKSClusterConfigSpec, np *aksv1.AKSNodePool) (p containerservice.AgentPool, err error) {
	agentProfile := &containerservice.ManagedClusterAgentPoolProfileProperties{
		Count:               np.Count,
		MaxPods:             np.MaxPods,
		OsDiskSizeGB:        np.OsDiskSizeGB,
		OsType:              containerservice.OSType(np.OsType),
		VMSize:              containerservice.VMSizeTypes(np.VMSize),
		Mode:                containerservice.AgentPoolMode(np.Mode),
		OrchestratorVersion: np.OrchestratorVersion,
	}

	future, err := agentPoolClient.CreateOrUpdate(ctx, spec.ResourceGroup, spec.ClusterName, to.String(np.Name), containerservice.AgentPool{
		ManagedClusterAgentPoolProfileProperties: agentProfile,
			})

	if err != nil {
		return p, fmt.Errorf("failed to update agent pool name %s with error: %v", to.String(np.Name), err)
	}

	if future.WaitForCompletionRef(ctx, agentPoolClient.Client); err != nil {
		return p, fmt.Errorf("can't get the AKS agent pool create or update future response: %v", err)
	}

	return future.Result(*agentPoolClient)
}


func hasCustomVirtualNetwork(spec *aksv1.AKSClusterConfigSpec) bool {
	return spec.VirtualNetwork != nil && spec.Subnet != nil
}

func hasAgentPoolProfile(spec *aksv1.AKSClusterConfigSpec) bool {
	return len(spec.NodePools) > 0
}

func hasLinuxProfile(spec *aksv1.AKSClusterConfigSpec) bool {
	return spec.LinuxAdminUsername != nil && spec.LinuxSSHPublicKey != nil
}