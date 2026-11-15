/** Create the Agent that will receive the reminder request. */
import { request } from "../../product_api.ts";
import { AgentRoute } from "../../product_routes.ts";

/** Product record name that the executor resolves after setup. */
export const REMINDER_AGENT_NAME = "Reminder keeper";

export async function prepareData(base: string, llmId: number): Promise<void> {
  await request(base, "POST", AgentRoute.CREATE, {
    name: REMINDER_AGENT_NAME,
    description: "Keeps the user's reminders at the requested time and content",
    character_settings: "Reliable and literal about requested reminder times and content.",
    llm_config_id: llmId,
  });
}
