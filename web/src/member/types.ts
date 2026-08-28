import type { DirectoryChildrenPayload } from '../api';
import type { EffectivePermission } from './permissions';

export type MemberSpace = {
  id: string;
  type: string;
  name: string;
  role: 'viewer' | 'editor' | 'manager';
};

export type MemberEffectiveAccess = {
  effectivePermission?: EffectivePermission;
  readOnly?: boolean;
  canWrite?: boolean;
  canShare?: boolean;
};

export type MemberMount = MemberEffectiveAccess & {
  id: string;
  name: string;
  space: string;
  kind?: 'external' | 'managed';
  mode: 'read-write' | 'read-only';
  index: string;
  health: string;
  tone: string;
};

export type MemberDirectoryEntry = MemberEffectiveAccess & {
  mountId?: string;
  mountName?: string;
  name: string;
  relativePath: string;
  kind: 'dir' | 'file';
  size: number;
  modifiedAt: string;
  readOnly: boolean;
  previewKind: string;
};

export type MemberDirectoryListing = MemberEffectiveAccess & {
  relativePath: string;
  readOnly: boolean;
  entries: MemberDirectoryEntry[];
};

export type MemberSearchResult = MemberEffectiveAccess & {
  id: string;
  mountId: string;
  relativePath: string;
  name: string;
  kind: 'dir' | 'file';
  sizeBytes?: number;
  modifiedAt?: string;
  previewKind?: string;
};

export type TransferItem = {
  id: string;
  uploadId?: string;
  name: string;
  progress: number;
  state: 'queued' | 'uploading' | 'paused' | 'failed' | 'completed' | 'cancelled';
  detail?: string;
};

export function formatDirectoryChildren(payload: DirectoryChildrenPayload): MemberDirectoryListing {
  return {
    relativePath: payload.relativePath ?? payload.path ?? '.',
    effectivePermission: payload.effectivePermission,
    readOnly: payload.readOnly !== false,
    canWrite: payload.canWrite,
    canShare: payload.canShare,
    entries: (payload.entries ?? payload.items ?? []).map((entry) => ({
      ...entry,
      readOnly: entry.readOnly !== false,
    })),
  };
}
