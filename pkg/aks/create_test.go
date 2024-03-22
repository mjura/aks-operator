package aks

import (
	"errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	azcoreto "github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/operationalinsights/armoperationalinsights"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/go-autorest/autorest/to"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rancher/aks-operator/pkg/aks/services/mock_services"
	aksv1 "github.com/rancher/aks-operator/pkg/apis/aks.cattle.io/v1"
	"github.com/rancher/aks-operator/pkg/utils"
	"go.uber.org/mock/gomock"
)

var _ = Describe("CreateResourceGroup", func() {
	var (
		mockController          *gomock.Controller
		mockResourceGroupClient *mock_services.MockResourceGroupsClientInterface
		resourceGroupName       = "test-rg"
		location                = "eastus"
	)

	BeforeEach(func() {
		mockController = gomock.NewController(GinkgoT())
		mockResourceGroupClient = mock_services.NewMockResourceGroupsClientInterface(mockController)
	})

	AfterEach(func() {
		mockController.Finish()
	})

	It("should successfully create a resource group", func() {
		mockResourceGroupClient.EXPECT().CreateOrUpdate(ctx, resourceGroupName, armresources.ResourceGroup{
			Name:     azcoreto.Ptr(resourceGroupName),
			Location: azcoreto.Ptr(location),
		}, nil).Return(armresources.ResourceGroupsClientCreateOrUpdateResponse{}, nil)

		Expect(CreateResourceGroup(ctx, mockResourceGroupClient, &aksv1.AKSClusterConfigSpec{
			ResourceGroup:    resourceGroupName,
			ResourceLocation: location,
		})).To(Succeed())
	})

	It("should catch error when resource group creation fails", func() {
		mockResourceGroupClient.EXPECT().CreateOrUpdate(ctx, resourceGroupName, armresources.ResourceGroup{
			Name:     azcoreto.Ptr(resourceGroupName),
			Location: azcoreto.Ptr(location),
		}, nil).Return(armresources.ResourceGroupsClientCreateOrUpdateResponse{}, errors.New("failed to create resource group"))

		err := CreateResourceGroup(ctx, mockResourceGroupClient, &aksv1.AKSClusterConfigSpec{
			ResourceGroup:    resourceGroupName,
			ResourceLocation: location,
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to create resource group"))
	})
})

var _ = Describe("newManagedCluster", func() {
	var (
		mockController       *gomock.Controller
		workplacesClientMock *mock_services.MockWorkplacesClientInterface
		clusterSpec          *aksv1.AKSClusterConfigSpec
		cred                 *Credentials
	)

	BeforeEach(func() {
		mockController = gomock.NewController(GinkgoT())
		workplacesClientMock = mock_services.NewMockWorkplacesClientInterface(mockController)
		clusterSpec = newTestClusterSpec()
		clusterSpec.Monitoring = to.BoolPtr(true)
		cred = &Credentials{
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
			TenantID:     "test-tenant-id",
		}
	})

	AfterEach(func() {
		mockController.Finish()
	})

	It("should successfully create a managed cluster", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)

		clusterSpec.LoadBalancerSKU = to.StringPtr("standard")
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())

		Expect(managedCluster.Tags).To(HaveKeyWithValue("test-tag", to.StringPtr("test-value")))
		Expect(*managedCluster.Properties.NetworkProfile.NetworkPolicy).To(Equal(armcontainerservice.NetworkPolicy(to.String(clusterSpec.NetworkPolicy))))
		Expect(*managedCluster.Properties.NetworkProfile.LoadBalancerSKU).To(Equal(armcontainerservice.LoadBalancerSKU(to.String(clusterSpec.LoadBalancerSKU))))
		Expect(*managedCluster.Properties.NetworkProfile.NetworkPlugin).To(Equal(armcontainerservice.NetworkPlugin(to.String(clusterSpec.NetworkPlugin))))
		Expect(managedCluster.Properties.NetworkProfile.DNSServiceIP).To(Equal(clusterSpec.NetworkDNSServiceIP))
		Expect(managedCluster.Properties.NetworkProfile.ServiceCidr).To(Equal(clusterSpec.NetworkServiceCIDR))
		Expect(managedCluster.Properties.NetworkProfile.PodCidr).To(Equal(clusterSpec.NetworkPodCIDR))
		Expect(*managedCluster.Properties.NetworkProfile.OutboundType).To(Equal(armcontainerservice.OutboundTypeLoadBalancer))
		agentPoolProfiles := managedCluster.Properties.AgentPoolProfiles
		Expect(agentPoolProfiles).To(HaveLen(1))
		Expect(agentPoolProfiles[0].Name).To(Equal(clusterSpec.NodePools[0].Name))
		Expect(agentPoolProfiles[0].Count).To(Equal(clusterSpec.NodePools[0].Count))
		Expect(agentPoolProfiles[0].MaxPods).To(Equal(clusterSpec.NodePools[0].MaxPods))
		Expect(agentPoolProfiles[0].OSDiskSizeGB).To(Equal(clusterSpec.NodePools[0].OsDiskSizeGB))
		Expect(*agentPoolProfiles[0].OSDiskType).To(Equal(armcontainerservice.OSDiskType(clusterSpec.NodePools[0].OsDiskType)))
		Expect(*agentPoolProfiles[0].OSType).To(Equal(armcontainerservice.OSType(clusterSpec.NodePools[0].OsType)))
		Expect(*agentPoolProfiles[0].VMSize).To(Equal(clusterSpec.NodePools[0].VMSize))
		Expect(*agentPoolProfiles[0].Mode).To(Equal(armcontainerservice.AgentPoolMode(clusterSpec.NodePools[0].Mode)))
		Expect(agentPoolProfiles[0].OrchestratorVersion).To(Equal(clusterSpec.NodePools[0].OrchestratorVersion))
		expectedAvailabilityZones := agentPoolProfiles[0].AvailabilityZones
		clusterSpecAvailabilityZones := *clusterSpec.NodePools[0].AvailabilityZones
		Expect(expectedAvailabilityZones).To(HaveLen(1))
		Expect(*expectedAvailabilityZones[0]).To(Equal(clusterSpecAvailabilityZones[0]))
		Expect(agentPoolProfiles[0].EnableAutoScaling).To(Equal(clusterSpec.NodePools[0].EnableAutoScaling))
		Expect(agentPoolProfiles[0].MinCount).To(Equal(clusterSpec.NodePools[0].MinCount))
		Expect(agentPoolProfiles[0].MaxCount).To(Equal(clusterSpec.NodePools[0].MaxCount))
		Expect(agentPoolProfiles[0].UpgradeSettings.MaxSurge).To(Equal(clusterSpec.NodePools[0].MaxSurge))
		expectedNodeTaints := agentPoolProfiles[0].NodeTaints
		clusterSpecNodeTaints := *clusterSpec.NodePools[0].NodeTaints
		Expect(expectedNodeTaints).To(HaveLen(1))
		Expect(*expectedNodeTaints[0]).To(Equal(clusterSpecNodeTaints[0]))
		Expect(agentPoolProfiles[0].NodeLabels).To(HaveKeyWithValue("node-label", to.StringPtr("test-value")))
		Expect(managedCluster.Properties.LinuxProfile.AdminUsername).To(Equal(clusterSpec.LinuxAdminUsername))
		sshPublicKeys := managedCluster.Properties.LinuxProfile.SSH.PublicKeys
		Expect(sshPublicKeys).To(HaveLen(1))
		Expect(sshPublicKeys[0].KeyData).To(Equal(clusterSpec.LinuxSSHPublicKey))
		Expect(managedCluster.Properties.AddonProfiles).To(HaveKey("httpApplicationRouting"))
		Expect(managedCluster.Properties.AddonProfiles["httpApplicationRouting"].Enabled).To(Equal(clusterSpec.HTTPApplicationRouting))
		Expect(managedCluster.Properties.AddonProfiles).To(HaveKey("omsAgent"))
		Expect(managedCluster.Properties.AddonProfiles["omsAgent"].Enabled).To(Equal(clusterSpec.Monitoring))
		Expect(managedCluster.Properties.AddonProfiles["omsAgent"].Config).To(HaveKeyWithValue("logAnalyticsWorkspaceResourceID", to.StringPtr("/test-workspace-id")))
		Expect(managedCluster.Location).To(Equal(to.StringPtr(clusterSpec.ResourceLocation)))
		Expect(managedCluster.Properties.KubernetesVersion).To(Equal(clusterSpec.KubernetesVersion))
		Expect(managedCluster.Properties.ServicePrincipalProfile).ToNot(BeNil())
		Expect(managedCluster.Properties.ServicePrincipalProfile.ClientID).To(Equal(to.StringPtr(cred.ClientID)))
		Expect(managedCluster.Properties.ServicePrincipalProfile.Secret).To(Equal(to.StringPtr(cred.ClientSecret)))
		Expect(managedCluster.Properties.DNSPrefix).To(Equal(clusterSpec.DNSPrefix))
		Expect(managedCluster.Properties.APIServerAccessProfile).ToNot(BeNil())
		Expect(managedCluster.Properties.APIServerAccessProfile.AuthorizedIPRanges).ToNot(BeNil())
		ipRanges := managedCluster.Properties.APIServerAccessProfile.AuthorizedIPRanges
		clusterSpecIPRanges := *clusterSpec.AuthorizedIPRanges
		Expect(ipRanges).To(HaveLen(1))
		Expect(*ipRanges[0]).To(Equal(clusterSpecIPRanges[0]))
		Expect(managedCluster.Properties.APIServerAccessProfile.EnablePrivateCluster).To(Equal(clusterSpec.PrivateCluster))
		Expect(managedCluster.Identity).ToNot(BeNil())
		Expect(*managedCluster.Identity.Type).To(Equal(armcontainerservice.ResourceIdentityTypeSystemAssigned))
		Expect(managedCluster.Properties.APIServerAccessProfile.PrivateDNSZone).To(Equal(clusterSpec.PrivateDNSZone))
	})

	It("should successfully create managed cluster with custom load balancer sku", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		clusterSpec.LoadBalancerSKU = to.StringPtr("basic")
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())
		Expect(*managedCluster.Properties.NetworkProfile.LoadBalancerSKU).To(Equal(armcontainerservice.LoadBalancerSKUBasic))
	})

	It("should successfully create managed cluster with outboundtype userdefinedrouting", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		clusterSpec.OutboundType = to.StringPtr("userDefinedRouting")
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())
		Expect(*managedCluster.Properties.NetworkProfile.OutboundType).To(Equal(armcontainerservice.OutboundTypeUserDefinedRouting))
	})

	It("should successfully create managed cluster with custom network plugin without network profile", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		clusterSpec.NetworkPlugin = to.StringPtr("kubenet")
		clusterSpec.NetworkPolicy = to.StringPtr("calico")
		clusterSpec.NetworkDNSServiceIP = to.StringPtr("")
		clusterSpec.NetworkDockerBridgeCIDR = to.StringPtr("")
		clusterSpec.NetworkServiceCIDR = to.StringPtr("")
		clusterSpec.NetworkPodCIDR = to.StringPtr("")

		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())
		Expect(*managedCluster.Properties.NetworkProfile.NetworkPlugin).To(Equal(armcontainerservice.NetworkPluginKubenet))
		Expect(managedCluster.Properties.NetworkProfile.DNSServiceIP).To(Equal(clusterSpec.NetworkDNSServiceIP))
		Expect(managedCluster.Properties.NetworkProfile.ServiceCidr).To(Equal(clusterSpec.NetworkServiceCIDR))
		Expect(managedCluster.Properties.NetworkProfile.PodCidr).To(Equal(clusterSpec.NetworkPodCIDR))
	})

	It("should successfully create managed cluster with custom network plugin", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		clusterSpec.NetworkPlugin = to.StringPtr("kubenet")
		clusterSpec.NetworkPolicy = to.StringPtr("calico")
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())
		Expect(*managedCluster.Properties.NetworkProfile.NetworkPlugin).To(Equal(armcontainerservice.NetworkPluginKubenet))
		Expect(managedCluster.Properties.NetworkProfile.DNSServiceIP).To(Equal(clusterSpec.NetworkDNSServiceIP))
		Expect(managedCluster.Properties.NetworkProfile.ServiceCidr).To(Equal(clusterSpec.NetworkServiceCIDR))
		Expect(managedCluster.Properties.NetworkProfile.PodCidr).To(Equal(clusterSpec.NetworkPodCIDR))
	})

	It("should successfully create managed cluster with custom virtual network resource group", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		clusterSpec.VirtualNetworkResourceGroup = to.StringPtr("test-vnet-resource-group")
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())

		agentPoolProfiles := managedCluster.Properties.AgentPoolProfiles
		Expect(agentPoolProfiles).To(HaveLen(1))
		Expect(to.String(agentPoolProfiles[0].VnetSubnetID)).To(ContainSubstring(to.String(clusterSpec.VirtualNetworkResourceGroup)))
	})

	It("should successfully create managed cluster with orchestrator version", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		clusterSpec.NodePools[0].OrchestratorVersion = to.StringPtr("custom-orchestrator-version")
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())

		agentPoolProfiles := managedCluster.Properties.AgentPoolProfiles
		Expect(agentPoolProfiles).To(HaveLen(1))
		Expect(to.String(agentPoolProfiles[0].OrchestratorVersion)).To(ContainSubstring(to.String(clusterSpec.NodePools[0].OrchestratorVersion)))
	})

	It("should successfully create managed cluster with no availability zones set", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		clusterSpec.NodePools[0].AvailabilityZones = nil
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())
		agentPoolProfiles := managedCluster.Properties.AgentPoolProfiles
		Expect(agentPoolProfiles).To(HaveLen(1))
		Expect(agentPoolProfiles[0].AvailabilityZones).To(BeNil())
	})

	It("should successfully create managed cluster with no autoscaling enabled", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		clusterSpec.NodePools[0].EnableAutoScaling = nil
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())
		agentPoolProfiles := managedCluster.Properties.AgentPoolProfiles
		Expect(agentPoolProfiles).To(HaveLen(1))
		Expect(agentPoolProfiles[0].EnableAutoScaling).To(BeNil())
		Expect(agentPoolProfiles[0].MaxCount).To(BeNil())
		Expect(agentPoolProfiles[0].MinCount).To(BeNil())
	})

	It("should successfully create managed cluster with no custom virtual network", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		clusterSpec.VirtualNetwork = nil
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())

		agentPoolProfiles := managedCluster.Properties.AgentPoolProfiles
		Expect(agentPoolProfiles).To(HaveLen(1))
		Expect(agentPoolProfiles[0].VnetSubnetID).To(BeNil())
	})

	It("should successfully create managed cluster with no linux profile", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		clusterSpec.LinuxAdminUsername = nil
		clusterSpec.LinuxSSHPublicKey = nil
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())

		Expect(managedCluster.Properties.LinuxProfile).To(BeNil())
	})

	It("should successfully create managed cluster with no http application routing", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		clusterSpec.ResourceLocation = "chinaeast"
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())

		Expect(managedCluster.Properties.AddonProfiles).ToNot(HaveKey("httpApplicationRouting"))
	})

	It("should successfully create managed cluster with no monitoring enabled", func() {
		workplacesClientMock.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(armoperationalinsights.WorkspacesClientGetResponse{}, nil).Times(0)
		clusterSpec.Monitoring = nil
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())

		Expect(managedCluster.Properties.AddonProfiles).ToNot(HaveKey("omsagent"))
	})

	It("should successfully create managed cluster when phase is set to active", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "active")
		Expect(err).ToNot(HaveOccurred())

		Expect(managedCluster.Properties.ServicePrincipalProfile).To(BeNil())
	})

	It("should fail if LogAnalyticsWorkspaceForMonitoring returns error", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{}, errors.New("test-error"))

		workplacesClientMock.EXPECT().BeginCreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&runtime.Poller[armoperationalinsights.WorkspacesClientCreateOrUpdateResponse]{}, errors.New("test-error"))

		_, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).To(HaveOccurred())
	})

	It("should fail if network policy is azure and network plugin is kubenet", func() {
		workplacesClientMock.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(armoperationalinsights.WorkspacesClientGetResponse{}, nil).Times(0)
		clusterSpec.NetworkPlugin = to.StringPtr("kubenet")
		clusterSpec.NetworkPolicy = to.StringPtr("azure")
		_, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).To(HaveOccurred())
	})

	It("should successfully create managed cluster with custom node resource group name", func() {
		clusterSpec.NodeResourceGroup = to.StringPtr("test-node-resource-group-name")
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "active")
		Expect(err).ToNot(HaveOccurred())

		Expect(managedCluster.Properties.NodeResourceGroup).To(Equal(to.StringPtr("test-node-resource-group-name")))
	})

	It("should successfully create managed cluster with truncated default node resource group name over 80 characters", func() {
		clusterSpec.ClusterName = "this-is-a-cluster-with-a-very-long-name-that-is-over-80-characters"
		defaultResourceGroupName := "MC_" + clusterSpec.ResourceGroup + "_" + clusterSpec.ClusterName + "_" + clusterSpec.ResourceLocation
		truncated := defaultResourceGroupName[:80]
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "active")
		Expect(err).ToNot(HaveOccurred())
		Expect(managedCluster.Properties.NodeResourceGroup).To(Equal(to.StringPtr(truncated)))
	})

	It("should successfully create managed cluster with no TenantID provided", func() {
		workplacesClientMock.EXPECT().Get(ctx, to.String(clusterSpec.LogAnalyticsWorkspaceGroup), to.String(clusterSpec.LogAnalyticsWorkspaceName), nil).
			Return(armoperationalinsights.WorkspacesClientGetResponse{
				Workspace: armoperationalinsights.Workspace{
					ID: to.StringPtr("test-workspace-id"),
				},
			}, nil)
		cred.TenantID = ""
		managedCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "test-phase")
		Expect(err).ToNot(HaveOccurred())

		Expect(managedCluster.Identity).To(BeNil())
	})
})

