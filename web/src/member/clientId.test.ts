import { describe, expect, it, vi } from 'vitest';
import { createClientId } from './clientId';

describe('createClientId', () => {
  it('uses randomUUID when available', () => {
    vi.stubGlobal('crypto', {
      randomUUID: () => '11111111-2222-4333-8444-555555555555',
    });
    expect(createClientId()).toBe('11111111-2222-4333-8444-555555555555');
    vi.unstubAllGlobals();
  });

  it('falls back when randomUUID is missing in insecure contexts', () => {
    const bytes = Uint8Array.from({ length: 16 }, (_, index) => index);
    vi.stubGlobal('crypto', {
      getRandomValues: (target: Uint8Array) => {
        target.set(bytes);
        return target;
      },
    });
    expect(createClientId()).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    vi.unstubAllGlobals();
  });
});
