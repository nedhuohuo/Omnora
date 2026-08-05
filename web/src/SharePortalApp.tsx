import { type FormEvent, useCallback, useEffect, useState } from 'react';
import {
  ApiError,
  clearShareFragment,
  exchangeShareFragmentWithPassword,
  getSharePortalCurrent,
  listSharePortalChildren,
  parseShareFragment,
  type ShareFragment,
  type SharePortalCurrentPayload,
  type SharePortalEntry,
  sharePortalDownloadURL,
} from './api';
import { getStoredLocale, localeMessages, saveLocale, type MemberLocale } from './member/i18n';
import './member/member-files.css';
import './share-portal.css';

type PortalStatus = 'checking' | 'password-required' | 'secret-missing' | 'unavailable' | 'ready';
type PortalMode = 'directory' | 'file' | null;

function describeApiErrorCode(error: unknown): string | undefined {
  if (error instanceof ApiError) {
    const body = error.body as { error?: { code?: string } } | undefined;
    return body?.error?.code;
  }
  return undefined;
}

function canPreview(previewKind: string | undefined) {
  return previewKind === 'image' || previewKind === 'pdf' || previewKind === 'media' || previewKind === 'text' || previewKind === 'markdown';
}

function breadcrumbSegments(path: string) {
  return path === '.' || !path ? [] : path.split('/').filter(Boolean);
}

function childPath(parentPath: string, name: string) {
  return parentPath === '.' || !parentPath ? name : `${parentPath}/${name}`;
}

