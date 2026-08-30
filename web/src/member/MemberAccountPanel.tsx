import { type FormEvent, useCallback, useEffect, useState } from 'react';
import {
  type AccountPayload,
  type AccountSessionPayload,
  ApiError,
  confirmTOTP,
  deleteSession,
  disableTOTP,
  getAccount,
  getPreferences,
  listSessions,
  setupTOTP,
  type ThemePreference,
  updateAccountPassword,
  updatePreferences,
} from '../api';
import { type MemberLocale, localeMessages } from './i18n';
import { applyThemePreference } from './theme';

function describeError(error: unknown) {
  if (error instanceof ApiError) {
    const body = error.body as { error?: { message?: string; code?: string } } | undefined;
    const message = body?.error?.message?.trim();
    if (message) return `HTTP ${error.status}: ${message}`;
    if (body?.error?.code) return `HTTP ${error.status}: ${body.error.code}`;
    return `HTTP ${error.status}`;
  }
  return error instanceof Error ? error.message : 'Unknown error';
}

function formatDate(value: string | undefined, locale: MemberLocale) {
  if (!value) return '--';
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? '--' : new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
}

export default function MemberAccountPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [account, setAccount] = useState<AccountPayload | null>(null);
  const [sessions, setSessions] = useState<AccountSessionPayload[]>([]);
  const [theme, setTheme] = useState<ThemePreference>('system');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  const [passwordForm, setPasswordForm] = useState({ current: '', next: '', confirm: '', revokeTokens: false, revokeShares: false });
  const [passwordSaving, setPasswordSaving] = useState(false);
  const [passwordError, setPasswordError] = useState('');

  const [totpSetup, setTotpSetup] = useState<{ secret: string; otpauthUri?: string } | null>(null);
  const [totpCode, setTotpCode] = useState('');
  const [totpBusy, setTotpBusy] = useState(false);
  const [totpError, setTotpError] = useState('');
  const [disablePassword, setDisablePassword] = useState('');
  const [disableCode, setDisableCode] = useState('');
  const [disableOpen, setDisableOpen] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [accountResponse, sessionResponse] = await Promise.all([getAccount(), listSessions()]);
      setAccount(accountResponse);
      setSessions(sessionResponse.items ?? []);
      try {
        const preferences = await getPreferences();
        setTheme(preferences.theme);
        applyThemePreference(preferences.theme);
      } catch {
        // Preference storage may not be available yet; keep the current theme.
      }
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function onChangePassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setPasswordError('');
    if (passwordForm.next !== passwordForm.confirm) {
      setPasswordError(text.accountPasswordMismatch);
      return;
    }
    setPasswordSaving(true);
    try {
      await updateAccountPassword({
        currentPassword: passwordForm.current,
        newPassword: passwordForm.next,
        revokeTokens: passwordForm.revokeTokens,
        revokeShares: passwordForm.revokeShares,
      });
      setPasswordForm({ current: '', next: '', confirm: '', revokeTokens: false, revokeShares: false });
      setNotice(text.accountPasswordUpdated);
      await load();
    } catch (caught) {
      setPasswordError(describeError(caught));
    } finally {
      setPasswordSaving(false);
    }
  }

  async function onStartTotpSetup() {
    setTotpError('');
    setTotpBusy(true);
    try {
      const response = await setupTOTP();
      setTotpSetup({ secret: response.secret ?? '', otpauthUri: response.otpauthUri });
    } catch (caught) {
      setTotpError(describeError(caught));
    } finally {
      setTotpBusy(false);
    }
  }

  async function onConfirmTotp(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setTotpBusy(true);
    setTotpError('');
    try {
      await confirmTOTP(totpCode.trim());
      setTotpSetup(null);
      setTotpCode('');
      await load();
    } catch (caught) {
      setTotpError(describeError(caught));
    } finally {
      setTotpBusy(false);
    }
  }

  async function onDisableTotp(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setTotpBusy(true);
    setTotpError('');
    try {
      await disableTOTP(disablePassword, disableCode.trim());
      setDisableOpen(false);
      setDisablePassword('');
      setDisableCode('');
      await load();
    } catch (caught) {
      setTotpError(describeError(caught));
    } finally {
      setTotpBusy(false);
    }
  }

  async function onRevokeSession(session: AccountSessionPayload) {
    setLoading(true);
    setError('');
    try {
      await deleteSession(session.id);
      await load();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function onThemeChange(next: ThemePreference) {
    setTheme(next);
    applyThemePreference(next);
    try {
      await updatePreferences({ theme: next });
    } catch (caught) {
      setError(describeError(caught));
    }
  }

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div><h1>{text.accountTitle}</h1><p>{text.accountTitleDetail}</p></div>
        <button className="member-secondary-action" type="button" onClick={() => void load()} disabled={loading}>{text.refresh}</button>
      </div>

      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {notice && <p className="member-admin-notice">{notice}</p>}

      <section className="member-account-section">
        <h2>{text.accountProfileSection}</h2>
        {account && <div className="member-account-profile">
          <p><span>{text.accountEmail}</span><strong>{account.email}</strong></p>
          <p><span>{text.accountDisplayName}</span><strong>{account.displayName}</strong></p>
        </div>}
      </section>

      <section className="member-account-section">
        <h2>{text.accountPasswordSection}</h2>
        <form className="member-admin-form" onSubmit={onChangePassword}>
          <label>{text.accountCurrentPassword}<input type="password" value={passwordForm.current} onChange={(event) => setPasswordForm({ ...passwordForm, current: event.target.value })} autoComplete="current-password" required /></label>
          <label>{text.accountNewPassword}<input type="password" value={passwordForm.next} onChange={(event) => setPasswordForm({ ...passwordForm, next: event.target.value })} autoComplete="new-password" required /></label>
          <label>{text.accountConfirmPassword}<input type="password" value={passwordForm.confirm} onChange={(event) => setPasswordForm({ ...passwordForm, confirm: event.target.value })} autoComplete="new-password" required /></label>
          <label className="member-admin-checkbox"><input type="checkbox" checked={passwordForm.revokeTokens} onChange={(event) => setPasswordForm({ ...passwordForm, revokeTokens: event.target.checked })} />{text.accountRevokeTokens}</label>
          <label className="member-admin-checkbox"><input type="checkbox" checked={passwordForm.revokeShares} onChange={(event) => setPasswordForm({ ...passwordForm, revokeShares: event.target.checked })} />{text.accountRevokeShares}</label>
          {passwordError && <div className="member-error member-page-error member-admin-form-wide">{text.error}: {passwordError}</div>}
          <div className="member-admin-form-actions"><button className="member-primary" type="submit" disabled={passwordSaving}>{text.accountUpdatePassword}</button></div>
        </form>
      </section>

      <section className="member-account-section">
        <h2>{text.accountTotpSection}</h2>
        <p>{account?.totpEnabled ? text.accountTotpEnabled : text.accountTotpDisabledLabel}</p>
        {totpError && <div className="member-error member-page-error">{text.error}: {totpError}</div>}
        {!account?.totpEnabled && !totpSetup && <button className="member-primary" type="button" onClick={() => void onStartTotpSetup()} disabled={totpBusy}>{text.accountTotpSetup}</button>}
        {totpSetup && (
          <form className="member-admin-inline-form" onSubmit={onConfirmTotp}>
            <p className="member-admin-form-wide member-modal-hint">{text.accountTotpSetupHint}</p>
            <label>{text.accountTotpSecret}<input readOnly value={totpSetup.secret} /></label>
            <label>{text.accountTotpCode}<input value={totpCode} onChange={(event) => setTotpCode(event.target.value)} inputMode="numeric" autoComplete="one-time-code" required /></label>
            <button className="member-primary" type="submit" disabled={totpBusy}>{text.accountTotpConfirm}</button>
          </form>
        )}
        {account?.totpEnabled && !disableOpen && <button className="member-table-action member-table-danger" type="button" onClick={() => setDisableOpen(true)}>{text.accountTotpDisable}</button>}
        {disableOpen && (
          <form className="member-admin-inline-form" onSubmit={onDisableTotp}>
            <p className="member-admin-form-wide member-modal-hint">{text.accountTotpDisableHint}</p>
            <label>{text.accountCurrentPassword}<input type="password" value={disablePassword} onChange={(event) => setDisablePassword(event.target.value)} autoComplete="current-password" required /></label>
            <label>{text.accountTotpCode}<input value={disableCode} onChange={(event) => setDisableCode(event.target.value)} inputMode="numeric" autoComplete="one-time-code" required /></label>
            <button type="button" onClick={() => setDisableOpen(false)}>{text.cancel}</button>
            <button className="member-modal-danger" type="submit" disabled={totpBusy}>{text.accountTotpDisable}</button>
          </form>
        )}
      </section>

      <section className="member-account-section">
        <h2>{text.accountSessionsSection}</h2>
        {loading ? <div className="member-loading">{text.loading}</div> : sessions.length === 0 ? <div className="member-empty">{text.accountNoSessions}</div> : (
          <table className="member-admin-table">
            <thead><tr><th>{text.time}</th><th>{text.updated}</th><th>{text.actions}</th></tr></thead>
            <tbody>{sessions.map((session) => (
              <tr key={session.id}>
                <td>{formatDate(session.createdAt, locale)}{session.current ? ` · ${text.accountSessionCurrent}` : ''}</td>
                <td>{formatDate(session.lastUsedAt ?? session.expiresAt, locale)}</td>
                <td>{!session.current && <button className="member-table-action member-table-danger" type="button" onClick={() => void onRevokeSession(session)} disabled={loading}>{text.accountSessionRevoke}</button>}</td>
              </tr>
            ))}</tbody>
          </table>
        )}
      </section>

      <section className="member-account-section">
        <h2>{text.accountThemeSection}</h2>
        <div className="member-view-toggle member-theme-toggle">
          <button type="button" aria-pressed={theme === 'system'} onClick={() => void onThemeChange('system')}>{text.accountThemeSystem}</button>
          <button type="button" aria-pressed={theme === 'light'} onClick={() => void onThemeChange('light')}>{text.accountThemeLight}</button>
          <button type="button" aria-pressed={theme === 'dark'} onClick={() => void onThemeChange('dark')}>{text.accountThemeDark}</button>
        </div>
      </section>
    </div>
  );
}
