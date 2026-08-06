import type { DirectoryChildrenPayload } from '../api';

export type MemberSpace = {
  id: string;
  type: string;
  name: string;
  role: 'viewer' | 'editor' | 'manager';
};

export type MemberMount = {
  id: string;
  name: string;
  space: string;
  kind?: 'external' | 'managed';
  mode: 'read-write' | 'read-only';
  index: string;
  health: string;
  tone: string;
};

export type MountDeletePolicy = 'trash' | 'permanent';

export function mountSupportsTrash(mount: Pick<MemberMount, 'kind'> | null | undefined) {
  return mount?.kind === 'managed';
}

export function mountDeletePolicy(mount: Pick<MemberMount, 'kind'> | null | undefined): MountDeletePolicy {
  return mountSupportsTrash(mount) ? 'trash' : 'permanent';
}

export type MemberDirectoryEntry = {
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

export type MemberDirectoryListing = {
  relativePath: string;
  readOnly: boolean;
  entries: MemberDirectoryEntry[];
};

export type MemberSearchResult = {
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
    readOnly: Boolean(payload.readOnly),
    entries: payload.entries ?? payload.items ?? [],
  };
}
