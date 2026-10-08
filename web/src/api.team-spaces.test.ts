import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  createTeamSpace,
  listTeamSpaceMembers,
  putTeamSpaceMember,
  removeTeamSpaceMember,
} from './api';

describe('team space API clients', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('uses account-scoped team-space routes with encoded identifiers', async () => {
    vi.stubGlobal('document', { cookie: 'omnora_dev_csrf=account-token' });
    const fetchMock = vi.fn<typeof fetch>(async () => new Response('{"items":[]}', {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }));
    vi.stubGlobal('fetch', fetchMock);

    await createTeamSpace({ name: '研发协作' });
    await listTeamSpaceMembers('sp/1');
    await putTeamSpaceMember('sp/1', 'USER+ONE@example.test', 'viewer');
    await removeTeamSpaceMember('sp/1', 'acct/1');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/team-spaces', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ name: '研发协作' }),
      credentials: 'same-origin',
    }));
    expect(new Headers(fetchMock.mock.calls[0][1]?.headers).get('X-CSRF-Token')).toBe('account-token');
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/team-spaces/sp%2F1/members', expect.objectContaining({
      credentials: 'same-origin',
    }));
    expect(fetchMock.mock.calls[1][1]?.method ?? 'GET').toBe('GET');
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/v1/team-spaces/sp%2F1/members/USER%2BONE%40example.test', expect.objectContaining({
      method: 'PUT',
      body: JSON.stringify({ permission: 'viewer' }),
      credentials: 'same-origin',
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/v1/team-spaces/sp%2F1/members/acct%2F1', expect.objectContaining({
      method: 'DELETE',
      credentials: 'same-origin',
    }));
  });
});
