package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/cmd"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/common/auth"
	"github.com/oracle/oci-go-sdk/v65/dns"
)

var GroupName = os.Getenv("GROUP_NAME")

// POD_NAMESPACE is injected via downwardAPI. The webhook reads OCI credential
// secrets from its own namespace rather than the challenge namespace so that a
// single secret can serve all ClusterIssuers without scattering credentials.
var PodNamespace = os.Getenv("POD_NAMESPACE")

func main() {
	if GroupName == "" {
		panic("GROUP_NAME must be specified")
	}

	cmd.RunWebhookServer(GroupName,
		&ociDNSProviderSolver{},
	)
}

type ociDNSProviderSolver struct {
	client *kubernetes.Clientset
}

type ociDNSProviderConfig struct {
	CompartmentOCID     string `json:"compartmentOCID"`
	OCIProfileSecretRef string `json:"ociProfileSecretName"`
}

func (c *ociDNSProviderSolver) Name() string {
	return "oci"
}

func (c *ociDNSProviderSolver) Present(ch *v1alpha1.ChallengeRequest) error {
	cfg, err := loadConfig(ch.Config)
	if err != nil {
		return err
	}

	klog.V(3).InfoS("call function Present", "namespace", ch.ResourceNamespace, "zone", ch.ResolvedZone, "fqdn", ch.ResolvedFQDN)

	ociDNSClient, err := c.ociDNSClient(&cfg, ch.ResourceNamespace)
	if err != nil {
		return fmt.Errorf("unable to initialize ociDNSClient: %v", err)
	}

	ctx := context.Background()

	_, err = ociDNSClient.PatchZoneRecords(ctx, patchRequest(ch, cfg.CompartmentOCID, dns.RecordOperationOperationAdd))
	if err != nil {
		return fmt.Errorf("can not create TXT record: %v", err)
	}
	return nil
}

func (c *ociDNSProviderSolver) CleanUp(ch *v1alpha1.ChallengeRequest) error {
	klog.V(3).InfoS("call function CleanUp", "namespace", ch.ResourceNamespace, "zone", ch.ResolvedZone, "fqdn", ch.ResolvedFQDN)
	cfg, err := loadConfig(ch.Config)
	if err != nil {
		return err
	}

	ociDNSClient, err := c.ociDNSClient(&cfg, ch.ResourceNamespace)
	if err != nil {
		return fmt.Errorf("unable to initialize ociDNSClient: %v", err)
	}

	ctx := context.Background()

	_, err = ociDNSClient.PatchZoneRecords(ctx, patchRequest(ch, cfg.CompartmentOCID, dns.RecordOperationOperationRemove))
	if err != nil {
		return fmt.Errorf("can not delete TXT record: %v", err)
	}
	return nil
}

func patchRequest(ch *v1alpha1.ChallengeRequest, compartmentOCID string, operation dns.RecordOperationOperationEnum) dns.PatchZoneRecordsRequest {
	zone := strings.TrimSuffix(ch.ResolvedZone, ".")
	domain := strings.TrimSuffix(ch.ResolvedFQDN, ".")
	rtype := "TXT"
	ttl := 60

	req := dns.PatchZoneRecordsRequest{
		ZoneNameOrId: &zone,
		PatchZoneRecordsDetails: dns.PatchZoneRecordsDetails{
			Items: []dns.RecordOperation{
				{
					Domain:    &domain,
					Rtype:     &rtype,
					Rdata:     &ch.Key,
					Ttl:       &ttl,
					Operation: operation,
				},
			},
		},
		RequestMetadata: getRequestMetadataWithDefaultRetryPolicy(),
	}
	if compartmentOCID != "" {
		req.CompartmentId = &compartmentOCID
	}
	return req
}

func (c *ociDNSProviderSolver) Initialize(kubeClientConfig *rest.Config, stopCh <-chan struct{}) error {
	cl, err := kubernetes.NewForConfig(kubeClientConfig)
	if err != nil {
		return err
	}

	c.client = cl

	return nil
}

func loadConfig(cfgJSON *extapi.JSON) (ociDNSProviderConfig, error) {
	cfg := ociDNSProviderConfig{}
	if cfgJSON == nil {
		return cfg, nil
	}
	if err := json.Unmarshal(cfgJSON.Raw, &cfg); err != nil {
		return cfg, fmt.Errorf("error decoding solver config: %v", err)
	}

	return cfg, nil
}

