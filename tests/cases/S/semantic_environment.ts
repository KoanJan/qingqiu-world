/** Shared server preparation and provider setup for live semantic cases. */
import { IsolatedServer, SetupError } from "../tools.ts";
import type { RunRecord } from "../tools.ts";
import { request, ScenarioError } from "./product_api.ts";
import { SetupRoute } from "./product_routes.ts";

const SERVER_START_TIMEOUT_MS = 60_000;
export const SEMANTIC_SERVER_STOP_TIMEOUT_MS = 10_000;
/** Product identifiers are positive integers. */
const FIRST_VALID_ID = 1;

/** Keep the test user identity consistent across setup and read-only evidence checks. */
export function semanticUserName(caseName: string): string {
  return `${caseName} request user`;
}

/** Configure one product resource through the shared business-code check. */
async function configure(
  base: string,
  method: string,
  route: string,
  payload: unknown,
): Promise<Record<string, any>> {
  try {
    return await request<Record<string, any>>(base, method, route, payload);
  } catch (error) {
    if (error instanceof ScenarioError) throw new SetupError(error.message);
    throw error;
  }
}

/** Prepare the isolated process resources; the caller starts and owns the server. */
export async function prepareSemanticServer(root: string, record: RunRecord,
  maxModelCalls: number): Promise<IsolatedServer> {
  return IsolatedServer.prepare(root, record.dataDir, {
    COGNITIVE_MAX_REQUESTS: String(maxModelCalls),
    FOCUSED_WORK_MAX_ITERATIONS: String(maxModelCalls),
  }, SERVER_START_TIMEOUT_MS);
}

/** Install provider settings and restart because embedding services read them at startup. */
export async function configureAndRestartSemanticServer(server: IsolatedServer,
  record: RunRecord): Promise<number> {
  const { caseName } = record;
  // Create the isolated user identity that setup and evidence checks resolve.
  await configure(server.base, "PUT", SetupRoute.USER_PROFILE, { name: semanticUserName(caseName) });
  // Register the provider configs; the response ID is the only stable handle
  // the case can reuse when creating Agents.
  const llm = await configure(server.base, "POST", SetupRoute.LLM_CONFIGS, {
    name: `${caseName} test LLM`, model_id: process.env.QINGQIU_TEST_LLM_MODEL_ID,
    base_url: process.env.QINGQIU_TEST_LLM_BASE_URL, api_key: process.env.QINGQIU_TEST_LLM_API_KEY,
  });
  if (!Number.isSafeInteger(llm.id) || llm.id < FIRST_VALID_ID)
    throw new SetupError("LLM configuration response did not contain a valid id");
  await configure(server.base, "PUT", SetupRoute.EMBEDDING_CONFIG, {
    name: `${caseName} test embedding`, model_id: process.env.QINGQIU_TEST_EMBEDDING_MODEL_ID,
    base_url: process.env.QINGQIU_TEST_EMBEDDING_BASE_URL, api_key: process.env.QINGQIU_TEST_EMBEDDING_API_KEY,
  });
  // Embedding services read their configuration at startup, so the server must
  // restart before the case can index or retrieve sources.
  await server.stop(SEMANTIC_SERVER_STOP_TIMEOUT_MS);
  await server.start();
  return llm.id as number;
}
