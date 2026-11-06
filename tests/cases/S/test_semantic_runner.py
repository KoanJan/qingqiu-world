"""Check that RAG capture distinguishes missing traces from empty retrievals."""

import json
from pathlib import Path
import tempfile
import unittest

from semantic_runner import check_db_evidence


class DBEvidenceChecks(unittest.TestCase):
    def test_missing_trace_does_not_claim_a_completed_empty_lookup(self):
        with tempfile.TemporaryDirectory() as temp:
            fixture = Path(temp) / "source.md"
            fixture.write_text("source passage")
            session = {
                "document": {"id": 7, "active_revision_id": 3},
                "kb_id": 1,
                "agent_id": 2,
            }
            evidence = {
                "document": {"knowledge_base_id": 1, "status": 2,
                             "active_revision_id": 3, "file_path": str(fixture)},
                "kb_access": [{"person_id": 2, "kb_id": 1}],
                "active_chunks": [{"id": 9}],
                "works": [],
                "kb_usage_traces": [],
            }

            failures = check_db_evidence(evidence, session, fixture)
            self.assertEqual(failures, [
                "record: no persisted work records for the chat session",
                "record: no KB retrieval trace was recorded for the chat session",
            ])

            evidence["works"] = [{"id": 4}]
            evidence["kb_usage_traces"] = [{
                "id": 5,
                "authorized_kb_ids_json": json.dumps([1]),
                "evidence_handles_json": "[]",
                "returned_evidence_count": 0,
            }]
            self.assertEqual(check_db_evidence(evidence, session, fixture), [
                "retrieval: KB retrieval returned no source evidence",
            ])

            evidence["kb_usage_traces"][0]["returned_evidence_count"] = 1
            self.assertEqual(check_db_evidence(evidence, session, fixture), [])


if __name__ == "__main__":
    unittest.main()
