import { describe, expect, it, vi } from 'vitest';

describe('authenticated theme synchronization', () => {
  it('applies the server preference after the session is ready', async () => {
    const setAttribute = vi.fn();
    vi.stubGlobal('document', { documentElement: { setAttribute } });
    vi.stubGlobal('localStorage', {
      getItem: () => null,
      setItem: vi.fn(),
    });

    const { syncAuthenticatedTheme } = await import('./themeSync');
    const loadPreferences = vi.fn(async () => ({ theme: 'light' as const }));

    await syncAuthenticatedTheme(loadPreferences);

    expect(loadPreferences).toHaveBeenCalledOnce();
    expect(setAttribute).toHaveBeenCalledWith('data-theme', 'light');
  });

  it('keeps the current theme when preference loading is unavailable', async () => {
    const setAttribute = vi.fn();
    vi.stubGlobal('document', { documentElement: { setAttribute } });
    vi.stubGlobal('localStorage', {
      getItem: () => null,
      setItem: vi.fn(),
    });

    const { syncAuthenticatedTheme } = await import('./themeSync');
    await expect(syncAuthenticatedTheme(async () => {
      throw new Error('session is not ready');
    })).resolves.toBeUndefined();

    expect(setAttribute).not.toHaveBeenCalled();
  });
});
