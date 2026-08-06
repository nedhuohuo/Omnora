import { describe, expect, it } from 'vitest';

import { mountDeletePolicy, mountSupportsTrash } from './types';

describe('mount file policies', () => {
  it('supports the recycle bin only for managed mounts', () => {
    expect(mountSupportsTrash({ kind: 'managed' })).toBe(true);
    expect(mountSupportsTrash({ kind: 'external' })).toBe(false);
    expect(mountSupportsTrash(undefined)).toBe(false);
  });

  it('requires permanent deletion only for known external mounts', () => {
    expect(mountDeletePolicy({ kind: 'managed' })).toBe('trash');
    expect(mountDeletePolicy({ kind: 'external' })).toBe('permanent');
    expect(mountDeletePolicy(undefined)).toBe('unavailable');
  });
});
