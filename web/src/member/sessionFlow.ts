import type { SessionPayload } from '../api';

export type SessionState = 'signed-out' | 'enrollment' | 'ready';

export function stateForSession(session: SessionPayload): SessionState {
  if (session.purpose === 'totp_enrollment' || session.requiresTotpEnrollment) {
    return 'enrollment';
  }
  return session.purpose === 'full' ? 'ready' : 'signed-out';
}
