package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const testSHA = "daa420206463e1920e56a235893a37aca3d46619"
const testDeploymentURL = "https://coolify.example/project/project123/environment/production123/application/app123/deployment/deployment123"

func deploymentPayload(target string) map[string]any {
	return formatNotification(map[string]any{"attachments": []any{map[string]any{
		"title": "New version successfully deployed", "color": "#00ff00",
		"text": "New version successfully deployed for sponsy-rest-api\n*Project:* Sponsy Metal\n*Environment:* production\n*<" + target + "|Deployment Logs>*",
	}}})
}

func TestEnrichmentUsesExactDeployment(t *testing.T) {
	var paths []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer read-only-test-token" {
			t.Error("missing API authentication")
		}
		switch r.URL.Path {
		case "/deployments/deployment123":
			fmt.Fprintf(w, `{"deployment_uuid":"deployment123","deployment_url":"%s","application_id":"24","commit":"%s","commit_message":"Merge pull request #114 from getsponsy/SPON-2213-sentry-db-perf\n\nFix slow queries","pull_request_id":0}`, testDeploymentURL, testSHA)
		case "/applications/app123":
			fmt.Fprint(w, `{"uuid":"app123","git_repository":"getsponsy/sponsy-monorepo","git_commit_sha":"newer-branch-head"}`)
		default:
			t.Errorf("unexpected API request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	e, err := newDeploymentEnricher("https://coolify.example", api.URL, "read-only-test-token", api.Client())
	if err != nil {
		t.Fatal(err)
	}
	p := deploymentPayload(testDeploymentURL)
	e.enrich(context.Background(), p)
	b, _ := json.Marshal(p)
	for _, want := range []string{"/commit/" + testSHA + "|daa4202", "/pull/114|#114", "Fix slow queries"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s in %s", want, b)
		}
	}
	if strings.Contains(string(b), "read-only-test-token") || strings.Contains(string(b), "newer-branch-head") {
		t.Fatal("leaked credential or used branch head")
	}
	if !reflect.DeepEqual(paths, []string{"/deployments/deployment123", "/applications/app123"}) {
		t.Fatal(paths)
	}
}

func TestEnrichmentFailureDoesNotBlockSlack(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer api.Close()
	sent := false
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sent = true; w.Write([]byte("ok")) }))
	defer slack.Close()
	e, _ := newDeploymentEnricher("https://coolify.example", api.URL, "read-only-test-token", api.Client())
	c := conductor{key: "test-key", main: Destination{url: slack.URL}, client: slack.Client(), enricher: e}
	raw, _ := json.Marshal(map[string]any{"attachments": []any{map[string]any{"title": "Deployment failed", "text": "Deployment failed for app\n*<" + testDeploymentURL + "|Deployment Logs>*"}}})
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("POST", "/?key=test-key", strings.NewReader(string(raw))))
	if !sent || w.Code != 200 {
		t.Fatalf("Slack delivery blocked: sent=%v status=%d", sent, w.Code)
	}
}

func TestEnrichmentRejectsUntrustedLinks(t *testing.T) {
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; t.Error("unexpected metadata request") }))
	defer api.Close()
	e, _ := newDeploymentEnricher("https://coolify.example", api.URL, "test-token", api.Client())
	for _, target := range []string{
		strings.Replace(testDeploymentURL, "coolify.example", "attacker.example", 1),
		strings.Replace(testDeploymentURL, "https://", "http://", 1),
		testDeploymentURL + "?redirect=elsewhere",
		strings.Replace(testDeploymentURL, "/deployment/deployment123", "/deployment/../../secrets", 1),
	} {
		p := deploymentPayload(target)
		before, _ := json.Marshal(p)
		e.enrich(context.Background(), p)
		after, _ := json.Marshal(p)
		if string(before) != string(after) {
			t.Fatalf("untrusted payload changed: %s", target)
		}
	}
	if calls != 0 {
		t.Fatal(calls)
	}
}

func TestMissingOrMismatchedDeploymentMetadata(t *testing.T) {
	for _, test := range []struct{ name, deployment, application string }{
		{"unknown revision", `{"deployment_uuid":"deployment123","commit":"HEAD"}`, `{}`},
		{"wrong deployment", `{"deployment_uuid":"different","commit":"` + testSHA + `"}`, `{}`},
		{"wrong stored application", `{"deployment_uuid":"deployment123","deployment_url":"/project/project123/environment/production123/application/different/deployment/deployment123","commit":"` + testSHA + `"}`, `{}`},
		{"wrong application response", `{"deployment_uuid":"deployment123","deployment_url":"` + testDeploymentURL + `","commit":"` + testSHA + `"}`, `{"uuid":"different","git_repository":"getsponsy/sponsy-monorepo"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/deployments/") {
					fmt.Fprint(w, test.deployment)
				} else {
					fmt.Fprint(w, test.application)
				}
			}))
			defer api.Close()
			e, _ := newDeploymentEnricher("https://coolify.example", api.URL, "test-token", api.Client())
			p := deploymentPayload(testDeploymentURL)
			before, _ := json.Marshal(p)
			e.enrich(context.Background(), p)
			after, _ := json.Marshal(p)
			if string(before) != string(after) {
				t.Fatal("invented commit details")
			}
		})
	}
}

func TestPullRequestNumbers(t *testing.T) {
	for _, test := range []struct {
		message string
		preview json.Number
		want    string
	}{
		{"Merge pull request #114 from getsponsy/fix", "0", "114"},
		{"Fix slow queries (#115)", "0", "115"},
		{"Fix issue #116", "0", ""},
		{"Ordinary commit\n\nReferences (#117)", "0", ""},
		{"Preview commit", "118", "118"},
	} {
		if got := pullRequestNumber(map[string]any{"pull_request_id": test.preview}, test.message); got != test.want {
			t.Fatalf("%q: got %s want %s", test.message, got, test.want)
		}
	}
}

func TestRepositoryNames(t *testing.T) {
	for _, input := range []string{"getsponsy/sponsy-monorepo", "https://github.com/getsponsy/sponsy-monorepo.git", "git@github.com:getsponsy/sponsy-monorepo.git"} {
		if repositoryName(input) != "getsponsy/sponsy-monorepo" {
			t.Fatal(input)
		}
	}
	for _, input := range []string{"https://gitlab.com/getsponsy/repo", "../secrets", "getsponsy/repo?token=secret"} {
		if repositoryName(input) != "" {
			t.Fatal(input)
		}
	}
}
