/** Verify the S-track provider prerequisite without using provider credentials. */
import assert from "node:assert/strict";
import { test } from "node:test";
import { REQUIRED_PROVIDER_ENV, missingProviderEnv } from "./provider_env.ts";

test("all six provider inputs are mandatory, including blank values", () => {
  const configured = Object.fromEntries(REQUIRED_PROVIDER_ENV.map((name) => [name, "present"]));
  assert.equal(REQUIRED_PROVIDER_ENV.length, 6);
  assert.deepEqual(missingProviderEnv(configured), []);
  for (const name of REQUIRED_PROVIDER_ENV) {
    const omitted = { ...configured };
    delete omitted[name];
    assert.deepEqual(missingProviderEnv(omitted), [name]);
    assert.deepEqual(missingProviderEnv({ ...configured, [name]: "  " }), [name]);
  }
});