var _ = Describe("CreateCluster", func() {
	var (
		mockController       *gomock.Controller
		workplacesClientMock *mock_services.MockWorkplacesClientInterface
		clusterClientMock    *mock_services.MockManagedClustersClientInterface
		pollerMock           *mock_services.MockPoller[armcontainerservice.ManagedClustersClientCreateOrUpdateResponse]
		clusterSpec          *aksv1.AKSClusterConfigSpec
	)

	BeforeEach(func() {
		mockController = gomock.NewController(GinkgoT())
		workplacesClientMock = mock_services.NewMockWorkplacesClientInterface(mockController)
		clusterClientMock = mock_services.NewMockManagedClustersClientInterface(mockController)
		pollerMock = mock_services.NewMockPoller[armcontainerservice.ManagedClustersClientCreateOrUpdateResponse](mockController)
		clusterSpec = newTestClusterSpec()
	})

	AfterEach(func() {
		mockController.Finish()
	})

	It("should successfully create managed cluster", func() {
		clusterClientMock.EXPECT().BeginCreateOrUpdate(
			ctx, clusterSpec.ResourceGroup, clusterSpec.ClusterName, gomock.Any(), gomock.Any()).Return(pollerMock, nil)
		Expect(CreateCluster(ctx, &Credentials{}, clusterClientMock, workplacesClientMock, clusterSpec, "test-phase")).To(Succeed())
	})

	It("should fail if clusterClient.CreateOrUpdate returns error", func() {
		clusterClientMock.EXPECT().BeginCreateOrUpdate(
			ctx, clusterSpec.ResourceGroup, clusterSpec.ClusterName, gomock.Any(), gomock.Any()).Return(pollerMock, errors.New("test-error"))
		Expect(CreateCluster(ctx, &Credentials{}, clusterClientMock, workplacesClientMock, clusterSpec, "test-phase")).ToNot(Succeed())
	})
})

