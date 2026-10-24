# Coolify Slack Conductor for Sponsy

A small webhook service based on [Hack Club's conductor](https://github.com/hackclub/coolify-slack-conductor). It formats Coolify notifications and forwards them to Sponsy's existing **#alerts** Slack incoming webhook.

Deployment notifications have a short app-specific heading, project and environment fields, and buttons to open the app and deployment logs. Other notifications retain their details and status color. Both Coolify's Block Kit payloads and its Mattermost payloads are supported. Coolify 4.5 uses the latter when the webhook host is not `hooks.slack.com`.

Deployment messages also show the deployed commit, its message, and a linked PR when the deployment is a preview or its commit message identifies a merge/squash PR. Commit links use the SHA recorded for that exact deployment. This also works for Docker image deployments when Coolify records their source commit. Image digests and the current branch head are never used as substitute commit IDs.

## Deployment

The Coolify project and application are named **Coolify Slack Conductor** and **coolify-slack-conductor**. They run on **localhost**, the regular Coolify host, using the repository's `Dockerfile`. They do not run on `sponsy-metal-1`.

- Branch: `main`, with manual deployments.
- Container port: `8080`; health endpoint: `GET /healthz`.
- Runtime limits: 128 MB memory and 0.25 CPU.
- HTTPS endpoint: `https://slack-conductor.37.27.245.178.sslip.io`.
- Runtime secrets in Coolify: `AUTH_KEY` and `WEBHOOK_MAIN_URL`.

For commit details, configure these runtime-only variables:

- `COOLIFY_INSTANCE_URL=https://coolify.getsponsy.com`
- `COOLIFY_API_URL=http://coolify:8080/api/v1` (the existing private Docker network)
- `COOLIFY_API_TOKEN`: a dedicated Root Team token with only the `read` ability.

The conductor validates the deployment link against the configured instance, fetches that deployment and its application, and checks that both records match before adding commit details. Metadata lookups have a three-second deadline. If the API is unavailable or the recorded commit is missing, the notification is still delivered without invented metadata. No GitHub credentials are required. PR links are only added for an explicit preview PR or a recognized merge/squash commit subject; an incidental issue reference does not count as a deployed PR.

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

The Docker build runs the tests. They cover both Coolify payload formats, notification details, deployment links, authentication, request size limits, upstream delivery failures, rate limits, duplicate destinations, exact deployment commit lookups, PR parsing, mismatched metadata, and continued Slack delivery during API failures.

The service accepts authenticated `POST /` requests up to 1 MB. It sends only JSON to Slack, without forwarding incoming headers or the authentication query. It does not log payloads, webhook URLs, or authentication keys. Outbound requests time out after ten seconds and do not follow redirects. Slack failures return an error to Coolify so its queue can retry; downstream 403/404/410 responses become 502 to avoid automatically disabling the conductor endpoint. Unknown message formats pass through unchanged.
