import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  createAiToken,
  listMemberCollaborations,
  putAdminMountGrant,
  registerAdminMount,
  listMemberContentSources,
  listMemberDirectoryChildren,
  memberDownloadURL,
  createMemberDirectory,
  renameMemberObject,
  requestJson,
} from './api';

describe('requestJson CSRF selection', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('uses account CSRF for /api/v1/shares', async () => {
    vi.stubGlobal('document', {
      cookie: 'omnora_dev_csrf=account-token; omnora_dev_share_csrf=share-token',
    });
    const fetchMock = vi.fn<typeof fetch>(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);

    await requestJson('/api/v1/shares', { method: 'POST', body: '{}' });

    expect(fetchMock).toHaveBeenCalled();
    const headers = new Headers(fetchMock.mock.calls[0][1]?.headers);
    expect(headers.get('X-CSRF-Token')).toBe('account-token');
  });

  it('uses share CSRF only when authContext is share', async () => {
    vi.stubGlobal('document', {
      cookie: 'omnora_dev_csrf=account-token; omnora_dev_share_csrf=share-token',
    });
    const fetchMock = vi.fn<typeof fetch>(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);

    await requestJson('/api/v1/share/current', { method: 'POST', body: '{}', authContext: 'share' });

    expect(fetchMock).toHaveBeenCalled();
    const headers = new Headers(fetchMock.mock.calls[0][1]?.headers);
    expect(headers.get('X-CSRF-Token')).toBe('share-token');
  });
});

describe('member content source API contract', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('lists account-visible content sources without a space or account selector', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
      personal: { source: 'personal', label: '我的文件' },
      commonMounts: [],
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);

    await listMemberContentSources();

    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/member/content-sources');
    expect(String(fetchMock.mock.calls[0][0])).not.toMatch(/spaceId|accountId|defaultMountId/);
  });

  it('serializes each content locator as a disjoint union', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ entries: [] }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }));
    vi.stubGlobal('fetch', fetchMock);

    await listMemberDirectoryChildren({ source: 'personal', path: 'notes' });
    await listMemberDirectoryChildren({ source: 'common_mount', mountId: 'mount / 1', path: '.' });
    await listMemberDirectoryChildren({ source: 'collaboration', collaborationId: 'collab / 1', path: 'drafts' });

    expect(fetchMock.mock.calls.map(([url]) => String(url))).toEqual([
      '/api/v1/member/files/children?source=personal&path=notes',
      '/api/v1/member/files/children?source=common_mount&path=.&mountId=mount+%2F+1',
      '/api/v1/member/files/children?source=collaboration&path=drafts&collaborationId=collab+%2F+1',
    ]);
    expect(fetchMock.mock.calls.map(([url]) => String(url)).join('\n')).not.toMatch(/spaceId|accountId|defaultMountId/);
  });

  it('uses account content operation routes without Space coordinates', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ relativePath: 'docs/renamed.txt' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }));
    vi.stubGlobal('fetch', fetchMock);

    await createMemberDirectory({ source: 'personal', path: 'docs' }, 'new-folder');
    await renameMemberObject({ source: 'common_mount', mountId: 'mount-1', path: 'docs/a.txt' }, 'docs/b.txt');

    expect(fetchMock.mock.calls.map(([url]) => String(url))).toEqual([
      '/api/v1/member/files/directories',
      '/api/v1/member/files/rename',
    ]);
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({ source: 'personal', path: 'docs', name: 'new-folder' });
    expect(JSON.parse(String(fetchMock.mock.calls[1][1]?.body))).toEqual({ source: 'common_mount', mountId: 'mount-1', path: 'docs/a.txt', toPath: 'docs/b.txt' });
    expect(JSON.stringify(fetchMock.mock.calls)).not.toMatch(/spaceId|spaceName|accountId/);
  });

  it('builds a locator-based download URL', () => {
    expect(memberDownloadURL({ source: 'personal', path: 'docs/a b.txt' })).toBe('/api/v1/member/files/download?source=personal&path=docs%2Fa+b.txt');
    expect(memberDownloadURL({ source: 'common_mount', mountId: 'mount / 1', path: 'a.txt' })).toBe('/api/v1/member/files/download?source=common_mount&path=a.txt&mountId=mount+%2F+1');
  });

  it('lists incoming and outgoing collaborations on the member route', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ items: [] }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }));
    vi.stubGlobal('fetch', fetchMock);

    await listMemberCollaborations('incoming');
    await listMemberCollaborations('outgoing');

    expect(fetchMock.mock.calls.map(([url]) => String(url))).toEqual([
      '/api/v1/member/collaborations?direction=incoming',
      '/api/v1/member/collaborations?direction=outgoing',
    ]);
  });
});

describe('admin mount API contract', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('creates an account-level mount without a Space selector', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ id: 'mount-1' }), {
      status: 201,
      headers: { 'Content-Type': 'application/json' },
    }));
    vi.stubGlobal('fetch', fetchMock);

    await registerAdminMount({
      displayName: 'Photos',
      rootPath: '/mnt/omnora/photos',
      governance: 'normal',
      mode: 'read_write',
      indexEnabled: true,
      grants: [{ accountId: 'account-1', permission: 'editor' }],
    });

    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/admin/mounts');
    const body = String(fetchMock.mock.calls[0][1]?.body);
    expect(JSON.parse(body)).toEqual({
      displayName: 'Photos', rootPath: '/mnt/omnora/photos', governance: 'normal',
      mode: 'read_write', indexEnabled: true,
      grants: [{ accountId: 'account-1', permission: 'editor' }],
    });
    expect(body).not.toMatch(/spaceId|kind|managed/);
  });

  it('uses mount-scoped grant routes', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);
    await putAdminMountGrant('mount / 1', 'account / 1', 'viewer');
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/admin/mounts/mount%20%2F%201/grants/account%20%2F%201');
  });
});

describe('AI token account-content API contract', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('posts source-discriminated boundaries without legacy resource selectors', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
      token: { id: 'token-1', publicId: 'ait-1', name: 'Agent', scopes: ['mounts:read'], expiresAt: '' },
      secret: 'secret',
      bearerToken: 'ait-1.secret',
    }), { status: 201, headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);

    await createAiToken({
      name: 'Agent',
      scopes: ['mounts:read'],
      boundaries: [
        { source: 'all_account_content' },
        { source: 'personal', path: 'notes' },
        { source: 'common_mount', mountId: 'common-1' },
      ],
    });

    const body = String(fetchMock.mock.calls[0][1]?.body);
    expect(JSON.parse(body)).toEqual({
      name: 'Agent',
      scopes: ['mounts:read'],
      boundaries: [
        { source: 'all_account_content' },
        { source: 'personal', path: 'notes' },
        { source: 'common_mount', mountId: 'common-1' },
      ],
    });
    expect(body).not.toMatch(/spaceId|accountId|defaultMountId|collaborationId/);
  });
});
