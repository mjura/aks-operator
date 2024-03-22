package aks

import (
	"errors"

	azcoreto "github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/Azure/go-autorest/autorest/to"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rancher/aks-operator/pkg/aks/services/mock_services"
	aksv1 "github.com/rancher/aks-operator/pkg/apis/aks.cattle.io/v1"
	"go.uber.org/mock/gomock"
)

var _ = Describe("updateCluster", func() {
	var (
		mockController       *gomock.Controller
		workplacesClientMock *mock_services.MockWorkplacesClientInterface
		clusterSpec          *aksv1.AKSClusterConfigSpec
		cred                 *Credentials
		actualCluster        *armcontainerservice.ManagedCluster
	)

	BeforeEach(func() {
		mockController = gomock.NewController(GinkgoT())
		workplacesClientMock = mock_services.NewMockWorkplacesClientInterface(mockController)
		clusterSpec = &aksv1.AKSClusterConfigSpec{
			ResourceGroup:     "test-rg",
			ClusterName:       "test-cluster",
			KubernetesVersion: to.StringPtr("test-version"),
			NodePools: []aksv1.AKSNodePool{
				{
					Name:       to.StringPtr("test-nodepool"),
					MaxSurge:   to.StringPtr("13%"),
					NodeTaints: to.StringSlicePtr([]string{"node=taint:NoSchedule"}),
					NodeLabels: map[string]*string{
						"node-label": to.StringPtr("test-value"),
					},
				},
			},
			AuthorizedIPRanges:      to.StringSlicePtr([]string{"test-ip-range"}),
			LinuxAdminUsername:      to.StringPtr("test-admin-username"),
			LinuxSSHPublicKey:       to.StringPtr("test-ssh-public-key"),
			NetworkPlugin:           to.StringPtr("azure"),
			NetworkPolicy:           to.StringPtr("azure"),
			NetworkDNSServiceIP:     to.StringPtr("test-dns-service-ip"),
			NetworkDockerBridgeCIDR: to.StringPtr("test-docker-bridge-cidr"),
			NetworkPodCIDR:          to.StringPtr("test-pod-cidr"),
			NetworkServiceCIDR:      to.StringPtr("test-service-cidr"),
			LoadBalancerSKU:         to.StringPtr("standard"),
			Tags: map[string]string{
				"test-tag": "test-value",
			},
		}
		cred = &Credentials{
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
		}
		actualCluster = &armcontainerservice.ManagedCluster{
			Properties: &armcontainerservice.ManagedClusterProperties{
				AddonProfiles: map[string]*armcontainerservice.ManagedClusterAddonProfile{
					"test-addon": {},
				},
			},
		}
	})

	AfterEach(func() {
		mockController.Finish()
	})

	It("should successfully update cluster", func() {
		desiredCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "phase")
		Expect(err).ToNot(HaveOccurred())

		updatedCluster := updateCluster(*desiredCluster, *actualCluster)
		Expect(updatedCluster.Properties.KubernetesVersion).To(Equal(clusterSpec.KubernetesVersion))
		Expect(updatedCluster.Properties.AddonProfiles).To(HaveKey("test-addon"))
		Expect(updatedCluster.Properties.AddonProfiles).To(HaveKey("httpApplicationRouting"))
		agentPoolProfiles := updatedCluster.Properties.AgentPoolProfiles
		Expect(agentPoolProfiles).To(HaveLen(1))
		Expect(agentPoolProfiles[0].Name).To(Equal(clusterSpec.NodePools[0].Name))
		Expect(agentPoolProfiles[0].OrchestratorVersion).To(Equal(clusterSpec.KubernetesVersion))
		Expect(agentPoolProfiles[0].UpgradeSettings.MaxSurge).To(Equal(clusterSpec.NodePools[0].MaxSurge))
		expectedNodeTaints := agentPoolProfiles[0].NodeTaints
		clusterSpecNodeTaints := *clusterSpec.NodePools[0].NodeTaints
		Expect(expectedNodeTaints).To(HaveLen(1))
		Expect(*expectedNodeTaints[0]).To(Equal(clusterSpecNodeTaints[0]))
		Expect(agentPoolProfiles[0].NodeLabels).To(HaveKeyWithValue("node-label", to.StringPtr("test-value")))
		Expect(updatedCluster.Properties.APIServerAccessProfile).ToNot(BeNil())
		authorizedIPranges := updatedCluster.Properties.APIServerAccessProfile.AuthorizedIPRanges
		Expect(authorizedIPranges).To(HaveLen(1))
		Expect(*authorizedIPranges[0]).To(Equal("test-ip-range"))
		Expect(updatedCluster.Properties.LinuxProfile).ToNot(BeNil())
		Expect(updatedCluster.Properties.LinuxProfile.AdminUsername).To(Equal(clusterSpec.LinuxAdminUsername))
		sshPublicKeys := updatedCluster.Properties.LinuxProfile.SSH.PublicKeys
		Expect(sshPublicKeys).To(HaveLen(1))
		Expect(sshPublicKeys[0].KeyData).To(Equal(clusterSpec.LinuxSSHPublicKey))
		Expect(updatedCluster.Properties.NetworkProfile).ToNot(BeNil())
		Expect(*updatedCluster.Properties.NetworkProfile.NetworkPlugin).To(Equal(armcontainerservice.NetworkPluginAzure))
		Expect(*updatedCluster.Properties.NetworkProfile.NetworkPolicy).To(Equal(armcontainerservice.NetworkPolicyAzure))
		Expect(updatedCluster.Properties.NetworkProfile.DNSServiceIP).To(Equal(clusterSpec.NetworkDNSServiceIP))
		Expect(updatedCluster.Properties.NetworkProfile.PodCidr).To(Equal(clusterSpec.NetworkPodCIDR))
		Expect(updatedCluster.Properties.NetworkProfile.ServiceCidr).To(Equal(clusterSpec.NetworkServiceCIDR))
		Expect(*updatedCluster.Properties.NetworkProfile.LoadBalancerSKU).To(Equal(armcontainerservice.LoadBalancerSKUStandard))
		Expect(updatedCluster.Properties.ServicePrincipalProfile).ToNot(BeNil())
		Expect(updatedCluster.Properties.ServicePrincipalProfile.ClientID).To(Equal(to.StringPtr(cred.ClientID)))
		Expect(updatedCluster.Properties.ServicePrincipalProfile.Secret).To(Equal(to.StringPtr(cred.ClientSecret)))
		Expect(updatedCluster.Tags).To(HaveKeyWithValue("test-tag", to.StringPtr("test-value")))
	})

	It("shouldn't update kubernetes version if it's not specified", func() {
		clusterSpec.KubernetesVersion = nil
		desiredCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "phase")
		Expect(err).ToNot(HaveOccurred())

		updatedCluster := updateCluster(*desiredCluster, *actualCluster)
		Expect(updatedCluster.Properties.KubernetesVersion).To(BeNil())
	})

	It("shouldn't add new agent pool profile if it already exists", func() {
		actualCluster.Properties.AgentPoolProfiles = []*armcontainerservice.ManagedClusterAgentPoolProfile{
			{
				Name: to.StringPtr("test-nodepool"),
			},
		}
		desiredCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "phase")
		Expect(err).ToNot(HaveOccurred())

		updatedCluster := updateCluster(*desiredCluster, *actualCluster)
		agentPoolProfiles := updatedCluster.Properties.AgentPoolProfiles
		Expect(agentPoolProfiles).To(HaveLen(1))
	})

	It("shouldn't set authorized IP ranges if not specified in cluster spec", func() {
		clusterSpec.AuthorizedIPRanges = nil
		desiredCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "phase")
		Expect(err).ToNot(HaveOccurred())

		updatedCluster := updateCluster(*desiredCluster, *actualCluster)
		Expect(updatedCluster.Properties.APIServerAccessProfile).ToNot(BeNil())
		Expect(updatedCluster.Properties.APIServerAccessProfile.AuthorizedIPRanges).ToNot(BeNil())
		authorizedIPranges := updatedCluster.Properties.APIServerAccessProfile.AuthorizedIPRanges
		Expect(authorizedIPranges).To(HaveLen(0))
	})

	It("shoudn't add new authorized IP range if it already exists ", func() {
		actualCluster.Properties.APIServerAccessProfile = &armcontainerservice.ManagedClusterAPIServerAccessProfile{
			AuthorizedIPRanges: []*string{azcoreto.Ptr("test-ip-range")},
		}
		desiredCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "phase")
		Expect(err).ToNot(HaveOccurred())

		updatedCluster := updateCluster(*desiredCluster, *actualCluster)
		Expect(updatedCluster.Properties.APIServerAccessProfile.AuthorizedIPRanges).To(Equal(actualCluster.Properties.APIServerAccessProfile.AuthorizedIPRanges))
		Expect(updatedCluster.Properties.APIServerAccessProfile).ToNot(BeNil())
		authorizedIPranges := updatedCluster.Properties.APIServerAccessProfile.AuthorizedIPRanges
		Expect(authorizedIPranges).To(HaveLen(1))
		Expect(*authorizedIPranges[0]).To(Equal("test-ip-range"))
	})

	It("shouldn't update linux profile if it's not specified", func() {
		clusterSpec.LinuxAdminUsername = nil
		clusterSpec.LinuxSSHPublicKey = nil
		desiredCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "phase")
		Expect(err).ToNot(HaveOccurred())

		updatedCluster := updateCluster(*desiredCluster, *actualCluster)
		Expect(updatedCluster.Properties.LinuxProfile).To(BeNil())
	})

	It("shouldn't update service principal if phase is active or updating", func() {
		desiredCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "active")
		Expect(err).ToNot(HaveOccurred())

		updatedCluster := updateCluster(*desiredCluster, *actualCluster)
		Expect(updatedCluster.Properties.ServicePrincipalProfile).To(BeNil())
	})

	It("shouldn't update tags if not specified in cluster spec", func() {
		clusterSpec.Tags = nil
		desiredCluster, err := createManagedCluster(ctx, cred, workplacesClientMock, clusterSpec, "phase")
		Expect(err).ToNot(HaveOccurred())

		updatedCluster := updateCluster(*desiredCluster, *actualCluster)
		Expect(updatedCluster.Tags).To(HaveLen(0))
	})
})