func (c *ociDNSProviderSolver) ociDNSClient(cfg *ociDNSProviderConfig, namespace string) (*dns.DnsClient, error) {
	var err2 error
	var configProvider common.ConfigurationProvider
	secretName := cfg.OCIProfileSecretRef

	// Look up the secret in the webhook's own namespace first (POD_NAMESPACE),
	// then fall back to the challenge namespace. This allows a single secret in
	// the webhook's namespace to serve all ClusterIssuers without scattering
	// credentials across namespaces, while remaining backwards compatible with
	// deployments that keep the secret in the challenge namespace.
	secretNamespace := PodNamespace
	if secretNamespace == "" {
		secretNamespace = namespace
	}
	sec, err := c.client.CoreV1().Secrets(secretNamespace).Get(context.Background(), secretName, metav1.GetOptions{})
	if err != nil && secretNamespace != namespace {
		klog.V(3).InfoS("Secret not found in pod namespace, trying challenge namespace", "secret", secretName, "podNamespace", secretNamespace, "challengeNamespace", namespace)
		secretNamespace = namespace
		sec, err = c.client.CoreV1().Secrets(secretNamespace).Get(context.Background(), secretName, metav1.GetOptions{})
	}

	klog.V(3).InfoS("Trying to load oci profile from secret", "secret", secretName, "namespace", secretNamespace)
	if err != nil {
		klog.V(3).InfoS("Did not find a secret for oci configuration. Using Workload Identity auth.")
		configProvider, err2 = auth.OkeWorkloadIdentityConfigurationProvider()
		if err2 != nil {
			return nil, fmt.Errorf("unable to get secret `%s/%s` and Workload Identity auth also failed; %v; %v", secretNamespace, secretName, err, err2)
		}
	} else {
		tenancy, err := stringFromSecretData(&sec.Data, "tenancy")
		if err != nil {
			return nil, fmt.Errorf("unable to get tenancy from secret `%s/%s`; %v", secretNamespace, secretName, err)
		}

		user, err := stringFromSecretData(&sec.Data, "user")
		if err != nil {
			return nil, fmt.Errorf("unable to get user from secret `%s/%s`; %v", secretNamespace, secretName, err)
		}

		region, err := stringFromSecretData(&sec.Data, "region")
		if err != nil {
			return nil, fmt.Errorf("unable to get region from secret `%s/%s`; %v", secretNamespace, secretName, err)
		}

		fingerprint, err := stringFromSecretData(&sec.Data, "fingerprint")
		if err != nil {
			return nil, fmt.Errorf("unable to get fingerprint from secret `%s/%s`; %v", secretNamespace, secretName, err)
		}

		privateKey, err := stringFromSecretData(&sec.Data, "privateKey")
		if err != nil {
			return nil, fmt.Errorf("unable to get privateKey from secret `%s/%s`; %v", secretNamespace, secretName, err)
		}

		privateKeyPassphrase, err := stringFromSecretData(&sec.Data, "privateKeyPassphrase")
		if err != nil {
			return nil, fmt.Errorf("unable to get privateKeyPassphrase from secret `%s/%s`; %v", secretNamespace, secretName, err)
		}

		configProvider = common.NewRawConfigurationProvider(tenancy, user, region, fingerprint, privateKey, &privateKeyPassphrase)
	}

	dnsClient, err := dns.NewDnsClientWithConfigurationProvider(configProvider)
	if err != nil {
		return nil, err
	}
	return &dnsClient, nil
}

func stringFromSecretData(secretData *map[string][]byte, key string) (string, error) {
	bytes, ok := (*secretData)[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret data", key)
	}
	return string(bytes), nil
}

func getRequestMetadataWithDefaultRetryPolicy() common.RequestMetadata {
	return common.RequestMetadata{
		RetryPolicy: getDefaultRetryPolicy(),
	}
}

func getDefaultRetryPolicy() *common.RetryPolicy {
	attempts := uint(10)

	retryOnAllNon200ResponseCodes := func(r common.OCIOperationResponse) bool {
		response := r.Response.HTTPResponse()
		retry := !((r.Error == nil && 199 < response.StatusCode && response.StatusCode < 300) || (400 <= response.StatusCode && response.StatusCode <= 407) || (411 <= response.StatusCode && response.StatusCode <= 417))
		if retry {
			klog.V(3).InfoS("retrying", "request method", response.Request.Method, "request", response.Request.URL.String(), "response", response.Status)
		}
		return retry
	}
	return getExponentialBackoffRetryPolicy(attempts, retryOnAllNon200ResponseCodes)
}

func getExponentialBackoffRetryPolicy(n uint, fn func(r common.OCIOperationResponse) bool) *common.RetryPolicy {
	exponentialBackoff := func(r common.OCIOperationResponse) time.Duration {
		response := r.Response.HTTPResponse()
		duration := time.Duration(math.Pow(float64(2), float64(r.AttemptNumber-1))) * time.Second
		klog.V(3).InfoS("backing off to retry", "duration", duration, "request method", response.Request.Method, "request", response.Request.URL.String(), "attempts", r.AttemptNumber)
		return duration
	}
	policy := common.NewRetryPolicy(n, fn, exponentialBackoff)
	return &policy
}
