import type { Session } from './types';

export function isSession(value: unknown): value is Session {
  return typeof value === 'object' && value !== null
    && 'token' in value && typeof value.token === 'string' && value.token.length > 0
    && 'actorId' in value && typeof value.actorId === 'string' && value.actorId.length > 0;
}