var _ = Describe("UpdateCluster", func() {
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
		clusterSpec = &aksv1.AKSClusterConfigSpec{
			ResourceGroup: "test-rg",
			ClusterName:   "test-cluster",
		}
	})

	AfterEach(func() {
		mockController.Finish()
	})

	It("should successfully update cluster", func() {
		clusterClientMock.EXPECT().Get(ctx, clusterSpec.ResourceGroup, clusterSpec.ClusterName, nil).Return(armcontainerservice.ManagedClustersClientGetResponse{}, nil)
		clusterClientMock.EXPECT().BeginCreateOrUpdate(ctx, clusterSpec.ResourceGroup, clusterSpec.ClusterName, gomock.Any(), nil).Return(pollerMock, nil)
		pollerMock.EXPECT().PollUntilDone(ctx, nil).Return(armcontainerservice.ManagedClustersClientCreateOrUpdateResponse{}, nil)
		Expect(UpdateCluster(ctx, &Credentials{}, clusterClientMock, workplacesClientMock, clusterSpec, "active")).To(Succeed())
	})

	It("should fail when createManagedCluster returns error", func() {
		clusterSpec.Monitoring = to.BoolPtr(true)
		Expect(UpdateCluster(ctx, &Credentials{}, clusterClientMock, workplacesClientMock, clusterSpec, "active")).ToNot(Succeed())
	})

	It("should fail when azure API returns error on Get() request", func() {
		clusterClientMock.EXPECT().Get(ctx, clusterSpec.ResourceGroup, clusterSpec.ClusterName, nil).Return(armcontainerservice.ManagedClustersClientGetResponse{}, errors.New("test error"))
		Expect(UpdateCluster(ctx, &Credentials{}, clusterClientMock, workplacesClientMock, clusterSpec, "active")).ToNot(Succeed())
	})

	It("should fail when azure API returns error on CreateOrUpdate() request", func() {
		clusterClientMock.EXPECT().Get(ctx, clusterSpec.ResourceGroup, clusterSpec.ClusterName, nil).Return(armcontainerservice.ManagedClustersClientGetResponse{}, nil)
		clusterClientMock.EXPECT().BeginCreateOrUpdate(ctx, clusterSpec.ResourceGroup, clusterSpec.ClusterName, gomock.Any(), nil).Return(pollerMock, errors.New("test error"))
		Expect(UpdateCluster(ctx, &Credentials{}, clusterClientMock, workplacesClientMock, clusterSpec, "active")).ToNot(Succeed())
	})
})

