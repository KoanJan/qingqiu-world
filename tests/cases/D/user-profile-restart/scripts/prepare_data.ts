/** Create the profile state that the restart test will later verify. */
import { CheckFailure, callJson, check } from "../../../tools.ts";
import type { CheckRecord, HttpCall } from "../../../tools.ts";

import { PersonType } from "../../../product_model.ts";

export const NAME = "profile-restart-user";
/** The single profile route exercised by both phases of this case. */
export const USER_PROFILE_PATH = "/api/user-profile";
/** Persisted Person type for a human profile, mirrored from the product model. */
export const PERSON_TYPE_HUMAN = PersonType.HUMAN;
const ORIGINAL_BIO = "Created before restart";
export const UPDATED_BIO = "Updated before restart";

export async function prepareData(base: string, responses: HttpCall[], checks: CheckRecord[]) {
  // A fresh data root must expose an empty profile.
  const initial = await callJson(base, "GET", USER_PROFILE_PATH, null, responses);
  check(checks, "initial profile response", initial.code, 0);
  check(checks, "fresh data has no user", initial.data, {});

  // Create the profile and record its identity for the post-restart check.
  const created = await callJson(base, "PUT", USER_PROFILE_PATH,
    { name: NAME, bio: ORIGINAL_BIO }, responses);
  check(checks, "create response", created.code, 0);
  const personId = created.data?.id;
  check(checks, "created profile name", created.data?.name, NAME);
  check(checks, "created profile bio", created.data?.bio, ORIGINAL_BIO);
  check(checks, "created profile type", created.data?.type, PERSON_TYPE_HUMAN);
  if (!Number.isInteger(personId) || personId <= 0)
    throw new CheckFailure("created profile has invalid ID: " + JSON.stringify(personId));

  // An update may change the bio but never the identity or name.
  const updated = await callJson(base, "PUT", USER_PROFILE_PATH,
    { name: "attempted-rename", bio: UPDATED_BIO }, responses);
  check(checks, "update response", updated.code, 0);
  check(checks, "user ID is stable", updated.data?.id, personId);
  check(checks, "user name is immutable", updated.data?.name, NAME);
  check(checks, "bio update took effect", updated.data?.bio, UPDATED_BIO);
}
