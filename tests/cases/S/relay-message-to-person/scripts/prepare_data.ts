/** Create the people needed by the relay scenario before the request is sent. */
import { request } from "../../product_api.ts";
import { AgentRoute } from "../../product_routes.ts";
import type { RelayScenario } from "./case_contract.ts";

/** Product record name that the executor resolves after setup. */
export const RELAY_AGENT_NAME = "Relay assistant";

export async function prepareData(base: string, llmId: number, scenario: RelayScenario): Promise<void> {
  // The intended recipient: named by the scenario so the relay target is not
  // hard-coded into the test.
  await request(base, "POST", AgentRoute.CREATE, {
    name: scenario.target_person_name,
    description: "The person who should receive the relayed message.",
    character_settings: "Calm and literal; answer only when someone addresses you.",
    llm_config_id: llmId,
  });
  // The subject: an Agent asked to relay the user's message unchanged.
  await request(base, "POST", AgentRoute.CREATE, {
    name: RELAY_AGENT_NAME,
    description: "Relays the user's message to the named person without changing its key details.",
    character_settings: "Careful and reliable about names, times, places, and details.",
    llm_config_id: llmId,
  });
}
