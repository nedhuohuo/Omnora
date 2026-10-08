import type { MemberDirectoryEntry, MemberMount, MemberSearchResult, MemberSpace } from './types';

export type SpaceSelectionState = {
  activeSpaceId: string;
  mounts: MemberMount[];
  activeMountId: string;
  destinationSpaceId: string;
  destinationMounts: MemberMount[];
  destinationMountId: string;
  destinationPath: string;
  relativePath: string;
  entries: MemberDirectoryEntry[];
  trashItems: [];
  readOnly: boolean;
  loading: boolean;
  error: string;
  searchQuery: string;
  searchResults: MemberSearchResult[] | null;
  searchNextCursor: string;
  newFolderOpen: boolean;
  folderName: string;
  preview: null;
  previewFailed: boolean;
  previewText: null;
  previewBlobUrl: null;
  shareTarget: null;
  shareCreatedResult: null;
  renameTarget: null;
  renameValue: string;
  renameBusy: boolean;
  deleteTarget: null;
  deleteBusy: boolean;
  operationTarget: null;
  operationBusy: boolean;
  resumeSelectMode: boolean;
};

function preferredMountId(mounts: MemberMount[]): string {
  return mounts.find((mount) => mount.health === 'active')?.id ?? mounts[0]?.id ?? '';
}

export function createSpaceSelectionState(activeSpaceId: string, mounts: MemberMount[] = []): SpaceSelectionState {
  return {
    activeSpaceId,
    mounts,
    activeMountId: preferredMountId(mounts),
    destinationSpaceId: '',
    destinationMounts: [],
    destinationMountId: '',
    destinationPath: '.',
    relativePath: '.',
    entries: [],
    trashItems: [],
    readOnly: true,
    loading: false,
    error: '',
    searchQuery: '',
    searchResults: null,
    searchNextCursor: '',
    newFolderOpen: false,
    folderName: '',
    preview: null,
    previewFailed: false,
    previewText: null,
    previewBlobUrl: null,
    shareTarget: null,
    shareCreatedResult: null,
    renameTarget: null,
    renameValue: '',
    renameBusy: false,
    deleteTarget: null,
    deleteBusy: false,
    operationTarget: null,
    operationBusy: false,
    resumeSelectMode: false,
  };
}

export function createSpaceSelectionTransition<TTab extends string>(
  activeTab: TTab,
  activeSpaceId: string,
  mounts: MemberMount[] = [],
): SpaceSelectionState & { activeTab: TTab } {
  return {
    ...createSpaceSelectionState(activeSpaceId, mounts),
    activeTab,
  };
}

export function resolveAccessibleSpaceId(spaces: MemberSpace[], currentSpaceId: string): string {
  if (!currentSpaceId) return '';
  if (spaces.some((space) => space.id === currentSpaceId)) return currentSpaceId;
  return spaces.find((space) => space.type === 'personal')?.id
    ?? spaces.find((space) => space.type === 'shared')?.id
    ?? '';
}

export type SpaceRequestToken = {
  spaceId: string;
  generation: number;
};

export function createSpaceRequestToken(spaceId: string, generation: number): SpaceRequestToken {
  return { spaceId, generation };
}

export function isSpaceRequestCurrent(
  token: SpaceRequestToken,
  activeSpaceId: string,
  generation: number,
): boolean {
  return token.spaceId === activeSpaceId && token.generation === generation;
}

export type SpaceRequestCommitPhase = 'success' | 'error' | 'finally';

export function transitionSpaceRequestCommit<T extends object>(
  state: T,
  request: SpaceRequestToken,
  activeSpaceId: string,
  generation: number,
  commit: { phase: SpaceRequestCommitPhase; patch: Partial<T> },
): { state: T; phase: SpaceRequestCommitPhase; committed: boolean } {
  if (!isSpaceRequestCurrent(request, activeSpaceId, generation)) {
    return { state, phase: commit.phase, committed: false };
  }
  return {
    state: { ...state, ...commit.patch },
    phase: commit.phase,
    committed: true,
  };
}

export function nextSpaceRequestGeneration(
  currentSpaceId: string,
  nextSpaceId: string,
  generation: number,
): number {
  return currentSpaceId === nextSpaceId ? generation : generation + 1;
}

export function mergeMountSelectionState<T>(state: T, mounts: MemberMount[]): T & {
  mounts: MemberMount[];
  activeMountId: string;
} {
  return {
    ...state,
    mounts,
    activeMountId: preferredMountId(mounts),
  };
}

export function resolveTrashTabState(activeTab: string, supportsTrash: boolean) {
  const trashSelected = activeTab === 'trash';
  return {
    activeTab,
    loadTrash: trashSelected && supportsTrash,
    clearTrash: trashSelected && !supportsTrash,
  };
}
