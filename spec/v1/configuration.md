# Future configuration and fleet boundaries

These schemas define only an interoperability boundary for a later
implementation. They do not create runtime endpoints or choose a database,
queue, cloud identity product, secret manager, or deployment provider.

## Immutable configuration bundle

`schemas/configuration-bundle.schema.json` defines an immutable bundle with:

- a monotonically increasing assignment generation;
- a SHA-256 checksum of the bundle `content`;
- a closed assignment and complete endpoint, subscription, retry, throttle, and
  payload-policy resources;
- sealed encrypted secret material with ciphertext checksum and key metadata,
  never plaintext secret values;
- Ed25519 key ID, signature value, and signing timestamp.

The content checksum is lowercase SHA-256 over RFC 8785 canonical JSON for the
`content` member. V1 configuration numeric fields are integers, avoiding
cross-runtime floating-point canonicalization. Every signed configuration
integer is at most JavaScript `Number.MAX_SAFE_INTEGER`
(`9007199254740991`). The Ed25519 message is the UTF-8 string below with no
final LF:

```text
hookship-configuration-bundle-v1
schema-version:<schemaVersion>
bundle-id:<bundleId>
generation:<generation>
content-sha256:<contentChecksum>
key-id:<signature.keyId>
signed-at:<signature.signedAt>
```

The signature therefore covers all content through its checksum. Bundle IDs,
generations, checksums, and signatures are immutable. Any resource change
requires a new generation.

The 64-byte Ed25519 signature uses canonical unpadded base64url: exactly 86
characters with a final character in `[AQgw]`. Verifiers MUST decode strictly
and require byte-for-byte equality with a fresh unpadded base64url re-encoding,
so alternate text with non-zero unused trailing bits is rejected.

Every resource reference in a subscription or endpoint resolves to an object
inside the same bundle. Sealed secrets include the ciphertext bytes, algorithm
identifier, ciphertext checksum, key ID, and key version needed by the
pre-provisioned local decryption identity. The bundle MUST NOT contain
plaintext credentials, HMAC secrets, private keys, or decrypted payload
material.

A fleet member MUST atomically persist the complete verified bundle bytes,
signature, trust-key ID, and local sealed-secret materialization as its
last-known-good artifact. Together with pre-provisioned signature trust roots
and decryption identity, that artifact MUST be sufficient to restart and apply
configuration while the control plane is unavailable. Applying a bundle MUST
NOT require dereferencing a control-plane resource.

Deterministic RFC 8785, SHA-256, and Ed25519 vectors are in
[`fixtures/configuration-vectors.json`](fixtures/configuration-vectors.json).

## Fleet registration report

`schemas/fleet-registration.schema.json` defines a minimal authenticated report:

- fleet member and pre-authorized assignment IDs;
- either mTLS certificate identity or generic workload identity;
- supported protocol/schema versions;
- one candidate bundle identity with its compatibility acknowledgement;
- state-specific `accepted`, `applied`, or `rejected` application reports;
- a last-known-good identity and materialization checksum when one exists.

Candidate and last-known-good generations use the same
`9007199254740991` maximum as configuration bundles.

The registrar maps authenticated identity to an assignment. A registering
member cannot self-select tenant or environment identity.

Before application, a member verifies schema compatibility, content checksum,
Ed25519 signature, assignment, all internal references, and monotonic
generation. A failed or incompatible candidate is reported only through the
`rejected` branch while the last-known-good bundle remains active. The
`rejectionCode` field is forbidden in accepted and applied reports. An applied
report's last-known-good identity MUST equal its candidate identity; executable
validation enforces equality that JSON Schema cannot express.
