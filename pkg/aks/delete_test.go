package aks

import (
	"github.com/Azure/azure-sdk-for-go/services/containerservice/mgmt/2020-11-01/containerservice"
	"github.com/Azure/go-autorest/autorest/azure"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rancher/aks-operator/pkg/aks/services/mock_services"
	aksv1 "github.com/rancher/aks-operator/pkg/apis/aks.cattle.io/v1"
	"go.uber.org/mock/gomock"
)

var _ = Describe("RemoveCluster", func() {
	var (
		mockController    *gomock.Controller
		clusterClientMock *mock_services.MockManagedClustersClientInterface
		clusterSpec       *aksv1.AKSClusterConfigSpec
	)

	BeforeEach(func() {
		mockController = gomock.NewController(GinkgoT())
		clusterClientMock = mock_services.NewMockManagedClustersClientInterface(mockController)
		clusterSpec = &aksv1.AKSClusterConfigSpec{
			ResourceGroup: "resourcegroup",
			ClusterName:   "clustername",
		}
	})

	AfterEach(func() {
		mockController.Finish()
	})

	It("should successfully delete cluster", func() {
		clusterClientMock.EXPECT().Delete(ctx, clusterSpec.ResourceGroup, clusterSpec.ClusterName).Return(containerservice.ManagedClustersDeleteFuture{
			FutureAPI: &azure.Future{},
		}, nil)
		clusterClientMock.EXPECT().WaitForTaskCompletion(ctx, gomock.Any()).Return(nil)
		Expect(RemoveCluster(ctx, clusterClientMock, clusterSpec)).To(Succeed())
	})
})