var _ = Describe("validateUpdate", func() {
	var (
		desiredCluster *armcontainerservice.ManagedCluster
		actualCluster  *armcontainerservice.ManagedCluster
	)

	BeforeEach(func() {
		desiredCluster = &armcontainerservice.ManagedCluster{
			Name:     to.StringPtr("test-cluster"),
			Location: to.StringPtr("test-location"),
			Properties: &armcontainerservice.ManagedClusterProperties{
				DNSPrefix: to.StringPtr("test-dns-prefix"),
				APIServerAccessProfile: &armcontainerservice.ManagedClusterAPIServerAccessProfile{
					EnablePrivateCluster: to.BoolPtr(true),
				},
			},
		}
		actualCluster = &armcontainerservice.ManagedCluster{
			Name:     to.StringPtr("test-cluster"),
			Location: to.StringPtr("test-location"),
			Properties: &armcontainerservice.ManagedClusterProperties{
				DNSPrefix: to.StringPtr("test-dns-prefix"),
				APIServerAccessProfile: &armcontainerservice.ManagedClusterAPIServerAccessProfile{
					EnablePrivateCluster: to.BoolPtr(true),
				},
			},
		}
	})

	It("should be true if cluster can be updated", func() {
		Expect(validateUpdate(*desiredCluster, *actualCluster)).To(BeTrue())
	})

	It("should be false if cluster name is different", func() {
		desiredCluster.Name = to.StringPtr("test-cluster-2")
		Expect(validateUpdate(*desiredCluster, *actualCluster)).To(BeFalse())
	})

	It("should be false if cluster location is different", func() {
		desiredCluster.Location = to.StringPtr("test-location-2")
		Expect(validateUpdate(*desiredCluster, *actualCluster)).To(BeFalse())
	})

	It("should be false if cluster dns prefix is different", func() {
		desiredCluster.Properties.DNSPrefix = to.StringPtr("test-dns-prefix-2")
		Expect(validateUpdate(*desiredCluster, *actualCluster)).To(BeFalse())
	})

	It("should be false if cluster private cluster is different", func() {
		desiredCluster.Properties.APIServerAccessProfile.EnablePrivateCluster = to.BoolPtr(false)
		Expect(validateUpdate(*desiredCluster, *actualCluster)).To(BeFalse())
	})
})
