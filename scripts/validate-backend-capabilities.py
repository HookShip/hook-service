#!/usr/bin/env python3
"""Validate the authoritative backend capability fixtures against the schema.

This mirrors the protocol-v1 validator but targets the separately versioned
backend_capabilities/v1 contract under spec/backend-capabilities/v1. It is the
authoritative cross-repo contract; platform must consume byte/semantic copies of
these fixtures and schema.
"""

import json
from pathlib import Path

from jsonschema import Draft202012Validator

ROOT = Path(__file__).resolve().parents[1]
CONTRACT = ROOT / "spec" / "backend-capabilities" / "v1"
SCHEMA_PATH = CONTRACT / "schema.json"
FIXTURES = CONTRACT / "fixtures"

EXPECTED_PROFILES = {
    "memory",
    "postgres",
    "postgres-redis",
    "postgres-kafka",
    "kafka-native-ledger",
}


def main() -> None:
    schema = json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
    Draft202012Validator.check_schema(schema)
    validator = Draft202012Validator(schema)

    fixtures = sorted(FIXTURES.glob("*.json"))
    profiles = set()
    for fixture in fixtures:
        document = json.loads(fixture.read_text(encoding="utf-8"))
        validator.validate(document)
        if document["profile"] + ".json" != fixture.name:
            raise ValueError(f"{fixture.name}: profile does not match filename")
        profiles.add(document["profile"])

        # The schema is closed; an extra property must be rejected.
        with_extra = dict(document)
        with_extra["unexpected"] = True
        if validator.is_valid(with_extra):
            raise ValueError(f"{fixture.name}: schema accepted an unexpected property")

    if profiles != EXPECTED_PROFILES:
        raise ValueError(
            f"fixture profiles {sorted(profiles)} != expected {sorted(EXPECTED_PROFILES)}"
        )

    print(f"backend capability schema validation passed for {len(fixtures)} fixtures")


if __name__ == "__main__":
    main()
