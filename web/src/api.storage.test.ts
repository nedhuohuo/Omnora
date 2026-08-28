import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  deleteAdminMountGrant,
  getAdminMount,
  listAdminMountGrants,
  putAdminMountGrant,
  updateAdminMount,
} from './api';

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

function mountDetailResponse(overrides: Record<string, unknown> = {}) {
  return {
    id: 'mount-1',
    spaceId: 'space-1',
    spaceName: 'Space',
    name: 'Archive',
    displayName: 'Archive',
    rootPath: '/mnt/archive',
    kind: 'external',
    mode: 'read_only',
    indexEnabled: false,
    allowPublicShares: false,
    health: 'active',
    grants: [],
    eligibleAccounts: [],
    ...overrides,
  };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('admin storage API', () => {
  it('loads mount detail and grants from mount-scoped endpoints', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(jsonResponse(mountDetailResponse({ id: 'mount 1' })))
      .mockResolvedValueOnce(jsonResponse({ items: [], eligibleAccounts: [] }));

    await getAdminMount('mount 1');
    await listAdminMountGrants('mount 1');

    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/admin/mounts/mount%201');
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/admin/mounts/mount%201/grants');
  });

  it('patches mount settings using the approved field names', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse(mountDetailResponse()));

    await updateAdminMount('mount-1', {
      displayName: 'Team archive',
      mode: 'read_only',
      indexEnabled: false,
      allowPublicShares: false,
    });

    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(init.method).toBe('PATCH');
    expect(JSON.parse(String(init.body))).toEqual({
      displayName: 'Team archive',
      mode: 'read_only',
      indexEnabled: false,
      allowPublicShares: false,
    });
  });

  it('puts and deletes individual viewer-editor-manager grants', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(jsonResponse({ accountId: 'account/1', spacePermission: 'manager', permission: 'editor', effectivePermission: 'editor' }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));

    await putAdminMountGrant('mount-1', 'account/1', 'editor');
    await deleteAdminMountGrant('mount-1', 'account/1');

    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/admin/mounts/mount-1/grants/account%2F1');
    const putInit = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(putInit.method).toBe('PUT');
    expect(JSON.parse(String(putInit.body))).toEqual({ permission: 'editor' });
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/admin/mounts/mount-1/grants/account%2F1');
    expect((fetchMock.mock.calls[1]?.[1] as RequestInit).method).toBe('DELETE');
  });

  it('rejects mount grants with missing effective permission fields', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse(mountDetailResponse({
      grants: [{ accountId: 'account-1', spacePermission: 'manager', permission: 'editor' }],
    })));

    await expect(getAdminMount('mount-1')).rejects.toThrow('Admin mount grant response is malformed');
  });
});
