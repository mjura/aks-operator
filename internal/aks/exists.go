package aks

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/services/containerservice/mgmt/2020-11-01/containerservice"
	"github.com/Azure/azure-sdk-for-go/services/resources/mgmt/2019-10-01/resources"
	aksv1 "github.com/rancher/aks-operator/pkg/apis/aks.cattle.io/v1"
)

func ExistsResourceGroup(ctx context.Context, groupsClient *resources.GroupsClient, resourceGroup string) bool {
	resp, err := groupsClient.CheckExistence(ctx, resourceGroup)
	if err != nil {
		return false
	}
	if resp.StatusCode == 204 {
		return true
	}

	return false
}

// Check if AKS managed Kubernetes cluster exist
func ExistsCluster(ctx context.Context, clusterClient *containerservice.ManagedClustersClient, spec *aksv1.AKSClusterConfigSpec) bool {
	resp, err := clusterClient.Get(ctx, spec.ResourceGroup, spec.ClusterName)

	return err == nil && resp.StatusCode == 200
}
