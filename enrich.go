package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var deploymentPath = regexp.MustCompile(`^/project/[a-zA-Z0-9]+/(?:environment/[a-zA-Z0-9]+|[a-zA-Z0-9]+)/application/([a-zA-Z0-9]+)/deployment/([a-zA-Z0-9]+)$`)
var commitSHA = regexp.MustCompile(`(?i)^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
var githubRepo = regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`)
var mergedPR = regexp.MustCompile(`^Merge pull request #([1-9][0-9]*)\b`)
var squashPR = regexp.MustCompile(`\(#([1-9][0-9]*)\)\s*$`)
var positiveInteger = regexp.MustCompile(`^[1-9][0-9]*$`)

type deploymentEnricher struct {
	instance *url.URL
	apiURL   string
	token    string
	client   *http.Client
}

func newDeploymentEnricher(instanceURL, apiURL, token string, client *http.Client) (*deploymentEnricher, error) {
	if instanceURL == "" && apiURL == "" && token == "" {
		return nil, nil
	}
	instance, err := url.Parse(instanceURL)
	if err != nil || instance.Scheme != "https" || instance.Host == "" || instance.User != nil || instance.RawQuery != "" || instance.Fragment != "" || strings.Trim(instance.Path, "/") != "" {
		return nil, errors.New("COOLIFY_INSTANCE_URL must be the HTTPS origin of the Coolify dashboard")
	}
	api, err := url.Parse(apiURL)
	if err != nil || (api.Scheme != "http" && api.Scheme != "https") || api.Host == "" || api.User != nil || api.RawQuery != "" || api.Fragment != "" || token == "" {
		return nil, errors.New("COOLIFY_API_URL and COOLIFY_API_TOKEN are required for deployment enrichment")
	}
	return &deploymentEnricher{instance, strings.TrimRight(apiURL, "/"), token, client}, nil
}

func (e *deploymentEnricher) enrich(ctx context.Context, payload map[string]any) {
	if e == nil {
		return
	}
	attachments, _ := payload["attachments"].([]any)
	if len(attachments) != 1 {
		return
	}
	a := object(attachments[0])
	blocks, _ := a["blocks"].([]any)
	var appID, deploymentID string
	for _, raw := range blocks {
		b := object(raw)
		if b["type"] != "actions" {
			continue
		}
		actions, _ := b["elements"].([]any)
		for _, rawAction := range actions {
			action := object(rawAction)
			if object(action["text"])["text"] != "View deployment" {
				continue
			}
			u, err := url.Parse(stringValue(action["url"]))
			if err != nil || u.Scheme != e.instance.Scheme || u.Host != e.instance.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				continue
			}
			if match := deploymentPath.FindStringSubmatch(u.Path); len(match) == 3 {
				appID, deploymentID = match[1], match[2]
			}
		}
	}
	if deploymentID == "" {
		return
	}
	// Metadata is optional: a failed lookup must not prevent Slack delivery.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	deployment, err := e.get(ctx, "/deployments/"+deploymentID)
	if err != nil {
		log.Print("deployment metadata unavailable")
		return
	}
	if stringValue(deployment["deployment_uuid"]) != deploymentID || !commitSHA.MatchString(stringValue(deployment["commit"])) {
		return
	}
	// Read-only application responses hide numeric database IDs. Verify the
	// immutable application UUID in the deployment's stored URL instead.
	storedURL, err := url.Parse(stringValue(deployment["deployment_url"]))
	if err != nil {
		return
	}
	storedIDs := deploymentPath.FindStringSubmatch(storedURL.Path)
	if len(storedIDs) != 3 || storedIDs[1] != appID || storedIDs[2] != deploymentID {
		return
	}
	application, err := e.get(ctx, "/applications/"+appID)
	if err != nil {
		log.Print("application metadata unavailable")
		return
	}
	if stringValue(application["uuid"]) != appID {
		return
	}

	sha := stringValue(deployment["commit"])
	repo := repositoryName(stringValue(application["git_repository"]))
	commit := "`" + sha[:7] + "`"
	if repo != "" {
		commit = "<https://github.com/" + repo + "/commit/" + sha + "|" + sha[:7] + ">"
	}
	fields := []any{textBlock("mrkdwn", "*Commit*\n"+commit)}
	message := strings.TrimSpace(stringValue(deployment["commit_message"]))
	if pr := pullRequestNumber(deployment, message); pr != "" && repo != "" {
		fields = append(fields, textBlock("mrkdwn", "*Pull request*\n<https://github.com/"+repo+"/pull/"+pr+"|#"+pr+">"))
	}
	metadata := []any{map[string]any{"type": "section", "fields": fields}}
	if message != "" {
		// Plain text prevents commit subjects from becoming Slack mentions or links.
		metadata = append(metadata, map[string]any{"type": "section", "text": textBlock("plain_text", truncate(message, 1000))})
	}
	if len(blocks)+len(metadata) > 50 {
		return
	}
	for i, raw := range blocks {
		if object(raw)["type"] == "actions" {
			updated := append([]any{}, blocks[:i]...)
			updated = append(updated, metadata...)
			a["blocks"] = append(updated, blocks[i:]...)
			return
		}
	}
}

func (e *deploymentEnricher) get(ctx context.Context, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.apiURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Accept", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("metadata request failed")
	}
	var result map[string]any
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 2<<20))
	decoder.UseNumber()
	err = decoder.Decode(&result)
	return result, err
}

func repositoryName(repo string) string {
	repo = strings.TrimSuffix(strings.TrimSpace(repo), ".git")
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "git@github.com:", "ssh://git@github.com/"} {
		repo = strings.TrimPrefix(repo, prefix)
	}
	if !githubRepo.MatchString(repo) || strings.Contains(repo, "..") {
		return ""
	}
	return repo
}

func pullRequestNumber(deployment map[string]any, message string) string {
	if number, ok := deployment["pull_request_id"].(json.Number); ok && positiveInteger.MatchString(string(number)) {
		return string(number)
	}
	firstLine := strings.SplitN(message, "\n", 2)[0]
	for _, pattern := range []*regexp.Regexp{mergedPR, squashPR} {
		if match := pattern.FindStringSubmatch(firstLine); len(match) == 2 {
			return match[1]
		}
	}
	return ""
}
