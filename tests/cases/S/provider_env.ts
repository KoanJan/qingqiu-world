/** Required provider environment for every live S-case execution. */
export const REQUIRED_PROVIDER_ENV = [
  "QINGQIU_TEST_LLM_BASE_URL",
  "QINGQIU_TEST_LLM_MODEL_ID",
  "QINGQIU_TEST_LLM_API_KEY",
  "QINGQIU_TEST_EMBEDDING_BASE_URL",
  "QINGQIU_TEST_EMBEDDING_MODEL_ID",
  "QINGQIU_TEST_EMBEDDING_API_KEY",
];

/** List absent or blank provider inputs without reading or exposing values. */
export function missingProviderEnv(environ = process.env) {
  return REQUIRED_PROVIDER_ENV.filter((name) => !String(environ[name] ?? "").trim());
}
