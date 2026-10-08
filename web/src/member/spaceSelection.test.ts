import { describe, expect, it } from 'vitest';

import type { MemberMount, MemberSpace } from './types';
import {
  createSpaceRequestToken,
  createSpaceSelectionState,
  createSpaceSelectionTransition,
  isSpaceRequestCurrent,
  mergeMountSelectionState,
  nextSpaceRequestGeneration,
  resolveAccessibleSpaceId,
  resolveTrashTabState,
  transitionSpaceRequestCommit,
} from './spaceSelection';

const mounts: MemberMount[] = [
  { id: 'warming', name: 'Warming', space: 'shared', kind: 'managed', mode: 'read-write', index: 'idle', health: 'warming', tone: 'neutral' },
  { id: 'active-first', name: 'Active first', space: 'shared', kind: 'managed', mode: 'read-write', index: 'ready', health: 'active', tone: 'success' },
  { id: 'active-second', name: 'Active second', space: 'shared', kind: 'external', mode: 'read-only', index: 'ready', health: 'active', tone: 'success' },
];

describe('member space selection', () => {
  it('constructs a root selection for the new space and clears dependent transient state', () => {
    const selection = createSpaceSelectionState('shared-team', mounts);

    expect(selection).toEqual({
      activeSpaceId: 'shared-team',
      mounts,
      activeMountId: 'active-first',
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
    });
    expect(selection).not.toHaveProperty('activeTab');
    expect(selection).not.toHaveProperty('locale');
    expect(selection).not.toHaveProperty('theme');
  });

  it('falls back to the first returned mount when none is active', () => {
    expect(createSpaceSelectionState('shared-team', [mounts[0]]).activeMountId).toBe('warming');
    expect(createSpaceSelectionState('shared-team').activeMountId).toBe('');
  });

  it('preserves an accessible space and otherwise falls back to personal then shared', () => {
    const spaces: MemberSpace[] = [
      { id: 'shared-first', type: 'shared', name: 'Shared', role: 'viewer' },
      { id: 'personal', type: 'personal', name: 'Personal', role: 'manager' },
      { id: 'shared-second', type: 'shared', name: 'Shared 2', role: 'editor' },
    ];

    expect(resolveAccessibleSpaceId(spaces, 'shared-second')).toBe('shared-second');
    expect(resolveAccessibleSpaceId(spaces, '')).toBe('');
    expect(resolveAccessibleSpaceId(spaces, 'removed')).toBe('personal');
    expect(resolveAccessibleSpaceId(spaces.filter((space) => space.type === 'shared'), 'removed')).toBe('shared-first');
    expect(resolveAccessibleSpaceId([], 'removed')).toBe('');
  });

  it('rejects a late request result after the active space generation changes', async () => {
    let activeSpaceId = 'personal';
    let generation = 4;
    let resolveLateRequest!: (value: string) => void;
    const lateRequest = new Promise<string>((resolve) => {
      resolveLateRequest = resolve;
    });
    const oldRequest = createSpaceRequestToken(activeSpaceId, generation);
    const appliedResults: string[] = [];
    const settlement = lateRequest.then((value) => {
      if (isSpaceRequestCurrent(oldRequest, activeSpaceId, generation)) appliedResults.push(value);
    });

    activeSpaceId = 'shared-team';
    generation += 1;
    resolveLateRequest('old-space-result');
    await settlement;

    expect(appliedResults).toEqual([]);
    expect(isSpaceRequestCurrent(oldRequest, 'personal', generation)).toBe(false);
  });

  it('updates mount results without resetting search or invalidating the current request token', () => {
    const generation = 7;
    const request = createSpaceRequestToken('shared-team', generation);
    const current = {
      mounts: [mounts[0]],
      activeMountId: mounts[0].id,
      searchQuery: 'quarterly report',
      searchResults: [{ id: 'result-1' }],
      searchNextCursor: 'next-page',
    };
    const next = mergeMountSelectionState(current, mounts);
    const nextGeneration = nextSpaceRequestGeneration('shared-team', 'shared-team', generation);

    expect(next).toMatchObject({
      mounts,
      activeMountId: 'active-first',
      searchQuery: 'quarterly report',
      searchResults: [{ id: 'result-1' }],
      searchNextCursor: 'next-page',
    });
    expect(nextGeneration).toBe(generation);
    expect(isSpaceRequestCurrent(request, 'shared-team', nextGeneration)).toBe(true);
    expect(nextSpaceRequestGeneration('shared-team', 'personal', generation)).toBe(generation + 1);
  });

  it('keeps the trash tab selected and never redirects to files when trash is unavailable', () => {
    const unavailable = resolveTrashTabState('trash', false);
    expect(unavailable).toEqual({
      activeTab: 'trash',
      loadTrash: false,
      clearTrash: true,
    });
    expect(unavailable.activeTab).not.toBe('files');
    expect(resolveTrashTabState('trash', true)).toEqual({
      activeTab: 'trash',
      loadTrash: true,
      clearTrash: false,
    });
  });

  it('commits success, error, and finally only for the current space request', () => {
    const request = createSpaceRequestToken('shared-team', 9);
    const staleRequest = createSpaceRequestToken('personal', 8);
    const initial = { entries: ['new-space'], error: '', loading: true };
    const commits = [
      { phase: 'success' as const, patch: { entries: ['loaded-root'] } },
      { phase: 'error' as const, patch: { error: 'request failed' } },
      { phase: 'finally' as const, patch: { loading: false } },
    ];

    for (const commit of commits) {
      const current = transitionSpaceRequestCommit(initial, request, 'shared-team', 9, commit);
      expect(current).toEqual({ state: { ...initial, ...commit.patch }, phase: commit.phase, committed: true });

      const stale = transitionSpaceRequestCommit(initial, staleRequest, 'shared-team', 9, commit);
      expect(stale).toEqual({ state: initial, phase: commit.phase, committed: false });
      expect(stale.state).toBe(initial);
    }
  });

  it('changes file context without changing the active account tab', () => {
    expect(createSpaceSelectionTransition('files', 'shared-team')).toMatchObject({
      activeTab: 'files',
      activeSpaceId: 'shared-team',
      activeMountId: '',
      relativePath: '.',
      searchResults: null,
    });
    expect(createSpaceSelectionTransition('shares', 'personal')).toMatchObject({
      activeTab: 'shares',
      activeSpaceId: 'personal',
    });
  });
});
