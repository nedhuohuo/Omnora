import { describe, expect, it } from 'vitest';

import type { AiTokenBoundary } from '../api';
import { boundaryPayload, boundarySummary, type BoundaryDraft } from './MemberTokensPanel';

describe('AI token account-content boundaries', () => {
  it('serializes each source as a disjoint account-mount boundary', () => {
    const drafts: BoundaryDraft[] = [
      { key: 'all', source: 'all_account_content', mountId: '', path: 'must-not-leak' },
      { key: 'personal', source: 'personal', mountId: 'must-not-leak', path: 'notes' },
      { key: 'common', source: 'common_mount', mountId: 'common-1', path: 'design' },
    ];

    expect(drafts.map(boundaryPayload)).toEqual([
      { source: 'all_account_content' },
      { source: 'personal', path: 'notes' },
      { source: 'common_mount', mountId: 'common-1', path: 'design' },
    ]);
  });

  it('describes token reach with account-visible labels and no space identity', () => {
    const sources = {
      personal: { source: 'personal' as const, label: 'My files' },
      commonMounts: [
        { source: 'common_mount' as const, mountId: 'common-1', displayName: 'Design', permission: 'editor' as const, mode: 'read_write' as const },
      ],
    };
    const boundaries: AiTokenBoundary[] = [
      { source: 'all_account_content' },
      { source: 'personal', path: 'notes' },
      { source: 'common_mount', mountId: 'common-1', path: 'design' },
    ];

    expect(boundaries.map((boundary) => boundarySummary(boundary, sources, 'All account content'))).toEqual([
      'All account content',
      'My files · notes',
      'Design · design',
    ]);
  });
});
