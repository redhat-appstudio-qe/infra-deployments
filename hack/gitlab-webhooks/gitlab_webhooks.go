// This module is a stop-gap solution for the rare case of when the smee server is
// migrated to another cluster. This is necessary because any tenant namespace created
// with KubeSaw (before its deprecation) has a webhook that points directly to the smee
// server and the server URL contains the hosting cluster name.
//
// To minimize down-time for these tenants, any namespace on the target cluster with a
// git provider URL of "gitlab.com" (external to Red Hat's gitlab.cee.redhat.com) will
// get a new webhook with the new smee server URL. Once functionality for this new smee
// server is verified, the old webhook will be deleted or each of the previous repositories.

package gitlab_webhooks

import (
	"fmt"

	"github.com/konflux-ci/build-service/pkg/git/gitlab"
)

// CreateExternalGitLabWebhooks creates a new webhook for each repository with a git provider URL
// of 'https://gitlab.com' and a webhook URL of 'webhook_url'.
func CreateExternalGitLabWebhooks(webhook_url string) error {
	repos, err := getExternalGitLabRepositories()
	if err != nil {
		fmt.Printf("error getting external GitLab repositories: %v\n", err)
		return fmt.Errorf("error getting external GitLab repositories: %v\n", err)
	}

	for _, repo := range repos {
		repoWebhookToken, err := getSecretToken(repo, "webhook")
		if err != nil {
			fmt.Printf("Warning:error getting webhook secret token for repository %s: %v\n", repo.Metadata.Name, err)
			continue
		}

		repoPacsToken, err := getSecretToken(repo, "pacs")
		if err != nil {
			fmt.Printf("Warning:error getting PaC secret token for repository %s: %v\n", repo.Metadata.Name, err)
			continue
		}

		gitlabClient, err := gitlab.NewGitlabClient(repoPacsToken, repo.Spec.GitProvider.URL)
		if err != nil {
			fmt.Printf("Warning: error creating GitLab client for repository %s: %v\n", repo.Metadata.Name, err)
			continue
		}

		err = gitlabClient.SetupPaCWebhook(repo.Spec.URL, webhook_url, repoWebhookToken)
		if err != nil {
			fmt.Printf("Warning: error creating a GitLab webhook with URL %s for repository %s: %v\n", webhook_url, repo.Metadata.Name, err)
		}
	}
	return nil
}

// DeleteExternalGitLabWebhooks deletes the webhook with URL 'webhook_url' for each
// repository with a git provider URL of 'https://gitlab.com'.
func DeleteExternalGitLabWebhooks(webhook_url string) error {
	repos, err := getExternalGitLabRepositories()
	if err != nil {
		fmt.Printf("error getting external GitLab repositories: %v\n", err)
		return fmt.Errorf("error getting external GitLab repositories: %v\n", err)
	}

	for _, repo := range repos {
		repoPacsToken, err := getSecretToken(repo, "pacs")
		if err != nil {
			fmt.Printf("Warning:error getting PaC secret token for repository %s: %v\n", repo.Metadata.Name, err)
			continue
		}

		gitlabClient, err := gitlab.NewGitlabClient(repoPacsToken, repo.Spec.GitProvider.URL)
		if err != nil {
			fmt.Printf("Warning: error creating GitLab client for repository %s: %v\n", repo.Metadata.Name, err)
			continue
		}

		err = gitlabClient.DeletePaCWebhook(repo.Spec.URL, webhook_url)
		if err != nil {
			fmt.Printf("Warning: error deleting the GitLab webhook for repository %s with URL %s: %v\n", repo.Metadata.Name, repo.Spec.URL, err)
		}
	}
	return nil
}
