import { type ChangeEvent, type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ApiError,
  cancelUpload,
  completeUpload,
  createDirectory,
  createUpload,
  downloadURL,
  getSession,
  getUpload,
  listDirectoryChildren,
  listMounts,
  listSpaces,
  login,
  logout,
  searchSpace,
  uploadPart,
} from '../api';
import { localeMessages } from './i18n';
import { formatDirectoryChildren, type MemberDirectoryEntry, type MemberMount, type MemberSearchResult, type MemberSpace, type TransferItem } from './types';
import { resumedUploadProgress, uploadStorageKey } from './uploadQueue';
import { useLocale } from './useLocale';
import './member-files.css';

type SessionState = 'checking' | 'signed-out' | 'ready';
type ViewMode = 'list' | 'grid';

function formatBytes(bytes: number, locale: string) {
  if (!Number.isFinite(bytes) || bytes <= 0) return bytes === 0 ? '0 B' : '--';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: index ? 1 : 0 }).format(bytes / 1024 ** index)} ${units[index]}`;
}

function formatDate(value: string, locale: string) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? '--' : new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
}

function describeError(error: unknown) {
  if (error instanceof ApiError) return `HTTP ${error.status}`;
  return error instanceof Error ? error.message : 'Unknown error';
}

function breadcrumbs(relativePath: string) {
  return relativePath === '.' ? [] : relativePath.split('/').filter(Boolean);
}

export default function MemberFilesApp() {
  const { locale, setLocale } = useLocale();
  const text = localeMessages[locale];
  const [sessionState, setSessionState] = useState<SessionState>('checking');
  const [loginForm, setLoginForm] = useState({ login: '', password: '', totpCode: '' });
  const [spaces, setSpaces] = useState<MemberSpace[]>([]);
  const [mounts, setMounts] = useState<MemberMount[]>([]);
  const [activeSpaceId, setActiveSpaceId] = useState('');
  const [activeMountId, setActiveMountId] = useState('');
  const [relativePath, setRelativePath] = useState('.');
  const [entries, setEntries] = useState<MemberDirectoryEntry[]>([]);
  const [readOnly, setReadOnly] = useState(true);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [searchQuery, setSearchQuery] = useState('');
  const [searchResults, setSearchResults] = useState<MemberSearchResult[] | null>(null);
  const [searchNextCursor, setSearchNextCursor] = useState('');
  const [viewMode, setViewMode] = useState<ViewMode>('list');
  const [newFolderOpen, setNewFolderOpen] = useState(false);
  const [folderName, setFolderName] = useState('');
  const [transfers, setTransfers] = useState<TransferItem[]>([]);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const controllersRef = useRef(new Map<string, AbortController>());
  const pendingDirectoryPathRef = useRef<string | null>(null);

  const activeSpace = useMemo(() => spaces.find((space) => space.id === activeSpaceId) ?? null, [activeSpaceId, spaces]);
  const activeMount = useMemo(() => mounts.find((mount) => mount.id === activeMountId) ?? null, [activeMountId, mounts]);

  const refreshDirectory = useCallback(async (spaceId: string, mountId: string, path: string) => {
    if (!spaceId || !mountId) return;
    setLoading(true);
    setError('');
    try {
      const listing = formatDirectoryChildren(await listDirectoryChildren(spaceId, mountId, path));
      setRelativePath(listing.relativePath);
      setEntries(listing.entries);
      setReadOnly(listing.readOnly);
    } catch (caught) {
      setEntries([]);
      setError(describeError(caught));
      if (caught instanceof ApiError && caught.status === 401) setSessionState('signed-out');
    } finally {
      setLoading(false);
    }
  }, []);

  const loadSpaces = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const response = await listSpaces();
      setSpaces(response.items);
      setActiveSpaceId((current) => response.items.some((space) => space.id === current) ? current : (response.items[0]?.id ?? ''));
    } catch (caught) {
      setError(describeError(caught));
      if (caught instanceof ApiError && caught.status === 401) setSessionState('signed-out');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void (async () => {
      try {
        await getSession();
        setSessionState('ready');
        await loadSpaces();
      } catch {
        setSessionState('signed-out');
      }
    })();
  }, [loadSpaces]);

  useEffect(() => {
    if (!activeSpaceId || sessionState !== 'ready') return;
    void (async () => {
      setLoading(true);
      setError('');
      try {
        const response = await listMounts(activeSpaceId);
        setMounts(response.items);
        setActiveMountId((current) => response.items.some((mount) => mount.id === current) ? current : (response.items[0]?.id ?? ''));
        setRelativePath('.');
        setSearchResults(null);
        setSearchNextCursor('');
      } catch (caught) {
        setError(describeError(caught));
      } finally {
        setLoading(false);
      }
    })();
  }, [activeSpaceId, sessionState]);

  useEffect(() => {
    if (!activeMountId || sessionState !== 'ready') return;
    const initialPath = pendingDirectoryPathRef.current ?? '.';
    pendingDirectoryPathRef.current = null;
    void refreshDirectory(activeSpaceId, activeMountId, initialPath);
  }, [activeMountId, activeSpaceId, refreshDirectory, sessionState]);

  async function onLogin(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError('');
    try {
      await login({ login: loginForm.login, password: loginForm.password, totpCode: loginForm.totpCode || undefined });
      setSessionState('ready');
      await loadSpaces();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onLogout() {
    await logout().catch(() => undefined);
    setSessionState('signed-out');
    setSpaces([]);
    setMounts([]);
    setEntries([]);
  }

  function openDirectory(path: string, mountId = activeMountId) {
    setSearchResults(null);
    setSearchNextCursor('');
    if (mountId !== activeMountId) {
      pendingDirectoryPathRef.current = path;
      setActiveMountId(mountId);
      return;
    }
    void refreshDirectory(activeSpaceId, mountId, path);
  }

  async function onSearch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!activeSpaceId || !searchQuery.trim()) return;
    setLoading(true);
    setError('');
    try {
      const result = await searchSpace(activeSpaceId, searchQuery.trim());
      setSearchResults(result.items ?? []);
      setSearchNextCursor(result.nextCursor ?? '');
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function loadMoreSearchResults() {
    if (!activeSpaceId || !searchQuery.trim() || !searchNextCursor) return;
    setLoading(true);
    setError('');
    try {
      const result = await searchSpace(activeSpaceId, searchQuery.trim(), 50, searchNextCursor);
      setSearchResults((current) => [...(current ?? []), ...(result.items ?? [])]);
      setSearchNextCursor(result.nextCursor ?? '');
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onCreateFolder(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!activeSpaceId || !activeMountId || !folderName.trim()) return;
    setLoading(true);
    setError('');
    try {
      await createDirectory(activeSpaceId, activeMountId, { parentPath: relativePath, name: folderName.trim() });
      setFolderName('');
      setNewFolderOpen(false);
      await refreshDirectory(activeSpaceId, activeMountId, relativePath);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  function patchTransfer(id: string, patch: Partial<TransferItem>) {
    setTransfers((current) => current.map((transfer) => transfer.id === id ? { ...transfer, ...patch } : transfer));
  }

  async function uploadFile(file: File) {
    if (!activeSpaceId || !activeMountId || readOnly) return;
    const transferId = crypto.randomUUID();
    const controller = new AbortController();
    controllersRef.current.set(transferId, controller);
    setTransfers((current) => [{ id: transferId, name: file.name, progress: 0, state: 'queued' }, ...current]);
    const storageKey = uploadStorageKey(activeSpaceId, activeMountId, relativePath, file);

    try {
      let uploadId = '';
      let partSize = 32 * 1024;
      const saved = window.localStorage.getItem(storageKey);
      if (saved) {
        try {
          const state = JSON.parse(saved) as { uploadId?: string };
          if (state.uploadId) {
            const existing = await getUpload(state.uploadId, controller.signal);
            uploadId = existing.id ?? state.uploadId;
            partSize = existing.partSize ?? partSize;
          }
        } catch {
          window.localStorage.removeItem(storageKey);
        }
      }
      if (!uploadId) {
        const created = await createUpload({ spaceId: activeSpaceId, mountId: activeMountId, parentPath: relativePath, fileName: file.name, size: file.size }, controller.signal);
        uploadId = created.id ?? '';
        partSize = created.partSize ?? partSize;
        if (!uploadId) throw new Error('Upload session ID is missing');
        window.localStorage.setItem(storageKey, JSON.stringify({ uploadId }));
      }

      patchTransfer(transferId, { uploadId });

      const resumed = await getUpload(uploadId, controller.signal);
      const uploaded = new Set((resumed.parts ?? []).map((part) => Number(part.Number ?? part.number ?? 0)));
      const totalParts = Math.ceil(file.size / partSize);
      patchTransfer(transferId, {
        progress: resumedUploadProgress(resumed.parts ?? [], file.size),
        state: 'uploading',
        detail: `${formatBytes(file.size, locale)}`,
      });
      for (let number = 1; number <= totalParts; number += 1) {
        if (uploaded.has(number)) continue;
        const start = (number - 1) * partSize;
        await uploadPart(uploadId, number, file.slice(start, Math.min(start + partSize, file.size)), controller.signal);
        patchTransfer(transferId, { progress: Math.round((Math.min(number * partSize, file.size) / Math.max(file.size, 1)) * 100) });
      }
      await completeUpload(uploadId, controller.signal);
      window.localStorage.removeItem(storageKey);
      patchTransfer(transferId, { progress: 100, state: 'completed' });
      await refreshDirectory(activeSpaceId, activeMountId, relativePath);
    } catch (caught) {
      if (controller.signal.aborted) {
        patchTransfer(transferId, { state: 'cancelled' });
      } else {
        patchTransfer(transferId, { state: 'failed', detail: describeError(caught) });
      }
    } finally {
      controllersRef.current.delete(transferId);
    }
  }

  function onFileInput(event: ChangeEvent<HTMLInputElement>) {
    const files = [...(event.target.files ?? [])];
    event.target.value = '';
    void files.reduce(async (previous, file) => { await previous; await uploadFile(file); }, Promise.resolve());
  }

  async function cancelTransfer(transfer: TransferItem) {
    controllersRef.current.get(transfer.id)?.abort();
    patchTransfer(transfer.id, { state: 'cancelled' });
    if (transfer.uploadId) await cancelUpload(transfer.uploadId).catch(() => undefined);
  }

  if (sessionState === 'checking') {
    return <main className="member-auth-state">{text.loading}</main>;
  }

  if (sessionState === 'signed-out') {
    return (
      <main className="member-auth-state">
        <section className="member-login-panel">
          <div className="member-brand"><span>O</span>Omnora</div>
          <h1>{text.signIn}</h1>
          <p>{text.myFiles}</p>
          <form onSubmit={onLogin}>
            <label>{text.email}<input value={loginForm.login} onChange={(event) => setLoginForm({ ...loginForm, login: event.target.value })} autoComplete="username" required /></label>
            <label>{text.password}<input type="password" value={loginForm.password} onChange={(event) => setLoginForm({ ...loginForm, password: event.target.value })} autoComplete="current-password" required /></label>
            <label>{text.verificationCode}<input value={loginForm.totpCode} onChange={(event) => setLoginForm({ ...loginForm, totpCode: event.target.value })} inputMode="numeric" autoComplete="one-time-code" /></label>
            {error && <p className="member-error">{text.error}: {error}</p>}
            <button className="member-primary" type="submit" disabled={loading}>{text.signInAction}</button>
          </form>
          <div className="member-language-auth"><button type="button" onClick={() => setLocale('zh-CN')} aria-pressed={locale === 'zh-CN'}>中文</button><button type="button" onClick={() => setLocale('en-US')} aria-pressed={locale === 'en-US'}>EN</button></div>
        </section>
      </main>
    );
  }

  const visibleEntries: MemberDirectoryEntry[] = searchResults === null ? entries : searchResults.map((item) => ({
    mountId: item.mountId,
    name: item.name,
    relativePath: item.relativePath,
    kind: item.kind,
    size: item.sizeBytes ?? 0,
    modifiedAt: item.modifiedAt ?? '',
    readOnly: mounts.find((mount) => mount.id === item.mountId)?.mode === 'read-only',
    previewKind: item.previewKind ?? 'unknown',
    mountName: mounts.find((mount) => mount.id === item.mountId)?.name ?? item.mountId,
  }));
  const crumbItems = breadcrumbs(relativePath);

  return (
    <main className="member-app">
      <header className="member-topbar">
        <div className="member-brand"><span>O</span>Omnora</div>
        <form className="member-search" onSubmit={onSearch}><input value={searchQuery} onChange={(event) => setSearchQuery(event.target.value)} placeholder={text.searchPlaceholder} /><button type="submit">{text.search}</button></form>
        <div className="member-top-actions"><div className="member-language" aria-label={text.language}><button type="button" onClick={() => setLocale('zh-CN')} aria-pressed={locale === 'zh-CN'}>中文</button><button type="button" onClick={() => setLocale('en-US')} aria-pressed={locale === 'en-US'}>EN</button></div><button className="member-account" type="button" onClick={onLogout}>{text.signOut}</button></div>
      </header>

      <div className="member-layout">
        <aside className="member-sidebar">
          <div className="member-sidebar-section"><p>{text.spaces}</p>{spaces.map((space) => <button className={`member-space ${space.id === activeSpaceId ? 'selected' : ''}`} key={space.id} type="button" onClick={() => setActiveSpaceId(space.id)}>{space.name}<small>{space.role}</small></button>)}</div>
          <div className="member-sidebar-section"><p>{text.mounts}</p>{mounts.map((mount) => <button className={`member-mount ${mount.id === activeMountId ? 'selected' : ''}`} key={mount.id} type="button" onClick={() => setActiveMountId(mount.id)}><span>{mount.name}</span><small>{mount.mode === 'read-only' ? text.readOnly : text.readWrite}</small></button>)}</div>
        </aside>

        <section className="member-content">
          <div className="member-crumbs"><button type="button" onClick={() => openDirectory('.')}>{activeSpace?.name ?? text.myFiles}</button>{crumbItems.map((part, index) => <span key={`${part}-${index}`}><b>/</b><button type="button" onClick={() => openDirectory(crumbItems.slice(0, index + 1).join('/'))}>{part}</button></span>)}</div>
          <div className="member-heading"><div><h1>{searchResults === null ? text.myFiles : `${text.search}: ${searchQuery}`}</h1><p>{activeMount ? `${activeMount.name} · ${readOnly ? text.readOnly : text.readWrite}` : text.noMount}</p></div><div className="member-view-toggle"><button type="button" aria-pressed={viewMode === 'list'} onClick={() => setViewMode('list')}>{text.list}</button><button type="button" aria-pressed={viewMode === 'grid'} onClick={() => setViewMode('grid')}>{text.grid}</button></div></div>
          <div className="member-toolbar"><button className="member-primary" type="button" disabled={readOnly || !activeMountId} onClick={() => fileInputRef.current?.click()}>{text.upload}</button><button type="button" disabled={readOnly || !activeMountId} onClick={() => setNewFolderOpen(true)}>{text.newFolder}</button>{searchResults !== null && <button type="button" onClick={() => { setSearchResults(null); setSearchNextCursor(''); setSearchQuery(''); }}>{text.clearSearch}</button>}<span className="member-toolbar-spacer" /><button type="button" onClick={() => void refreshDirectory(activeSpaceId, activeMountId, relativePath)} disabled={loading || !activeMountId}>{text.refresh}</button><input ref={fileInputRef} type="file" multiple hidden onChange={onFileInput} /></div>
          {readOnly && activeMount && <p className="member-readonly">{text.uploadBlocked}</p>}
          {searchResults !== null && <p className="member-search-scope">{text.searchScope}</p>}
          {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
          {loading ? <div className="member-loading">{text.loading}</div> : visibleEntries.length === 0 ? <div className="member-empty">{spaces.length === 0 ? text.noSpaces : activeMount ? text.emptyFolder : text.noMount}</div> : viewMode === 'list' ? (
            <table className="member-file-table">
              <thead><tr><th>{text.name}</th><th>{text.size}</th><th>{text.modified}</th><th>{text.actions}</th></tr></thead>
              <tbody>{visibleEntries.map((entry) => (
                <tr key={`${entry.kind}-${entry.mountId ?? activeMountId}-${entry.relativePath}`}>
                  <td><div className="member-file-name"><span className={`member-file-icon ${entry.kind}`}>{entry.kind === 'dir' ? 'DIR' : entry.name.split('.').pop()?.slice(0, 3).toUpperCase() || 'FILE'}</span>{entry.kind === 'dir' ? <button type="button" onClick={() => openDirectory(entry.relativePath, entry.mountId)}>{entry.name}</button> : <span>{entry.name}</span>}{searchResults !== null && <small>{entry.mountName}</small>}</div></td>
                  <td>{entry.kind === 'dir' ? '--' : formatBytes(entry.size, locale)}</td>
                  <td>{formatDate(entry.modifiedAt, locale)}</td>
                  <td>{entry.kind === 'dir' ? <button type="button" onClick={() => openDirectory(entry.relativePath, entry.mountId)}>{text.open}</button> : <a href={downloadURL(activeSpaceId, entry.mountId ?? activeMountId, entry.relativePath)}>{text.download}</a>}</td>
                </tr>
              ))}</tbody>
            </table>
          ) : (
            <div className="member-file-grid">{visibleEntries.map((entry) => (
              <article key={`${entry.kind}-${entry.mountId ?? activeMountId}-${entry.relativePath}`}>
                <span className={`member-file-icon ${entry.kind}`}>{entry.kind === 'dir' ? 'DIR' : entry.name.split('.').pop()?.slice(0, 3).toUpperCase() || 'FILE'}</span>
                <strong>{entry.name}</strong>
                <small>{entry.kind === 'dir' ? '--' : formatBytes(entry.size, locale)}</small>
                {searchResults !== null && <small>{entry.mountName}</small>}
                {entry.kind === 'dir' ? <button type="button" onClick={() => openDirectory(entry.relativePath, entry.mountId)}>{text.open}</button> : <a className="member-grid-download" href={downloadURL(activeSpaceId, entry.mountId ?? activeMountId, entry.relativePath)}>{text.download}</a>}
              </article>
            ))}</div>
          )}
          {searchResults !== null && searchNextCursor && <button className="member-load-more" type="button" onClick={() => void loadMoreSearchResults()} disabled={loading}>{text.loadMore}</button>}
        </section>
      </div>

      {transfers.length > 0 && <aside className="member-transfers"><div><strong>{text.activeTransfers}</strong><button type="button" onClick={() => setTransfers((items) => items.filter((item) => item.state === 'uploading' || item.state === 'queued'))}>{text.clearCompleted}</button></div>{transfers.map((transfer) => <div className="member-transfer" key={transfer.id}><span>{transfer.name}</span><progress value={transfer.progress} max="100" /><small>{transfer.state === 'failed' ? `${text.uploadFailed}: ${transfer.detail ?? ''}` : transfer.state === 'completed' ? text.uploadComplete : `${text.uploadProgress} ${transfer.progress}%`}</small>{(transfer.state === 'failed' || transfer.state === 'cancelled') && <small>{text.resumeUploadHint}</small>}{(transfer.state === 'uploading' || transfer.state === 'queued') && <button type="button" onClick={() => void cancelTransfer(transfer)}>{text.cancel}</button>}</div>)}</aside>}

      {newFolderOpen && <div className="member-modal-backdrop"><form className="member-modal" onSubmit={onCreateFolder}><h2>{text.newFolder}</h2><label>{text.folderName}<input autoFocus value={folderName} onChange={(event) => setFolderName(event.target.value)} required /></label><div><button type="button" onClick={() => setNewFolderOpen(false)}>{text.cancel}</button><button className="member-primary" type="submit" disabled={loading}>{text.create}</button></div></form></div>}
    </main>
  );
}
