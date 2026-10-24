package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

type conductor struct {
	key     string
	main    Destination
	mirrors []Destination
	client  *http.Client
}

func (c conductor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		w.Write([]byte("ok"))
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	want, got := sha256.Sum256([]byte(c.key)), sha256.Sum256([]byte(r.URL.Query().Get("key")))
	if c.key == "" || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid body", http.StatusBadRequest)
		}
		return
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil || payload == nil || len(payload) == 0 {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}
	out, err := json.Marshal(formatNotification(payload))
	if err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	status, retryAfter, err := c.send(r, c.main, out)
	if err != nil {
		log.Print("main Slack delivery failed")
		http.Error(w, "Slack delivery failed", http.StatusBadGateway)
		return
	}
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	if status < 200 || status >= 300 {
		// Coolify disables notification settings after 403/404/410. Keep
		// downstream failures retryable without disabling this endpoint.
		if status != http.StatusTooManyRequests {
			status = http.StatusBadGateway
		}
		log.Print("main Slack delivery rejected")
		http.Error(w, "Slack delivery rejected", status)
		return
	}
	seen := map[string]bool{c.main.url: true}
	for _, dest := range c.mirrors {
		if !seen[dest.url] && dest.Matches(string(body)) {
			seen[dest.url] = true
			code, _, err := c.send(r, dest, out)
			if err != nil || code < 200 || code >= 300 {
				log.Print("mirror Slack delivery failed")
			}
		}
	}
	log.Print("Slack notification delivered")
	w.Write([]byte("ok"))
}

func (c conductor) send(in *http.Request, dest Destination, body []byte) (int, string, error) {
	req, err := http.NewRequestWithContext(in.Context(), http.MethodPost, dest.url, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	// Do not forward the conductor key, incoming headers, or query to Slack.
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, resp.Header.Get("Retry-After"), nil
}

func main() {
	loadDestinations()
	key := os.Getenv("AUTH_KEY")
	if key == "" {
		log.Fatal("Missing AUTH_KEY environment variable")
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	server := &http.Server{
		Addr: ":8080", Handler: conductor{key, MainDestination, LoadedDestinations, client},
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	log.Print("Conductor listening on :8080")
	log.Fatal(server.ListenAndServe())
}