var _ = Describe("CreateOrUpdateAgentPool", func() {
	var (
		mockController      *gomock.Controller
		agentPoolClientMock *mock_services.MockAgentPoolsClientInterface
		clusterSpec         *aksv1.AKSClusterConfigSpec
		nodePoolSpec        *aksv1.AKSNodePool
	)

	BeforeEach(func() {
		mockController = gomock.NewController(GinkgoT())
		agentPoolClientMock = mock_services.NewMockAgentPoolsClientInterface(mockController)
		clusterSpec = newTestClusterSpec()
		nodePoolSpec = &aksv1.AKSNodePool{
			Name:                to.StringPtr("test-nodepool"),
			Count:               to.Int32Ptr(1),
			MaxPods:             to.Int32Ptr(1),
			OsDiskSizeGB:        to.Int32Ptr(1),
			OsDiskType:          "Ephemeral",
			OsType:              "Linux",
			VMSize:              "Standard_D2_v2",
			Mode:                "System",
			OrchestratorVersion: to.StringPtr("test-version"),
			AvailabilityZones:   to.StringSlicePtr([]string{"test-az"}),
			EnableAutoScaling:   to.BoolPtr(true),
			MinCount:            to.Int32Ptr(1),
			MaxCount:            to.Int32Ptr(2),
			MaxSurge:            to.StringPtr("10%"),
			NodeTaints:          to.StringSlicePtr([]string{"node=taint:NoSchedule"}),
			NodeLabels: map[string]*string{
				"node-label": to.StringPtr("test-value"),
			},
		}
	})

	AfterEach(func() {
		mockController.Finish()
	})

	It("should successfully create agent pool", func() {
		agentPoolClientMock.EXPECT().BeginCreateOrUpdate(
			ctx, clusterSpec.ResourceGroup, clusterSpec.ClusterName, to.String(nodePoolSpec.Name),
			armcontainerservice.AgentPool{
				Properties: &armcontainerservice.ManagedClusterAgentPoolProfileProperties{
					Count:               nodePoolSpec.Count,
					MaxPods:             nodePoolSpec.MaxPods,
					OSDiskSizeGB:        nodePoolSpec.OsDiskSizeGB,
					OSDiskType:          azcoreto.Ptr(armcontainerservice.OSDiskType(nodePoolSpec.OsDiskType)),
					OSType:              azcoreto.Ptr(armcontainerservice.OSType(nodePoolSpec.OsType)),
					VMSize:              azcoreto.Ptr(nodePoolSpec.VMSize),
					Mode:                azcoreto.Ptr(armcontainerservice.AgentPoolMode(nodePoolSpec.Mode)),
					Type:                azcoreto.Ptr(armcontainerservice.AgentPoolTypeVirtualMachineScaleSets),
					OrchestratorVersion: nodePoolSpec.OrchestratorVersion,
					AvailabilityZones:   utils.ConvertToSliceOfPointers(nodePoolSpec.AvailabilityZones),
					EnableAutoScaling:   nodePoolSpec.EnableAutoScaling,
					MinCount:            nodePoolSpec.MinCount,
					MaxCount:            nodePoolSpec.MaxCount,
					NodeTaints:          utils.ConvertToSliceOfPointers(nodePoolSpec.NodeTaints),
					NodeLabels:          nodePoolSpec.NodeLabels,
					UpgradeSettings: &armcontainerservice.AgentPoolUpgradeSettings{
						MaxSurge: nodePoolSpec.MaxSurge,
					},
				},
			}).Return(&runtime.Poller[armcontainerservice.AgentPoolsClientCreateOrUpdateResponse]{}, nil)
		Expect(CreateOrUpdateAgentPool(ctx, agentPoolClientMock, clusterSpec, nodePoolSpec)).To(Succeed())
	})

	It("should fail if agentPoolClient.CreateOrUpdate returns error", func() {
		agentPoolClientMock.EXPECT().BeginCreateOrUpdate(
			ctx, clusterSpec.ResourceGroup, clusterSpec.ClusterName, to.String(nodePoolSpec.Name), gomock.Any()).
			Return(&runtime.Poller[armcontainerservice.AgentPoolsClientCreateOrUpdateResponse]{}, errors.New("test-error"))

		Expect(CreateOrUpdateAgentPool(ctx, agentPoolClientMock, clusterSpec, nodePoolSpec)).ToNot(Succeed())
	})
})

