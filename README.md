# Coolify Slack Conductor for Sponsy

A small webhook service based on [Hack Club's conductor](https://github.com/hackclub/coolify-slack-conductor). It formats Coolify notifications and forwards them to Sponsy's existing **#alerts** Slack incoming webhook.

Deployment notifications have a short app-specific heading, project and environment fields, and buttons to open the app and deployment logs. Other notifications retain their details and status color. Both Coolify's Block Kit payloads and its Mattermost payloads are supported. Coolify 4.5 uses the latter when the webhook host is not `hooks.slack.com`.

## Deployment

The Coolify project and application are named **Coolify Slack Conductor** and **coolify-slack-conductor**. They run on **localhost**, the regular Coolify host, using the repository's `Dockerfile`. They do not run on `sponsy-metal-1`.

- Branch: `main`, with manual deployments.
- Container port: `8080`; health endpoint: `GET /healthz`.
- Runtime limits: 128 MB memory and 0.25 CPU.
- HTTPS endpoint: `https://slack-conductor.37.27.245.178.sslip.io`.
- Runtime secrets in Coolify: `AUTH_KEY` and `WEBHOOK_MAIN_URL`.

`WEBHOOK_MAIN_URL` is the original Slack incoming webhook for #alerts. `AUTH_KEY` is a random shared secret. Neither variable should be enabled during the build or committed to Git.

In Coolify's Root Team Slack notification settings, use:

```text
https://slack-conductor.37.27.245.178.sslip.io/?key=<AUTH_KEY>
```

Keep the existing notification event switches. The conductor always delivers once to the main webhook. `config.yml` has no additional destinations, so notifications stay in the same channel. Optional regex destinations from upstream remain supported, using `WEBHOOK_<NAME>_URL` variables.

To roll back, restore the Slack notification webhook to the application's `WEBHOOK_MAIN_URL` value. This immediately bypasses the conductor. The original encrypted notification settings are also backed up on the Coolify host in `storage/app/conductor-slack-rollback.json` inside the Coolify container.

## Checks

```sh
go test ./...
go vet ./...
docker build -t coolify-slack-conductor .
```

The Docker build runs the tests. They cover both Coolify payload formats, notification details, deployment links, authentication, request size limits, upstream delivery failures, rate limits, and duplicate destinations.

The service accepts authenticated `POST /` requests up to 1 MB. It sends only JSON to Slack, without forwarding incoming headers or the authentication query. It does not log payloads, webhook URLs, or authentication keys. Outbound requests time out after ten seconds and do not follow redirects. Slack failures return an error to Coolify so its queue can retry; downstream 403/404/410 responses become 502 to avoid automatically disabling the conductor endpoint. Unknown message formats pass through unchanged.
