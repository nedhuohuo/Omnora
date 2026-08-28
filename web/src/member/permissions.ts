export type EffectivePermission = 'viewer' | 'editor' | 'manager';

export type EffectiveAccess = {
  effectivePermission: EffectivePermission | null;
  canWrite: boolean;
  canShare: boolean;
  readOnly: boolean;
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isEffectivePermission(value: unknown): value is EffectivePermission {
  return value === 'viewer' || value === 'editor' || value === 'manager';
}

export function effectiveAccessFrom(value: unknown): EffectiveAccess {
  const source = isRecord(value) ? value : {};
  const effectivePermission = isEffectivePermission(source.effectivePermission)
    ? source.effectivePermission
    : null;
  const permissionCanWrite = effectivePermission === 'editor' || effectivePermission === 'manager';
  const canWrite = permissionCanWrite && source.canWrite === true && source.readOnly !== true;
  const canShare = effectivePermission === 'manager' && source.canShare === true;

  return {
    effectivePermission,
    canWrite,
    canShare,
    readOnly: source.readOnly === true || !canWrite,
  };
}

export function shareableMounts<T>(mounts: readonly T[]): T[] {
  return mounts.filter((mount) => isRecord(mount) && mount.health === 'active' && effectiveAccessFrom(mount).canShare);
}

export function normalizeShareableMountId<T extends { id: string }>(mounts: readonly T[], currentMountId: string): string {
  const available = shareableMounts(mounts);
  return available.some((mount) => mount.id === currentMountId) ? currentMountId : (available[0]?.id ?? '');
}
