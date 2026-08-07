import { describe, expect, it } from 'vitest';
import { ApiError, isReauthenticationRequired } from '../api';
import { stateForSession } from './sessionFlow';

describe('stateForSession', () => {
  it('maps enrollment and full sessions', () => {
    expect(stateForSession({ purpose: 'totp_enrollment' })).toBe('enrollment');
    expect(stateForSession({ requiresTotpEnrollment: true })).toBe('enrollment');
    expect(stateForSession({ purpose: 'full' })).toBe('ready');
    expect(stateForSession({})).toBe('signed-out');
  });
});

describe('isReauthenticationRequired', () => {
  it('detects only the reauthentication_required code', () => {
    expect(isReauthenticationRequired(new ApiError('x', 403, { error: { code: 'reauthentication_required' } }))).toBe(true);
    expect(isReauthenticationRequired(new ApiError('x', 403, { error: { code: 'csrf_required' } }))).toBe(false);
    expect(isReauthenticationRequired(new ApiError('x', 401, { error: { code: 'reauthentication_required' } }))).toBe(false);
  });
});
