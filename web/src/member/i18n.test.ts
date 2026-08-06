import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DirectoryChildrenPayload } from '../api';
import { defaultRootForKind } from './AdminWorkspace';
import { getStoredLocale, localeMessages, resolveLocale, saveLocale } from './i18n';
import { formatDirectoryChildren } from './types';

function createStorage(): Storage {
  const values = new Map<string, string>();

  return {
    get length() {
      return values.size;
    },
    clear() {
      values.clear();
    },
    getItem(key) {
      return values.get(key) ?? null;
    },
    key(index) {
      return [...values.keys()][index] ?? null;
    },
    removeItem(key) {
      values.delete(key);
    },
    setItem(key, value) {
      values.set(key, value);
    },
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('member locale', () => {
  it('uses Chinese when no saved locale exists', () => {
    expect(resolveLocale(null)).toBe('zh-CN');
    expect(localeMessages['zh-CN'].files).toBe('\u6587\u4ef6');
  });

  it('persists a selected English locale', () => {
    vi.stubGlobal('localStorage', createStorage());

    saveLocale('en-US');

    expect(getStoredLocale()).toBe('en-US');
    expect(resolveLocale('en-US')).toBe('en-US');
  });

  it('keeps zh-CN and en-US message keys in parity', () => {
    const zhKeys = Object.keys(localeMessages['zh-CN']).sort();
    const enKeys = Object.keys(localeMessages['en-US']).sort();

    expect(enKeys).toEqual(zhKeys);
  });
});

describe('directory response formatting', () => {
  it('returns the API directory entries as typed member entries without fallback data', () => {
    const response: DirectoryChildrenPayload = {
      relativePath: 'documents',
      readOnly: false,
      entries: [
        {
          name: 'plan.md',
          relativePath: 'documents/plan.md',
          kind: 'file',
          size: 128,
          modifiedAt: '2026-08-03T22:00:00Z',
          readOnly: false,
          previewKind: 'markdown',
        },
      ],
    };

    expect(formatDirectoryChildren(response)).toEqual({
      relativePath: 'documents',
      readOnly: false,
      entries: response.entries,
    });
  });
});

describe('admin host directory roots', () => {
  it('selects configured custom paths by their authoritative kind', () => {
    const roots = [
      { path: '/data/omnora-managed', kind: 'managed' as const },
      { path: '/volume/nas', kind: 'external' as const },
    ];

    expect(defaultRootForKind('managed', roots)).toBe('/data/omnora-managed');
    expect(defaultRootForKind('external', roots)).toBe('/volume/nas');
  });

  it('keeps the first configured path as a legacy fallback without guessing its kind', () => {
    const roots = [{ path: '/custom/storage' }];

    expect(defaultRootForKind('managed', roots)).toBe('/custom/storage');
    expect(defaultRootForKind('external', roots)).toBe('/custom/storage');
  });
});
