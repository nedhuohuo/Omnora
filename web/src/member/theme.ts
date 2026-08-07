import { type ThemePreference } from '../api';

// localStorage mirror of the server-stored theme preference. The server
// (/api/v1/account/preferences) is the source of truth; this mirror only lets
// the app render the correct theme before the first authenticated round-trip.
const STORAGE_KEY = 'omnora.theme';
const VALID_THEMES: readonly string[] = ['system', 'light', 'dark'];

const systemDarkQuery =
  typeof window !== 'undefined' && typeof window.matchMedia === 'function' ? window.matchMedia('(prefers-color-scheme: dark)') : null;

function isThemePreference(value: unknown): value is ThemePreference {
  return typeof value === 'string' && VALID_THEMES.includes(value);
}

function readStoredTheme(): ThemePreference {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored && isThemePreference(stored)) return stored;
  } catch {
    // localStorage may be unavailable (private mode, storage disabled).
  }
  return 'system';
}

function resolveTheme(theme: ThemePreference): 'light' | 'dark' {
  if (theme === 'system') {
    return systemDarkQuery?.matches ? 'dark' : 'light';
  }
  return theme;
}

function applyTheme() {
  document.documentElement.setAttribute('data-theme', resolveTheme(currentTheme));
}

let currentTheme: ThemePreference = readStoredTheme();

// Apply synchronously at module load so a saved preference is present before
// first paint. The inline <head> script covers the very first paint; this
// re-applies the same resolution once the app bundle loads and keeps it in
// sync when the OS color scheme changes.
if (typeof document !== 'undefined') {
  applyTheme();
}

// Re-resolve a "system" preference when the OS color scheme changes.
systemDarkQuery?.addEventListener?.('change', applyTheme);

export function applyThemePreference(theme: ThemePreference) {
  currentTheme = theme;
  try {
    localStorage.setItem(STORAGE_KEY, theme);
  } catch {
    // The mirror is best-effort; the server remains the source of truth.
  }
  applyTheme();
}
