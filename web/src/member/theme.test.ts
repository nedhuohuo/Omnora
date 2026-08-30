import { afterEach, describe, expect, it, vi } from 'vitest';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.resetModules();
});

describe('theme bootstrap', () => {
  it('applies the system theme before account settings are opened', async () => {
    const setAttribute = vi.fn();
    vi.stubGlobal('document', { documentElement: { setAttribute } });
    vi.stubGlobal('window', {
      matchMedia: vi.fn(() => ({ matches: true, addEventListener: vi.fn() })),
    });

    const { applyThemePreference } = await import('./theme');
    expect(setAttribute).toHaveBeenCalledWith('data-theme', 'dark');

    applyThemePreference('light');
    expect(setAttribute).toHaveBeenLastCalledWith('data-theme', 'light');
  });
});
