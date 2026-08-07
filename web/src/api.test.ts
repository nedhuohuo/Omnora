import { afterEach, describe, expect, it, vi } from 'vitest';
import { requestJson } from './api';

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
