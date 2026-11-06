"""Required provider environment for every live S-case execution."""

import os
from collections.abc import Mapping


REQUIRED_PROVIDER_ENV = (
    "QINGQIU_TEST_LLM_BASE_URL",
    "QINGQIU_TEST_LLM_MODEL_ID",
    "QINGQIU_TEST_LLM_API_KEY",
    "QINGQIU_TEST_EMBEDDING_BASE_URL",
    "QINGQIU_TEST_EMBEDDING_MODEL_ID",
    "QINGQIU_TEST_EMBEDDING_API_KEY",
)


def missing_provider_env(environ: Mapping[str, str] | None = None) -> list[str]:
    """List absent or blank provider inputs without reading or exposing values."""
    source = os.environ if environ is None else environ
    return [name for name in REQUIRED_PROVIDER_ENV if not source.get(name, "").strip()]
