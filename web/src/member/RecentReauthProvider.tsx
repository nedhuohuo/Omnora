import { createContext, type FormEvent, type ReactNode, useCallback, useContext, useMemo, useState } from 'react';
import { ApiError, isReauthenticationRequired, ReauthenticationCanceledError, reauthenticate } from '../api';
import { type MemberLocale, localeMessages } from './i18n';

type RunSensitive = <T>(operation: () => Promise<T>) => Promise<T>;

type RecentReauthContextValue = {
  runSensitive: RunSensitive;
};

const RecentReauthContext = createContext<RecentReauthContextValue | null>(null);

export function useRecentReauth(): RecentReauthContextValue {
  const value = useContext(RecentReauthContext);
  if (!value) {
    throw new Error('useRecentReauth must be used within RecentReauthProvider');
  }
  return value;
}

export function RecentReauthProvider({
  locale,
  children,
}: {
  locale: MemberLocale;
  children: ReactNode;
}) {
  const text = localeMessages[locale];
  const [pending, setPending] = useState<{
    resolve: (value: unknown) => void;
    reject: (reason?: unknown) => void;
    operation: () => Promise<unknown>;
  } | null>(null);
  const [password, setPassword] = useState('');
  const [totpCode, setTotpCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const closePending = useCallback((reason?: unknown) => {
    setPending((current) => {
      if (current) {
        current.reject(reason ?? new ReauthenticationCanceledError());
      }
      return null;
    });
    setPassword('');
    setTotpCode('');
    setError('');
    setBusy(false);
  }, []);

  const runSensitive = useCallback<RunSensitive>(async (operation) => {
    try {
      return await operation();
    } catch (caught) {
      if (!isReauthenticationRequired(caught)) {
        throw caught;
      }
      return await new Promise((resolve, reject) => {
        setPending({
          resolve: (value) => resolve(value as Awaited<ReturnType<typeof operation>>),
          reject,
          operation,
        });
      });
    }
  }, []);

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!pending) return;
    setBusy(true);
    setError('');
    try {
      await reauthenticate({
        password,
        totpCode: totpCode.trim() || undefined,
      });
      const result = await pending.operation();
      const { resolve } = pending;
      setPending(null);
      setPassword('');
      setTotpCode('');
      setBusy(false);
      resolve(result);
    } catch (caught) {
      if (isReauthenticationRequired(caught)) {
        setError(text.reauthStillRequired);
        setBusy(false);
        pending.reject(caught);
        setPending(null);
        setPassword('');
        setTotpCode('');
        return;
      }
      if (caught instanceof ApiError && caught.status === 401) {
        setError(text.reauthInvalidCredentials);
        setBusy(false);
        return;
      }
      setError(caught instanceof Error ? caught.message : text.error);
      setBusy(false);
      pending.reject(caught);
      setPending(null);
      setPassword('');
      setTotpCode('');
    }
  }

  const value = useMemo(() => ({ runSensitive }), [runSensitive]);

  return (
    <RecentReauthContext.Provider value={value}>
      {children}
      {pending ? (
        <div className="member-modal-backdrop member-reauth-backdrop" role="presentation">
          <form className="member-modal" onSubmit={onSubmit}>
            <h2>{text.reauthTitle}</h2>
            <p className="member-modal-hint">{text.reauthDetail}</p>
            <label>
              {text.password}
              <input
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                required
              />
            </label>
            <label>
              {text.verificationCode}
              <input
                type="text"
                inputMode="numeric"
                autoComplete="one-time-code"
                value={totpCode}
                onChange={(event) => setTotpCode(event.target.value)}
              />
            </label>
            {error ? <p className="member-error">{error}</p> : null}
            <div className="member-modal-actions">
              <button type="button" onClick={() => closePending()} disabled={busy}>
                {text.cancel}
              </button>
              <button className="member-primary" type="submit" disabled={busy || !password}>
                {busy ? text.loading : text.reauthSubmit}
              </button>
            </div>
          </form>
        </div>
      ) : null}
    </RecentReauthContext.Provider>
  );
}
