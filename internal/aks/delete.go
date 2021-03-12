package aks

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/services/containerservice/mgmt/2020-11-01/containerservice"
	"github.com/Azure/azure-sdk-for-go/services/resources/mgmt/2019-10-01/resources"
	aksv1 "github.com/rancher/aks-operator/pkg/apis/aks.cattle.io/v1"
	"github.com/sirupsen/logrus"
)

func RemoveResourceGroup(ctx context.Context, groupsClient *resources.GroupsClient, spec *aksv1.AKSClusterConfigSpec) error {
	if !ExistsResourceGroup(ctx, groupsClient, spec.ResourceGroup) {
		logrus.Infof("Resource group %s for cluster [%s] doesn't exist", spec.ResourceGroup, spec.ClusterName)
		return nil
	}

	future, err := groupsClient.Delete(ctx, spec.ResourceGroup)
	if err != nil {
		return fmt.Errorf("error removing resource group '%s': %v", spec.ResourceGroup, err)
	}

	if err = future.WaitForCompletionRef(ctx, groupsClient.Client); err != nil {
		return fmt.Errorf("cannot get the AKS cluster create or update future response: %v", err)
	}

	logrus.Infof("Resource group %s for cluster [%s] removed successfully", spec.ResourceGroup, spec.ClusterName)
	return nil
}

// Delete AKS managed Kubernetes cluster
func RemoveCluster(ctx context.Context, clusterClient *containerservice.ManagedClustersClient, spec *aksv1.AKSClusterConfigSpec) (err error) {
	future, err := clusterClient.Delete(ctx, spec.ResourceGroup, spec.ClusterName)
	if err != nil {
		return err
	}

	err = future.WaitForCompletionRef(ctx, clusterClient.Client)
	if err != nil {
		logrus.Errorf("can't get the AKS cluster create or update future response: %v", err)
		return err
	}

	logrus.Infof("Cluster %v removed successfully", spec.ClusterName)
	logrus.Infof("Cluster removal status %v", future.Status())

	return nil
}
