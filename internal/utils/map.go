package utils

import (
	"github.com/Azure/go-autorest/autorest/to"
	aksv1 "github.com/rancher/aks-operator/pkg/apis/aks.cattle.io/v1"
)

func BuildNodePoolMap(nodePools []aksv1.AKSNodePool) map[string]*aksv1.AKSNodePool {
	ret := make(map[string]*aksv1.AKSNodePool)
	for i := range nodePools {
		if nodePools[i].Name != nil {
			ret[*nodePools[i].Name] = &nodePools[i]
		}
	}
	return ret
}

func GetKeyValuesToUpdate(tags map[string]string, upstreamTags map[string]string) map[string]*string {
	if len(tags) == 0 {
		return nil
	}

	if len(upstreamTags) == 0 {
		return *to.StringMapPtr(tags)
	}

	updateTags := make(map[string]*string)
	for key, val := range tags {
		if upstreamTags[key] != val {
			updateTags[key] = to.StringPtr(val)
		}
	}

	if len(updateTags) == 0 {
		return nil
	}
	return updateTags
}

func GetKeysToDelete(tags map[string]string, upstreamTags map[string]string) []*string {
	if len(upstreamTags) == 0 {
		return nil
	}

	var updateUntags []*string
	for key, val := range upstreamTags {
		if len(tags) == 0 || tags[key] != val {
			updateUntags = append(updateUntags, to.StringPtr(key))
		}
	}

	if len(updateUntags) == 0 {
		return nil
	}
	return updateUntags
}