# Delivery-service source evaluation

Use this guide to evaluate HookShip's portable outbound webhook delivery data
plane on a local machine. The workflow uses synthetic assignments, a local
receiver, and public evaluation keys only. It does not require a registry,
cloud provider, managed platform, or production endpoint.

HookShip is pre-release. Record the exact commit you test because no binary or
container release exists yet.

## Time and prerequisites

Allow 45-90 minutes.

- Git
- Go 1.26.5 or newer
- Docker with Compose
- `curl`
- enough local resources to build the service and its evaluation containers

Use a machine where binding `127.0.0.1:8080` is safe and available.

## 1. Record the environment

```sh
git rev-parse HEAD
go version
docker version
docker compose version
```

Include these values in feedback.

## 2. Run the smallest delivery path

```sh
scripts/demo.sh memory
```

The script builds an ephemeral in-memory service and local sink, waits for
readiness, submits one protocol-v1 signed event, verifies a Standard
Webhooks-compatible delivery, reads payload-free status, and prints sanitized
sink evidence.

Expected result:

- readiness succeeds on loopback;
- the producer receives an acceptance receipt that states the selected
  profile's durability;
- the local sink verifies the delivery signature; and
- status contains no payload.

Tear the stack down when finished:

```sh
docker compose --profile memory down -v
```

## 3. Exercise durability and retry

If the memory path is clear, run:

```sh
scripts/demo.sh evaluation retry
```

This starts the PostgreSQL-backed evaluation profile. The sink rejects the
first two deliveries so the worker must classify the responses, retry, and
eventually deliver successfully without changing the producer event identity.

Tear it down:

```sh
docker compose --profile evaluation down -v
```

Optional profiles are `redis`, `kafka`, and `object-store`. Evaluate them only
when their additional local services are relevant to your deployment question.
PostgreSQL remains the source of truth for every durable profile.

## Evaluation checklist

- [ ] The distinction between API, worker, and all-in-one modes was clear.
- [ ] The selected backend profile and durability boundary were understandable.
- [ ] I observed accept, signed delivery, and payload-free status.
- [ ] Retry behavior was visible without exposing request payloads or secrets.
- [ ] Configuration failures and teardown instructions were actionable.
- [ ] The at-least-once delivery contract and downstream idempotency
      requirement were clear.
- [ ] I can state which deployment profile I would evaluate next, or why the
      service is not a fit.

## Safe feedback

Do not point the evaluation at production endpoints or replace the synthetic
assignment and payload with customer data. Never post signing secrets,
credentials, payload bodies, authorization headers, private URLs, object keys,
database URLs, or personal data.

- Submit cross-project experience feedback through the
  [early evaluation form](https://github.com/HookShip/.github/issues/new?template=evaluation_feedback.yml).
- File a reproducible delivery-service defect through the
  [bug report form](https://github.com/HookShip/hook-service/issues/new?template=bug_report.yml).
- Report vulnerabilities through [`SECURITY.md`](../SECURITY.md), never a
  public issue.

Useful feedback names the exact commit, environment, profile, elapsed time,
first confusing or failing step, sanitized error code or status, and the
decision you would make next.
