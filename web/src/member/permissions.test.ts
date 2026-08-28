import { describe, expect, it } from 'vitest';
import { effectiveAccessFrom, normalizeShareableMountId, shareableMounts } from './permissions';

describe('effective mount permissions', () => {
  it('accepts consistent editor and manager action flags', () => {
    expect(effectiveAccessFrom({ effectivePermission: 'editor', canWrite: true, canShare: false, readOnly: false })).toEqual({
      effectivePermission: 'editor',
      canWrite: true,
      canShare: false,
      readOnly: false,
    });
    expect(effectiveAccessFrom({ effectivePermission: 'manager', canWrite: true, canShare: true, readOnly: false })).toEqual({
      effectivePermission: 'manager',
      canWrite: true,
      canShare: true,
      readOnly: false,
    });
  });

  it('honors effective read-only even when canWrite is inconsistent', () => {
    expect(effectiveAccessFrom({ effectivePermission: 'manager', canWrite: true, canShare: true, readOnly: true })).toEqual({
      effectivePermission: 'manager',
      canWrite: false,
      canShare: true,
      readOnly: true,
    });
  });

  it('does not let viewer or editor roles claim stronger actions', () => {
    expect(effectiveAccessFrom({ effectivePermission: 'viewer', canWrite: true, canShare: true, readOnly: false })).toMatchObject({
      canWrite: false,
      canShare: false,
      readOnly: true,
    });
    expect(effectiveAccessFrom({ effectivePermission: 'editor', canWrite: true, canShare: true, readOnly: false })).toMatchObject({
      canWrite: true,
      canShare: false,
    });
  });

  it('fails closed for missing, partial, or malformed fields', () => {
    for (const value of [
      null,
      {},
      { effectivePermission: 'manager' },
      { effectivePermission: 'owner', canWrite: true, canShare: true, readOnly: false },
      { effectivePermission: 'manager', canWrite: 'true', canShare: 1, readOnly: false },
    ]) {
      expect(effectiveAccessFrom(value)).toMatchObject({ canWrite: false, canShare: false, readOnly: true });
    }
  });

  it('supports search flags that intentionally omit readOnly', () => {
    expect(effectiveAccessFrom({ effectivePermission: 'editor', canWrite: true, canShare: false })).toMatchObject({
      canWrite: true,
      canShare: false,
      readOnly: false,
    });
  });

  it('filters share targets strictly and normalizes stale selections', () => {
    const mounts = [
      { id: 'viewer', health: 'active', effectivePermission: 'viewer', canShare: true },
      { id: 'unavailable', health: 'unavailable', effectivePermission: 'manager', canShare: true },
      { id: 'shareable', health: 'active', effectivePermission: 'manager', canShare: true },
      { id: 'missing-flag', health: 'active', effectivePermission: 'manager' },
    ];

    expect(shareableMounts(mounts).map((mount) => mount.id)).toEqual(['shareable']);
    expect(normalizeShareableMountId(mounts, 'viewer')).toBe('shareable');
    expect(normalizeShareableMountId(mounts, 'shareable')).toBe('shareable');
    expect(normalizeShareableMountId(mounts.slice(0, 2), 'shareable')).toBe('');
  });
});
