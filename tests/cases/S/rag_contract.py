"""Shared scenario contract for cases using the scripted RAG workflow."""

from pathlib import Path

from base_contracts import ScenarioBase


CHAT_PATH = "/api/chat/new"
MESSAGES_PATH = "/api/messages"
ACTIVITIES_PATH = "/api/sessions"
SUBJECT_ENTRY = (
    f"POST {CHAT_PATH}; GET {MESSAGES_PATH}/:id; "
    f"GET {ACTIVITIES_PATH}/:id/activities"
)
LIMIT_KEYS = frozenset({
    "index_timeout_seconds", "answer_timeout_seconds", "max_model_calls",
})


class Scenario(ScenarioBase):
    """Require precisely the limits consumed by the shared RAG runner."""

    def validate_context(self, path: Path, case_dir: Path) -> Path:
        fixture = super().validate_context(path, case_dir)
        if set(self.limits) != LIMIT_KEYS:
            raise ValueError(f"RAG limits must be exactly {sorted(LIMIT_KEYS)}")
        return fixture
