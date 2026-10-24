package main

import (
	"net/url"
	"regexp"
	"strings"
)

var metadataLine = regexp.MustCompile(`^\*{1,2}([^*:]+):?\*{1,2}:?\s*(.*)$`)
var markdownLink = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^\s)]+)\)`)
var deploymentApp = regexp.MustCompile(`(?i)^(?:new version successfully deployed|deployment failed|deployment started|deploying) for (.+)$`)

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func stringValue(v any) string    { s, _ := v.(string); return s }
func slackText(s string) string {
	s = markdownLink.ReplaceAllString(s, `<$2|$1>`)
	return strings.ReplaceAll(s, "**", "*")
}
func textBlock(kind, text string) map[string]any {
	return map[string]any{"type": kind, "text": text}
}

// Coolify 4.5 sends Mattermost-shaped attachments for any webhook hostname
// except hooks.slack.com. Older versions send Block Kit. Accept both formats.
func formatNotification(payload map[string]any) map[string]any {
	attachments, _ := payload["attachments"].([]any)
	if len(attachments) != 1 {
		return payload
	}
	a := object(attachments[0])
	title, description := stringValue(a["title"]), stringValue(a["text"])
	blocks, _ := a["blocks"].([]any)
	for _, raw := range blocks {
		b := object(raw)
		t := stringValue(object(b["text"])["text"])
		if b["type"] == "header" {
			title = t
		}
		if b["type"] == "section" && t != "" {
			if description != "" {
				description += "\n"
			}
			description += t
		}
	}
	if title == "" {
		title = stringValue(payload["text"])
	}
	if title == "" || description == "" {
		return payload
	}

	var details []string
	var fields []any
	var actions []any
	app := ""
	deployment := strings.Contains(strings.ToLower(title), "deploy")
	for _, line := range strings.Split(description, "\n") {
		trimmed := strings.TrimSpace(line)
		if deployment && app == "" {
			if m := deploymentApp.FindStringSubmatch(trimmed); len(m) == 2 {
				app = strings.TrimSuffix(m[1], ".")
				continue
			}
		}
		label, value := "", ""
		if m := metadataLine.FindStringSubmatch(trimmed); len(m) == 3 {
			label, value = strings.TrimSuffix(m[1], ":"), m[2]
		} else if strings.HasPrefix(trimmed, "Application URL: ") {
			label, value = "Application URL", strings.TrimPrefix(trimmed, "Application URL: ")
		} else if strings.HasPrefix(trimmed, "Preview URL: ") {
			label, value = "Application URL", strings.TrimPrefix(trimmed, "Preview URL: ")
		} else if link := strings.Trim(trimmed, "*"); strings.HasPrefix(link, "<") && strings.HasSuffix(link, "|Deployment Logs>") {
			label, value = "Deployment Logs", link
		}
		switch label {
		case "Project", "Environment":
			if value != "" {
				fields = append(fields, textBlock("mrkdwn", "*"+label+"*\n"+truncate(slackText(value), 1900)))
				continue
			}
		case "Deployment Logs", "Application URL":
			if target := linkURL(value); target != "" {
				name := "View deployment"
				if label == "Application URL" {
					name = "Open app"
				}
				actions = append(actions, map[string]any{"type": "button", "text": textBlock("plain_text", name), "url": target})
				continue
			}
		}
		details = append(details, slackText(line))
	}

	color := stringValue(a["color"])
	if color == "" {
		color = "#6366f1"
	}
	heading := title
	lower := strings.ToLower(title)
	switch {
	case strings.HasPrefix(lower, "pull request"):
		heading = "✅ " + title
	case strings.Contains(lower, "successfully deployed"):
		heading = "✅ Deployed"
	case strings.Contains(lower, "deploy") && strings.Contains(lower, "fail"):
		heading = "🚨 Deployment failed"
	case strings.Contains(lower, "fail"), strings.Contains(lower, "unreachable"), strings.Contains(lower, "stopped"), strings.Contains(lower, "high disk"), strings.Contains(lower, "action required"):
		heading = "🚨 " + title
	case strings.Contains(lower, "success"), strings.Contains(lower, "revived"), strings.Contains(lower, "reachable"):
		heading = "✅ " + title
	default:
		heading = "ℹ️ " + title
	}
	if app != "" {
		heading += " · " + app
	}
	formatted := []any{map[string]any{"type": "header", "text": textBlock("plain_text", truncate(heading, 150))}}
	if len(fields) > 0 {
		for len(fields) > 0 {
			n := min(len(fields), 10)
			formatted = append(formatted, map[string]any{"type": "section", "fields": fields[:n]})
			fields = fields[n:]
		}
	}
	text := strings.TrimSpace(strings.Join(details, "\n"))
	for text != "" {
		chunk := truncate(text, 2900)
		formatted = append(formatted, map[string]any{"type": "section", "text": textBlock("mrkdwn", chunk)})
		text = strings.TrimPrefix(text, chunk)
	}
	if len(actions) > 0 {
		formatted = append(formatted, map[string]any{"type": "actions", "elements": actions})
	}
	if len(formatted) > 45 || len(actions) > 5 {
		return payload
	}
	return map[string]any{
		"text":         truncate(heading, 300),
		"attachments":  []any{map[string]any{"color": color, "blocks": formatted}},
		"unfurl_links": false, "unfurl_media": false,
	}
}

func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return s
}

func linkURL(s string) string {
	s = strings.TrimSpace(s)
	if m := markdownLink.FindStringSubmatch(s); len(m) == 3 {
		s = m[2]
	}
	if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
		s = strings.SplitN(strings.Trim(s, "<>"), "|", 2)[0]
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || len(s) > 3000 {
		return ""
	}
	return s
}
