import { type PreferencesPayload } from '../api';
import { applyThemePreference } from './theme';

export async function syncAuthenticatedTheme(loadPreferences: () => Promise<PreferencesPayload>): Promise<void> {
  try {
    const preferences = await loadPreferences();
    applyThemePreference(preferences.theme);
  } catch {
    // Keep the current theme until the authenticated preference is available.
  }
}
