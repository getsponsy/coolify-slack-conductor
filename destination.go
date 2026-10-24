package main

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"log"
	"net/url"
	"os"
	"regexp"
	"strings"
)

type ConfigItem struct {
	Name  string
	Regex []string
}
type Config struct {
	Destinations []ConfigItem
}

type Destination struct {
	url    string
	regexp []string
}

func (dest Destination) Matches(body string) bool {
	for _, regex := range dest.regexp {
		match, _ := regexp.MatchString(regex, body)
		if match {
			return true
		}
	}
	return false
}

var MainDestination = Destination{
	url: os.Getenv("WEBHOOK_MAIN_URL"),
}

var LoadedConfig Config
var LoadedDestinations []Destination
var ConfigIsLoaded = false

func loadDestinations() {
	if ConfigIsLoaded {
		return
	}

	validateDestination("WEBHOOK_MAIN_URL", MainDestination.url)
	config, err := os.ReadFile("config.yml")
	if err != nil {
		log.Fatal("Cannot read config.yml")
	}
	err = yaml.Unmarshal(config, &LoadedConfig)
	if err != nil {
		panic(err)
	}

	// Convert config into destinations
	for _, item := range LoadedConfig.Destinations {
		envVar := fmt.Sprintf("WEBHOOK_%s_URL", item.Name)
		dest := Destination{
			url:    os.Getenv(envVar),
			regexp: item.Regex,
		}
		validateDestination(envVar, dest.url)
		for _, pattern := range dest.regexp {
			if _, err := regexp.Compile(pattern); err != nil {
				log.Fatalf("Invalid destination pattern for %s", item.Name)
			}
		}

		LoadedDestinations = append(LoadedDestinations, dest)
	}

	ConfigIsLoaded = true
}

func validateDestination(name, value string) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host != "hooks.slack.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/services/") {
		log.Fatalf("%s must be an HTTPS Slack incoming webhook URL", name)
	}
}

func destinations(body string) []Destination {
	var results []Destination
	for _, dest := range LoadedDestinations {
		if dest.Matches(body) {
			results = append(results, dest)
		}
	}
	return results
}
