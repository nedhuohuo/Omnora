import type { ThemePreference } from '../api';

const systemDarkQuery = typeof window !== 'undefined' && typeof window.matchMedia === 'function'
  ? window.matchMedia('(prefers-color-scheme: dark)')
  : null;

let currentTheme: ThemePreference = 'system';

function resolveTheme(theme: ThemePreference): 'light' | 'dark' {
  if (theme === 'system') return systemDarkQuery?.matches ? 'dark' : 'light';
  return theme;
}

function applyTheme() {
  if (typeof document === 'undefined') return;
  document.documentElement.setAttribute('data-theme', resolveTheme(currentTheme));
}

export function applyThemePreference(theme: ThemePreference) {
  currentTheme = theme;
  applyTheme();
}

systemDarkQuery?.addEventListener?.('change', applyTheme);
applyTheme();
