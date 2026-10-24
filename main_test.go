package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCoolifyFormats(t *testing.T) {
	description := "New version successfully deployed for sponsy-web\nApplication URL: https://getsponsy.com\n\n**Project:** Sponsy Metal\n**Environment:** production\n**Deployment Logs:** [View logs](https://coolify.getsponsy.com/deployment/123)"
	for _, shape := range []string{"mattermost", "slack"} {
		t.Run(shape, func(t *testing.T) {
			a := map[string]any{"color": "#00ff00", "title": "New version successfully deployed", "text": description}
			if shape == "slack" {
				a = map[string]any{"color": "#00ff00", "blocks": []any{
					map[string]any{"type": "header", "text": textBlock("plain_text", "New version successfully deployed")},
					map[string]any{"type": "section", "text": textBlock("mrkdwn", strings.ReplaceAll(description, "**", "*"))},
				}}
			}
			got := formatNotification(map[string]any{"attachments": []any{a}})
			if got["text"] != "✅ Deployed · sponsy-web" {
				t.Fatalf("bad title: %v", got["text"])
			}
			b := object(got["attachments"].([]any)[0])["blocks"].([]any)
			if len(b) != 3 {
				t.Fatalf("expected header, fields and actions: %#v", b)
			}
			fields := object(b[1])["fields"].([]any)
			if object(fields[0])["text"] != "*Project*\nSponsy Metal" || object(fields[1])["text"] != "*Environment*\nproduction" {
				t.Fatalf("bad fields: %#v", fields)
			}
			actions := object(b[2])["elements"].([]any)
			if object(actions[0])["url"] != "https://getsponsy.com" || object(actions[1])["url"] != "https://coolify.getsponsy.com/deployment/123" {
				t.Fatalf("bad links: %#v", actions)
			}
		})
	}
}

func TestFailureDetailsAndUnknownPayloads(t *testing.T) {
	p := decode(t, `{"attachments":[{"title":"Docker cleanup job failed","color":"danger","text":"Docker cleanup job failed on 'localhost'!\n\nError: worker stopped before cleanup finished."}]}`)
	got := formatNotification(p)
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), "worker stopped before cleanup finished") || !strings.HasPrefix(got["text"].(string), "🚨") {
		t.Fatal(string(b))
	}
	for _, raw := range []string{`{"text":"Unknown notification","blocks":[{"type":"divider"}]}`, `{"attachments":[{"text":"one"},{"text":"two"}]}`} {
		p := decode(t, raw)
		if !reflect.DeepEqual(formatNotification(p), p) {
			t.Fatal("unknown payload changed")
		}
	}
}

func TestForwardingAndFailures(t *testing.T) {
	for _, status := range []int{200, 403, 404, 410, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("incoming secrets forwarded")
				}
				b, _ := io.ReadAll(r.Body)
				if !json.Valid(b) || strings.Contains(string(b), "private-key") {
					t.Error("invalid outgoing payload")
				}
				w.Header().Set("Retry-After", "10")
				w.WriteHeader(status)
			}))
			defer slack.Close()
			c := conductor{key: "private-key", main: Destination{url: slack.URL}, client: slack.Client()}
			r := httptest.NewRequest("POST", "/?key=private-key", strings.NewReader(`{"text":"test"}`))
			r.Header.Set("Authorization", "Bearer incoming-secret")
			r.Header.Set("Cookie", "session=incoming-secret")
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			want := status
			if status != 200 && status != 429 {
				want = 502
			}
			if calls != 1 || w.Code != want || w.Header().Get("Retry-After") != "10" {
				t.Fatalf("calls=%d status=%d", calls, w.Code)
			}
		})
	}
}

func TestRejectedRequestsNeverForward(t *testing.T) {
	c := conductor{key: "secret"}
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{"GET", "/healthz", "", 200},
		{"GET", "/", "", 405},
		{"POST", "/", `{}`, 401},
		{"POST", "/?key=wrong", `{}`, 401},
		{"POST", "/?key=secret", `not json`, 400},
		{"POST", "/?key=secret", `null`, 400},
		{"POST", "/?key=secret", `{}`, 400},
		{"POST", "/?key=secret", strings.Repeat("x", (1<<20)+1), 413},
	} {
		w := httptest.NewRecorder()
		c.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.code {
			t.Fatalf("%s: got %d want %d", tc.path, w.Code, tc.code)
		}
	}
}

func TestMirrorDoesNotDuplicateMain(t *testing.T) {
	calls := 0
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte("ok")) }))
	defer slack.Close()
	d := Destination{url: slack.URL, regexp: []string{"test"}}
	c := conductor{key: "secret", main: d, mirrors: []Destination{d}, client: slack.Client()}
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("POST", "/?key=secret", strings.NewReader(`{"text":"test"}`)))
	if w.Code != 200 || calls != 1 {
		t.Fatalf("calls=%d status=%d", calls, w.Code)
	}
}
