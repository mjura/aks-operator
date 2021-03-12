package aks

import (
	"fmt"

	"github.com/Azure/azure-sdk-for-go/services/containerservice/mgmt/2020-11-01/containerservice"
	"github.com/Azure/azure-sdk-for-go/services/resources/mgmt/2019-10-01/resources"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/adal"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/rancher/aks-operator/internal/utils"
	aksv1 "github.com/rancher/aks-operator/pkg/apis/aks.cattle.io/v1"
	wranglerv1 "github.com/rancher/wrangler/pkg/generated/controllers/core/v1"
)

type Credentials struct {
	AuthBaseURL *string
	BaseURL *string
	SubscriptionID string
	TenantID string
	ClientID string
	ClientSecret string
}

func NewResourceGroupClient(cred *Credentials) (*resources.GroupsClient, error) {
	authorizer, err := NewClientAuthorizer(cred)
	if err != nil {
		return nil, err
	}

	baseURL := cred.BaseURL
	if baseURL == nil {
		baseURL = to.StringPtr(azure.PublicCloud.ResourceManagerEndpoint)
	}

	subscriptionID := cred.SubscriptionID
	if subscriptionID == "" {
		return nil, fmt.Errorf("subscriptionId not specified")
	}

	client := resources.NewGroupsClientWithBaseURI(to.String(baseURL), subscriptionID)
	client.Authorizer = authorizer

	return &client, nil
}

func NewClusterClient(cred *Credentials) (*containerservice.ManagedClustersClient, error) {
	authorizer, err := NewClientAuthorizer(cred)
	if err != nil {
		return nil, err
	}

	baseURL := cred.BaseURL
	if baseURL == nil {
		baseURL = to.StringPtr(azure.PublicCloud.ResourceManagerEndpoint)
	}

	subscriptionID := cred.SubscriptionID
	if subscriptionID == "" {
		return nil, fmt.Errorf("subscriptionId not specified")
	}

	client := containerservice.NewManagedClustersClientWithBaseURI(to.String(baseURL), subscriptionID)
	client.Authorizer = authorizer

	return &client, nil
}

func NewAgentPoolClient(cred *Credentials) (*containerservice.AgentPoolsClient, error)  {
	authorizer, err := NewClientAuthorizer(cred)
	if err != nil {
		return nil, err
	}

	baseURL := cred.BaseURL
	if baseURL == nil {
		baseURL = to.StringPtr(azure.PublicCloud.ResourceManagerEndpoint)
	}

	subscriptionID := cred.SubscriptionID
	if subscriptionID == "" {
		return nil, fmt.Errorf("subscriptionId not specified")
	}

	agentProfile := containerservice.NewAgentPoolsClientWithBaseURI(to.String(baseURL), subscriptionID)
	agentProfile.Authorizer = authorizer

	return &agentProfile, nil
}

func NewClientAuthorizer(cred *Credentials) (autorest.Authorizer, error) {
	authBaseURL := cred.AuthBaseURL
	if authBaseURL == nil {
		authBaseURL = to.StringPtr(azure.PublicCloud.ActiveDirectoryEndpoint)
	}
	baseURL := cred.BaseURL
	if baseURL == nil {
		baseURL = to.StringPtr(azure.PublicCloud.ResourceManagerEndpoint)
	}

	oauthConfig, err := adal.NewOAuthConfig(to.String(authBaseURL), cred.TenantID)
	if err != nil {
		return nil, err
	}

	spToken, err := adal.NewServicePrincipalToken(*oauthConfig, cred.ClientID, cred.ClientSecret, to.String(baseURL))
	if err != nil {
		return nil, fmt.Errorf("couldn't authenticate to Azure cloud with error: %v", err)
	}

	return autorest.NewBearerAuthorizer(spToken), nil
}

func GetSecrets(secretsCache wranglerv1.SecretCache, spec *aksv1.AKSClusterConfigSpec) (*Credentials, error) {
	var cred Credentials

	secretName := spec.AzureCredentialSecret
	if secretName == "" {
		return nil, fmt.Errorf("secret name not provided")
	}

	ns, id := utils.ParseSecretName(secretName)
	if ns == "" {
		ns = "default"
	}

	secret, err := secretsCache.Get(ns, id)
	if err != nil {
		return nil, fmt.Errorf("couldn't find secret [%s] in namespace [%s]", id, ns)
	}

	clientIdBytes := secret.Data["clientId"]
	clientSecretBytes := secret.Data["clientSecret"]
	if clientIdBytes == nil || clientSecretBytes == nil {
		return nil, fmt.Errorf("invalid secret client data for Azure cloud")
	}
	cred.ClientID = string(clientIdBytes)
	cred.ClientSecret = string(clientSecretBytes)

	if spec.AuthBaseURL != nil {
		cred.AuthBaseURL = spec.AuthBaseURL
	}
	if spec.BaseURL != nil {
		cred.BaseURL = spec.BaseURL
	}

	if spec.SubscriptionID != "" {
		cred.SubscriptionID = spec.SubscriptionID
	}
	if spec.TenantID != "" {
		cred.TenantID = spec.TenantID
	}

	return &cred, nil
}