func newTestClusterSpec() *aksv1.AKSClusterConfigSpec {
	return &aksv1.AKSClusterConfigSpec{
		ResourceLocation: "eastus",
		Tags: map[string]string{
			"test-tag": "test-value",
		},
		NetworkPolicy:           to.StringPtr("azure"),
		NetworkPlugin:           to.StringPtr("azure"),
		NetworkDNSServiceIP:     to.StringPtr("test-dns-service-ip"),
		NetworkDockerBridgeCIDR: to.StringPtr("test-docker-bridge-cidr"),
		NetworkServiceCIDR:      to.StringPtr("test-service-cidr"),
		NetworkPodCIDR:          to.StringPtr("test-pod-cidr"),
		ResourceGroup:           "test-rg",
		VirtualNetwork:          to.StringPtr("test-virtual-network"),
		Subnet:                  to.StringPtr("test-subnet"),
		NodePools: []aksv1.AKSNodePool{
			{
				Name:                to.StringPtr("test-node-pool"),
				Count:               to.Int32Ptr(1),
				MaxPods:             to.Int32Ptr(1),
				OsDiskSizeGB:        to.Int32Ptr(1),
				OsDiskType:          "Ephemeral",
				OsType:              "Linux",
				VMSize:              "Standard_D2_v2",
				Mode:                "System",
				OrchestratorVersion: to.StringPtr("test-orchestrator-version"),
				AvailabilityZones:   to.StringSlicePtr([]string{"test-availability-zone"}),
				EnableAutoScaling:   to.BoolPtr(true),
				MinCount:            to.Int32Ptr(1),
				MaxCount:            to.Int32Ptr(2),
				MaxSurge:            to.StringPtr("10%"),
				NodeTaints:          to.StringSlicePtr([]string{"node=taint:NoSchedule"}),
				NodeLabels: map[string]*string{
					"node-label": to.StringPtr("test-value"),
				},
			},
		},
		LinuxAdminUsername:         to.StringPtr("test-admin-username"),
		LinuxSSHPublicKey:          to.StringPtr("test-ssh-public-key"),
		HTTPApplicationRouting:     to.BoolPtr(true),
		Monitoring:                 to.BoolPtr(false),
		KubernetesVersion:          to.StringPtr("test-kubernetes-version"),
		DNSPrefix:                  to.StringPtr("test-dns-prefix"),
		AuthorizedIPRanges:         to.StringSlicePtr([]string{"test-authorized-ip-range"}),
		PrivateCluster:             to.BoolPtr(true),
		PrivateDNSZone:             to.StringPtr("test-private-dns-zone"),
		LogAnalyticsWorkspaceGroup: to.StringPtr("test-log-analytics-workspace-group"),
		LogAnalyticsWorkspaceName:  to.StringPtr("test-log-analytics-workspace-name"),
	}
}
