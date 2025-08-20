// This script is one half of a stop-gap solution for the rare case of when the smee server is
// migrated to another cluster. Any namespace on the target cluster with a git provider URL of
// "gitlab.com" (external to Red Hat's gitlab.cee.redhat.com) will get a new webhook with the new
// smee server URL.
//
// This is necessary because any tenant namespace created with KubeSaw (before its deprecation)
// has a webhook that points directly to the smee server and the server URL contains the hosting
// cluster.

package gitlab_webhooks

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/konflux-ci/build-service/pkg/git/gitlab"
	k8s "k8s.io/api/core/v1"
)

const (
	webhookSecretType = "webhook"
	pacsSecretType    = "pacs"
	gitLabComURL      = "https://gitlab.com"
)

type RepoSecret struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// Repository represents a single Kubernetes Repository resource.
type Repository struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		URL         string `json:"url"`
		Type        string `json:"type"`
		GitProvider struct {
			URL           string     `json:"url"`
			WebhookSecret RepoSecret `json:"webhook_secret"`
			PacsSecret    RepoSecret `json:"pacs_secret"`
		} `json:"git_provider"`
	} `json:"spec"`
}

// getSecretToken retrieves the one of the repository's secret tokens from the cluster.
func getSecretToken(repo Repository, secretType string) (string, error) {
	// Determine which secret to retrieve.
	var repoSecret RepoSecret
	if secretType == webhookSecretType {
		repoSecret = repo.Spec.GitProvider.WebhookSecret
	} else if secretType == pacsSecretType {
		repoSecret = repo.Spec.GitProvider.PacsSecret
	} else {
		return "", fmt.Errorf("invalid secret type: %s", secretType)
	}

	// Get the secret from the cluster.
	secretCmd := exec.Command("oc", "get", "secret", repoSecret.Name, "-n", repo.Metadata.Namespace, "-o", "json")
	secretOutput, err := secretCmd.Output()
	if err != nil {
		return "", fmt.Errorf("could not get secret '%s': %v\n", repoSecret.Name, err)
	}

	// Unmarshal the secret JSON.
	var secret k8s.Secret
	err = json.Unmarshal(secretOutput, &secret)
	if err != nil {
		return "", fmt.Errorf("error unmarshalling secret JSON for '%s': %v\n", repoSecret.Name, err)
	}

	// Retrieve and decode the secret token.
	secretKeyToken, ok := secret.Data[repoSecret.Key]
	if !ok {
		return "", fmt.Errorf("key '%s' not found in secret '%s'", repoSecret.Key, repoSecret.Name)
	}
	decodedToken, err := base64.StdEncoding.DecodeString(string(secretKeyToken))
	if err != nil {
		return "", fmt.Errorf("error decoding base64 data for secret '%s': %v\n", repoSecret.Name, err)
	}

	return string(decodedToken), nil
}

// create_external_gitlab_webhooks creates a new webhook for each repository with a git provider URL
// of 'https://gitlab.com' and a webhook URL of 'webhook_url'.
func create_external_gitlab_webhooks(webhook_url string) {
	fmt.Println("Searching for Repository resources with Git provider URL '" + gitLabComURL + "'...")

	// Get all repository resources with a git provider URL of 'https://gitlab.com'.
	cmd := exec.Command("bash", "-c", `oc get repository -A -o json | jq -c '.items[] | select(.spec.git_provider.url == "`+gitLabComURL+`")'`)
	output, err := cmd.Output()
	if err != nil {
		fmt.Printf("Error executing retrieving repository resources: %v\n", err)
		os.Exit(1)
	}

	// Process each line as a separate JSON object.
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	foundRepos := 0

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var repo Repository
		err := json.Unmarshal([]byte(line), &repo)
		if err != nil {
			fmt.Printf("Warning: Error unmarshalling repository JSON: %v\n", err)
			continue
		}

		foundRepos++
		fmt.Println("---")
		fmt.Printf("Found repository %s in namespace: %s\n", repo.Metadata.Name, repo.Metadata.Namespace)
		fmt.Printf("Secret name: %s\n", repo.Spec.GitProvider.WebhookSecret.Name)
		fmt.Printf("Secret key name: %s\n", repo.Spec.GitProvider.WebhookSecret.Key)

		repoWebhookToken, err := getSecretToken(repo, "webhook")
		if err != nil {
			fmt.Printf("error getting webhook secret token for repository %s: %v\n", repo.Metadata.Name, err)
			continue
		}
		repoPacsToken, err := getSecretToken(repo, "pacs")
		if err != nil {
			fmt.Printf("error getting PaC secret token for repository %s: %v\n", repo.Metadata.Name, err)
			continue
		}

		gitlabClient, err := gitlab.NewGitlabClient(repoPacsToken, repo.Spec.GitProvider.URL)
		if err != nil {
			fmt.Printf("error creating GitLab client for repository %s: %v\n", repo.Metadata.Name, err)
			continue
		}

		err = gitlabClient.SetupPaCWebhook(repo.Spec.URL, webhook_url, repoWebhookToken)
		if err != nil {
			fmt.Printf("error creating a GitLab webhook for repository %s: %v\n", repo.Metadata.Name, err)
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("error reading filtered repository output: %v\n", err)
		os.Exit(1)
	}

	if foundRepos == 0 {
		fmt.Println("No GitLab repositories found with URL '" + gitLabComURL + "'")
	} else {
		fmt.Printf("Found %d GitLab repositories.\n", foundRepos)
	}
}
