# Examples

Small, self-contained programs and configuration that demonstrate the delivery
vertical slice locally. They are **evaluation-only** and are not part of any
published artifact or the shipped binaries.

| Example | What it is |
| --- | --- |
| [`evaluation-client/`](evaluation-client) | Submits a protocol-v1 signed event and reads payload-free status |
| [`local-sink/`](local-sink) | A minimal receiver that verifies the Standard Webhooks signature and logs payload-free deliveries |
| [`compose/assignments.yaml`](compose/assignments.yaml) | Assignment/credential source for the durable Compose profiles |
| [`tools.Dockerfile`](tools.Dockerfile) | Builds the local sink image for the Compose profiles |

Run them via the Compose profiles or the scripted demo (see the top-level
[`README.md`](../README.md) and [`scripts/demo.sh`](../scripts/demo.sh)).

## Provenance

These examples were independently authored for this repository. They import no
third-party or private-platform source and add no runtime dependency to the
service. Any credential or signing secret that appears here is **well-known,
public evaluation test material** — never a production secret — and is
allowlisted for the secret scanner in [`../.gitleaks.toml`](../.gitleaks.toml).
The delivery targets use loopback/container addresses on a trusted local network
and require the explicit `HOOKSHIP_DELIVERY_ALLOW_*` opt-ins; they must never be
pointed at untrusted destinations. See [`../docs/provenance.md`](../docs/provenance.md).
