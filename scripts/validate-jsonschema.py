#!/usr/bin/env python3

import base64
import copy
import json
from pathlib import Path

from jsonschema import Draft202012Validator
from referencing import Registry, Resource


ROOT = Path(__file__).resolve().parents[1]
SPEC = ROOT / "spec" / "v1"


def load(relative_path: str):
    return json.loads((SPEC / relative_path).read_text(encoding="utf-8"))


registry_document = load("schema-registry.json")
resources = []
schemas = {}
for entry in registry_document["resources"]:
    schema = load(entry["path"])
    if schema["$id"] != entry["id"]:
        raise ValueError(f"{entry['path']}: registry ID does not match $id")
    Draft202012Validator.check_schema(schema)
    schemas[entry["id"]] = schema
    resources.append((entry["id"], Resource.from_contents(schema)))

registry = Registry().with_resources(resources)

fixture_schemas = {
    "fixtures/accepted.json": "urn:hookship:spec:v1:schema:golden-http-outcome",
    "fixtures/conflict.json": "urn:hookship:spec:v1:schema:golden-http-outcome",
    "fixtures/duplicate.json": "urn:hookship:spec:v1:schema:golden-http-outcome",
    "fixtures/rejected.json": "urn:hookship:spec:v1:schema:golden-http-outcome",
    "fixtures/signing-vectors.json": "urn:hookship:spec:v1:schema:signing-vectors",
    "fixtures/status.json": "urn:hookship:spec:v1:schema:event-ingest-status",
    "fixtures/timestamp-vectors.json": "urn:hookship:spec:v1:schema:timestamp-vectors",
    "fixtures/configuration-vectors.json": "urn:hookship:spec:v1:schema:configuration-vectors",
    "fixtures/fleet-accepted.json": "urn:hookship:spec:v1:schema:fleet-registration",
    "fixtures/fleet-applied.json": "urn:hookship:spec:v1:schema:fleet-registration",
    "fixtures/fleet-rejected.json": "urn:hookship:spec:v1:schema:fleet-registration",
    "fixtures/not-found-foreign.json": "urn:hookship:spec:v1:schema:problem-details",
    "fixtures/not-found-unknown.json": "urn:hookship:spec:v1:schema:problem-details",
    "fixtures/problem-responses.json": "urn:hookship:spec:v1:schema:problem-response-vectors",
}

for fixture_path, schema_id in fixture_schemas.items():
    Draft202012Validator(
        schemas[schema_id],
        registry=registry,
    ).validate(load(fixture_path))


def replace_pointer(document, pointer, value):
    parts = [
        part.replace("~1", "/").replace("~0", "~")
        for part in pointer.removeprefix("/").split("/")
    ]
    current = document
    for part in parts[:-1]:
        current = current[int(part)] if isinstance(current, list) else current[part]
    final = parts[-1]
    if isinstance(current, list):
        current[int(final)] = value
    else:
        current[final] = value


def canonical_raw_base64url(value, size):
    padding = "=" * ((4 - len(value) % 4) % 4)
    try:
        decoded = base64.urlsafe_b64decode(value + padding)
    except ValueError:
        return False
    encoded = base64.urlsafe_b64encode(decoded).rstrip(b"=").decode("ascii")
    return len(decoded) == size and encoded == value


problem_vectors = load("fixtures/problem-responses.json")["responses"]
for entry in problem_vectors:
    validator = Draft202012Validator(
        {"$ref": entry["schemaRef"]},
        registry=registry,
    )
    validator.validate(entry["body"])
    if validator.is_valid({}):
        raise ValueError(f"{entry['schemaRef']} accepted an empty object")
    with_extra = dict(entry["body"])
    with_extra["unexpected"] = True
    if validator.is_valid(with_extra):
        raise ValueError(f"{entry['schemaRef']} accepted an extra property")

configuration_vectors = load("fixtures/configuration-vectors.json")
configuration_validator = Draft202012Validator(
    schemas["urn:hookship:spec:v1:schema:configuration-bundle"],
    registry=registry,
)
for entry in configuration_vectors["integerBoundaryCases"]:
    candidate = copy.deepcopy(configuration_vectors["bundle"])
    replace_pointer(candidate, entry["jsonPointer"], entry["value"])
    if configuration_validator.is_valid(candidate) != entry["valid"]:
        raise ValueError(f"configuration integer boundary failed: {entry['name']}")

fleet_validator = Draft202012Validator(
    schemas["urn:hookship:spec:v1:schema:fleet-registration"],
    registry=registry,
)
for entry in configuration_vectors["fleetGenerationBoundaryCases"]:
    candidate = load(f"fixtures/{entry['fixture']}")
    replace_pointer(candidate, entry["jsonPointer"], entry["value"])
    if fleet_validator.is_valid(candidate) != entry["valid"]:
        raise ValueError(f"fleet generation boundary failed: {entry['name']}")

signing_vectors = load("fixtures/signing-vectors.json")
for entry in signing_vectors["signatureEncodingCases"]:
    if canonical_raw_base64url(entry["value"], 32) != entry["valid"]:
        raise ValueError(f"HMAC base64url boundary failed: {entry['name']}")
signing_alias = copy.deepcopy(signing_vectors)
signing_alias["vectors"][0]["expectedSignature"] = signing_vectors[
    "signatureEncodingCases"
][1]["value"]
signing_validator = Draft202012Validator(
    schemas["urn:hookship:spec:v1:schema:signing-vectors"],
    registry=registry,
)
if signing_validator.is_valid(signing_alias):
    raise ValueError("signing schema accepted non-canonical HMAC signature")
if not canonical_raw_base64url(
    configuration_vectors["bundle"]["signature"]["value"],
    64,
):
    raise ValueError("configuration signature is not canonical base64url")
for mutation in configuration_vectors["mutations"]:
    if mutation["expectedFailure"] != "signature_encoding":
        continue
    original = configuration_vectors["bundle"]["signature"]["value"]
    alias = mutation["replacement"]
    padding = "=" * ((4 - len(original) % 4) % 4)
    alias_padding = "=" * ((4 - len(alias) % 4) % 4)
    if base64.urlsafe_b64decode(original + padding) != base64.urlsafe_b64decode(
        alias + alias_padding
    ):
        raise ValueError("configuration alias does not decode to original bytes")
    if canonical_raw_base64url(alias, 64):
        raise ValueError("configuration signature alias was accepted")
    candidate = copy.deepcopy(configuration_vectors["bundle"])
    replace_pointer(candidate, mutation["jsonPointer"], alias)
    if configuration_validator.is_valid(candidate):
        raise ValueError("configuration schema accepted signature alias")

print("Draft 2020-12 schema validation passed")