function formatBytes(bytes: number | undefined) {
  if (bytes === undefined || !Number.isFinite(bytes) || bytes < 0) return '--';
  if (bytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}`;
}

export default function SharePortalApp() {
  const [locale, setLocale] = useState<MemberLocale>(getStoredLocale);
  const text = localeMessages[locale];
  const [status, setStatus] = useState<PortalStatus>('checking');
  const [fragment, setFragment] = useState<ShareFragment | null>(null);
  const [password, setPassword] = useState('');
  const [passwordSubmitting, setPasswordSubmitting] = useState(false);
  const [passwordError, setPasswordError] = useState('');
  const [current, setCurrent] = useState<SharePortalCurrentPayload | null>(null);
  const [mode, setMode] = useState<PortalMode>(null);
  const [path, setPath] = useState('.');
  const [entries, setEntries] = useState<SharePortalEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState('');

  function changeLocale(next: MemberLocale) {
    saveLocale(next);
    setLocale(next);
  }

  const loadChildren = useCallback(async (nextPath: string) => {
    setLoading(true);
    setLoadError('');
    try {
      const response = await listSharePortalChildren(nextPath === '.' ? '' : nextPath);
      setEntries(response.entries ?? []);
      setPath(response.relativePath ?? nextPath);
      return true;
    } catch (caught) {
      setEntries([]);
      setLoadError(caught instanceof Error ? caught.message : text.portalUnavailableDetail);
      return false;
    } finally {
      setLoading(false);
    }
  }, [text.portalUnavailableDetail]);

  const enterReadyState = useCallback(async () => {
    const currentPayload = await getSharePortalCurrent();
    setCurrent(currentPayload);
    setStatus('ready');
    if (currentPayload.kind === 'file') {
      setEntries([]);
      setPath('.');
      setMode('file');
      return;
    }
    if (currentPayload.kind === 'dir') {
      const loaded = await loadChildren('.');
      setMode(loaded ? 'directory' : null);
      return;
    }
    const isDirectory = await loadChildren('.');
    setMode(isDirectory ? 'directory' : 'file');
  }, [loadChildren]);

  const attemptExchange = useCallback(async (target: ShareFragment, passwordValue: string) => {
    try {
      const result = await exchangeShareFragmentWithPassword(target, passwordValue || undefined);
      if (result.status === 'password_required') {
        setStatus('password-required');
        return;
      }
      clearShareFragment();
      await enterReadyState();
    } catch (caught) {
      if (describeApiErrorCode(caught) === 'password_required') {
        setStatus('password-required');
        return;
      }
      setStatus('unavailable');
    }
  }, [enterReadyState]);

  useEffect(() => {
    void (async () => {
      try {
        await enterReadyState();
        return;
      } catch {
        // No existing share session; fall back to fragment exchange below.
      }
      const parsed = parseShareFragment();
      if (!parsed) {
        setStatus('secret-missing');
        return;
      }
      setFragment(parsed);
      await attemptExchange(parsed, '');
    })();
  }, [attemptExchange, enterReadyState]);

  async function onSubmitPassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!fragment) return;
    setPasswordSubmitting(true);
    setPasswordError('');
    try {
      await attemptExchange(fragment, password);
    } finally {
      setPasswordSubmitting(false);
    }
  }

  const crumbs = breadcrumbSegments(path);
  const rootLabel = current?.path || text.portalBackToRoot;
  const filePreviewKind = current?.previewKind ?? 'unknown_download';

  return (
    <main className="share-portal">
      <header className="share-portal-topbar">
        <div className="member-brand"><span>O</span>Omnora</div>
        <div className="member-language" aria-label={text.language}>
          <button type="button" onClick={() => changeLocale('zh-CN')} aria-pressed={locale === 'zh-CN'}>中文</button>
          <button type="button" onClick={() => changeLocale('en-US')} aria-pressed={locale === 'en-US'}>EN</button>
        </div>
      </header>

      <div className="share-portal-stage">
        {status === 'checking' && <p className="share-portal-message">{text.portalLoading}</p>}

        {status === 'secret-missing' && (
          <section className="share-portal-panel">
            <h1>{text.portalUnavailable}</h1>
            <p>{text.portalSecretMissing}</p>
          </section>
        )}

        {status === 'unavailable' && (
          <section className="share-portal-panel">
            <h1>{text.portalUnavailable}</h1>
            <p>{text.portalUnavailableDetail}</p>
          </section>
        )}

        {status === 'password-required' && (
          <section className="share-portal-panel">
            <h1>{text.portalPasswordRequired}</h1>
            <form onSubmit={onSubmitPassword}>
              <label>{text.portalPasswordHint}<input type="password" value={password} onChange={(event) => setPassword(event.target.value)} autoFocus required /></label>
              {passwordError && <p className="member-error">{text.error}: {passwordError}</p>}
              <button className="member-primary" type="submit" disabled={passwordSubmitting}>{text.portalPasswordSubmit}</button>
            </form>
          </section>
        )}

        {status === 'ready' && current && !mode && (
          <section className="share-portal-panel">
            <h1>{text.portalUnavailable}</h1>
            <p>{loadError || text.portalUnavailableDetail}</p>
          </section>
        )}

        {status === 'ready' && current && mode === 'file' && (
          <section className="share-portal-panel share-portal-file">
            <h1>{rootLabel}</h1>
            {!current.allowDownload && <p className="member-readonly">{text.portalDownloadDisabledHint}</p>}
            <div className="share-portal-file-actions">
              {current.allowPreview && canPreview(filePreviewKind) ? (
                <a className="member-primary" href={sharePortalDownloadURL('', true)} target="_blank" rel="noreferrer">{text.portalPreview}</a>
              ) : current.allowPreview ? (
                <span className="member-error">{text.portalPreviewUnavailable}</span>
              ) : null}
              {current.allowDownload && <a href={sharePortalDownloadURL('')}>{text.portalDownload}</a>}
            </div>
          </section>
        )}

        {status === 'ready' && current && mode === 'directory' && (
          <section className="share-portal-panel share-portal-browser">
            <h1>{rootLabel}</h1>
            <div className="member-crumbs">
              <button type="button" onClick={() => void loadChildren('.')}>{text.portalBackToRoot}</button>
              {crumbs.map((part, index) => (
                <span key={`${part}-${index}`}><b>/</b><button type="button" onClick={() => void loadChildren(crumbs.slice(0, index + 1).join('/'))}>{part}</button></span>
              ))}
            </div>
            {!current.allowDownload && <p className="member-readonly">{text.portalDownloadDisabledHint}</p>}
            {loading ? (
              <div className="member-loading">{text.loading}</div>
            ) : entries.length === 0 ? (
              <div className="member-empty">{text.portalEmpty}</div>
            ) : (
              <table className="member-file-table">
                <thead><tr><th>{text.name}</th><th>{text.size}</th><th>{text.modified}</th><th>{text.actions}</th></tr></thead>
                <tbody>{entries.map((entry) => {
                  const entryPath = childPath(path, entry.name);
                  return (
                    <tr key={entryPath}>
                      <td>
                        <div className="member-file-name">
                          <span className={`member-file-icon ${entry.kind}`}>{entry.kind === 'dir' ? 'DIR' : entry.name.split('.').pop()?.slice(0, 3).toUpperCase() || 'FILE'}</span>
                          {entry.kind === 'dir' ? <button type="button" onClick={() => void loadChildren(entryPath)}>{entry.name}</button> : <span>{entry.name}</span>}
                        </div>
                      </td>
                      <td>{entry.kind === 'dir' ? '--' : formatBytes(entry.size)}</td>
                      <td>{entry.modifiedAt ?? '--'}</td>
                      <td className="member-file-actions">
                        {entry.kind === 'dir' ? (
                          <button type="button" onClick={() => void loadChildren(entryPath)}>{text.open}</button>
                        ) : (
                          <>
                            {current.allowPreview && canPreview(entry.previewKind) ? (
                              <a href={sharePortalDownloadURL(entryPath, true)} target="_blank" rel="noreferrer">{text.portalPreview}</a>
                            ) : (
                              current.allowPreview ? <span className="member-error">{text.portalPreviewUnavailable}</span> : null
                            )}
                            {current.allowDownload && <a href={sharePortalDownloadURL(entryPath)}>{text.portalDownload}</a>}
                          </>
                        )}
                      </td>
                    </tr>
                  );
                })}</tbody>
              </table>
            )}
          </section>
        )}
      </div>
    </main>
  );
}
