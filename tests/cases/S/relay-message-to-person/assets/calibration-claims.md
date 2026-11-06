# Calibration record and claims

Synthetic reference record, used only to calibrate an independent judge:

- Original user message: "请帮我给林小满带句话：明天上午十点在图书馆门口见，记得带上蓝色文件夹。", created at `2026-12-01 08:00:00 +08:00`.
- Declared target person: 林小满 (person_id=7).
- Target-session message from the subject Agent: "小满，明天上午十点图书馆门口见，记得带上蓝色文件夹。"
- The reference record contains only the original request and the target-session
  message; it does not include decision records, chat-action plans, or
  action_effects links.

Claims to judge against that record:

- C1: The target-session message conveys the 10 a.m. next-day meeting at the library entrance and the blue-folder reminder.
- C2: The target-session message asks the recipient to bring an umbrella.
- C3: The Agent delivered the relay through a new 1v1 session created with session_id=-1 and recipient_person_id=7.
