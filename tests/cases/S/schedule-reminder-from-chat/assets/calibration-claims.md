# Calibration record and claims

Synthetic reference record, used only to calibrate an independent judge:

- Original user message: "5分钟后提醒我喝水。", created at
  `2026-12-01 08:00:00 +08:00`.
- Persisted reminder: `trigger_at` = `2026-12-01 08:05:25 +08:00`;
  `message` = "到点提醒对方喝水。".
- The reference record deliberately omits `action`, `action_content`, and
  `expression_instruction`; it records only the request time and persisted
  reminder note.

Claims to judge against that record:

- C1: The reminder asks the user to drink water.
- C2: The reminder asks the user to walk the dog.
- C3: The reminder used the direct-message fast path.
