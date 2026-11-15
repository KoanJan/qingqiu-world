/**
 * Product numeric enums mirrored from server/internal/model.
 *
 * These values are part of the persisted product contract. Cases compare them
 * against database rows, so every test module imports this single copy instead
 * of redeclaring local literals.
 */
export const PersonType = {
  AI: 1,
  HUMAN: 2,
} as const;

export const InteractionType = {
  RESPONSE: 2,
} as const;

export const EventType = {
  MESSAGE: 1,
  WORK_COMPLETED: 4,
} as const;

export const ActionType = {
  CHAT: 0,
  CREATE_ALARM: 4,
} as const;

export const ActionStatus = {
  IN_PROGRESS: 1,
} as const;

export const ActionEffectType = {
  MESSAGE: 2,
  SCHEDULED_EVENT: 3,
} as const;

export const WorkStatus = {
  RUNNING: 0,
} as const;

export const ScheduledEventStatus = {
  PENDING: 0,
} as const;

export const DocumentStatus = {
  READY: 2,
  FAILED: 3,
} as const;
