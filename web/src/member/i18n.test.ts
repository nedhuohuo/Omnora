import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DirectoryChildrenPayload } from '../api';
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

  it('keeps account-level mount management copy free of business Space terminology', () => {
    expect(localeMessages['zh-CN'].mountManagementDetail).not.toContain('空间');
    expect(localeMessages['en-US'].mountManagementDetail).not.toMatch(/\bSpace\b/);
  });

  it('describes MCP as standard Streamable HTTP without claiming OAuth support', () => {
    expect(localeMessages['zh-CN'].tokenMcpStatusDetail).toContain('Streamable HTTP');
    expect(localeMessages['zh-CN'].routeDescMcp).toContain('OAuth');
    expect(localeMessages['zh-CN'].tokenMcpAuth).toContain('认证');
    expect(localeMessages['en-US'].tokenMcpStatusDetail).toContain('OAuth is not implemented');
    expect(localeMessages['en-US'].routeDescOpenapi).toContain('Raw OpenAPI 3.1 YAML');
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
