import { type ChangeEvent, type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ApiError,
  type CreateShareResponse,
  cancelUpload,
  completeUpload,
  createDirectory,
  createUpload,
  deleteObject,
  downloadURL,
  getPreferences,
  getSession,
  getUpload,
  listDirectoryChildren,
  listMounts,
  listSpaces,
  login,
  logout,
  previewURL,
  renameObject,
  searchSpace,
  uploadPart,
} from '../api';
import { localeMessages } from './i18n';
import AdminWorkspace, { type AdminTab } from './AdminWorkspace';
import MemberSharesPanel, { ShareCreateModal, ShareCreatedResult } from './MemberSharesPanel';
import MemberTokensPanel from './MemberTokensPanel';
import MemberAccountPanel, { applyThemePreference } from './MemberAccountPanel';
import { formatDirectoryChildren, type MemberDirectoryEntry, type MemberMount, type MemberSearchResult, type MemberSpace, type TransferItem } from './types';
import { resumedUploadProgress, uploadStorageKey } from './uploadQueue';
import { createClientId } from './clientId';
import { useLocale } from './useLocale';
import './member-files.css';

type MemberTab = 'files' | 'shares' | 'tokens' | 'account';

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
  if (error instanceof ApiError) {
    const body = error.body as { error?: { message?: string; code?: string } } | undefined;
    const message = body?.error?.message?.trim();
    if (message) return `HTTP ${error.status}: ${message}`;
    if (body?.error?.code) return `HTTP ${error.status}: ${body.error.code}`;
    return `HTTP ${error.status}`;
  }
  return error instanceof Error ? error.message : 'Unknown error';
}

function breadcrumbs(relativePath: string) {
  return relativePath === '.' ? [] : relativePath.split('/').filter(Boolean);
}

function normalizeParentPath(path: string) {
  const trimmed = path.trim();
  return !trimmed || trimmed === '.' ? '.' : trimmed.replace(/^\/+|\/+$/g, '');
}

function canPreview(previewKind: string) {
  return previewKind === 'image' || previewKind === 'pdf' || previewKind === 'media' || previewKind === 'text' || previewKind === 'markdown';
}

function isAudioName(name: string) {
  return /\.(aac|flac|m4a|mp3|ogg|wav)$/i.test(name);
}

type PreviewTarget = {
  name: string;
  mountId: string;
  relativePath: string;
  previewKind: string;
};

type MemberFilesAppProps = {
  entry?: 'member' | 'admin';
};

export default function MemberFilesApp({ entry = 'member' }: MemberFilesAppProps) {
  const { locale, setLocale } = useLocale();
  const text = localeMessages[locale];
  const [sessionState, setSessionState] = useState<SessionState>('checking');
  const [isAdmin, setIsAdmin] = useState(false);
  const [activeTab, setActiveTab] = useState<MemberTab | AdminTab>(entry === 'admin' ? 'overview' : 'files');
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
  const [preview, setPreview] = useState<PreviewTarget | null>(null);
  const [previewFailed, setPreviewFailed] = useState(false);
  const [previewText, setPreviewText] = useState<string | null>(null);
  const [previewBlobUrl, setPreviewBlobUrl] = useState<string | null>(null);
  const [shareTarget, setShareTarget] = useState<{ mountId: string; relativePath: string; name: string } | null>(null);
  const [shareCreatedResult, setShareCreatedResult] = useState<CreateShareResponse | null>(null);
  const [renameTarget, setRenameTarget] = useState<MemberDirectoryEntry | null>(null);
  const [renameValue, setRenameValue] = useState('');
  const [renameBusy, setRenameBusy] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<MemberDirectoryEntry | null>(null);
  const [deleteBusy, setDeleteBusy] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const controllersRef = useRef(new Map<string, AbortController>());
  const pendingDirectoryPathRef = useRef<string | null>(null);

  const activeSpace = useMemo(() => spaces.find((space) => space.id === activeSpaceId) ?? null, [activeSpaceId, spaces]);
  const activeMount = useMemo(() => mounts.find((mount) => mount.id === activeMountId) ?? null, [activeMountId, mounts]);
  const mountUnavailable = activeMount?.health === 'unavailable' || activeMount?.health === 'disabled';
  const writeBlocked = readOnly || mountUnavailable;

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
        const session = await getSession();
        setIsAdmin(session.isAdmin === true);
        setSessionState('ready');
        await loadSpaces();
        try {
          const preferences = await getPreferences();
          applyThemePreference(preferences.theme);
        } catch {
          // Preference storage may be unavailable; keep the default theme.
        }
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
        setActiveMountId((current) => {
          if (response.items.some((mount) => mount.id === current)) return current;
          const active = response.items.find((mount) => mount.health === 'active');
          return active?.id ?? response.items[0]?.id ?? '';
        });
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
      const session = await login({ login: loginForm.login, password: loginForm.password, totpCode: loginForm.totpCode || undefined });
      setIsAdmin(session.isAdmin === true);
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
    setIsAdmin(false);
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

  function openPreview(entry: MemberDirectoryEntry) {
    const mountId = entry.mountId ?? activeMountId;
    if (!mountId || !canPreview(entry.previewKind)) return;
    setPreviewFailed(false);
    setPreviewText(null);
    setPreviewBlobUrl(null);
    setPreview({
      name: entry.name,
      mountId,
      relativePath: entry.relativePath,
      previewKind: entry.previewKind,
    });
  }

  useEffect(() => {
    if (!preview) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setPreview(null);
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [preview]);

  useEffect(() => {
    if (!preview || !activeSpaceId) {
      setPreviewText(null);
      setPreviewBlobUrl((current) => {
        if (current) URL.revokeObjectURL(current);
        return null;
      });
      return;
    }
    if (preview.previewKind === 'image' || preview.previewKind === 'media') {
      setPreviewText(null);
      setPreviewBlobUrl((current) => {
        if (current) URL.revokeObjectURL(current);
        return null;
      });
      return;
    }

    let cancelled = false;
    let objectUrl: string | null = null;
    const url = previewURL(activeSpaceId, preview.mountId, preview.relativePath);
    setPreviewFailed(false);
    setPreviewText(null);
    setPreviewBlobUrl((current) => {
      if (current) URL.revokeObjectURL(current);
      return null;
    });

    void (async () => {
      try {
        const response = await fetch(url, { credentials: 'same-origin' });
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        if (preview.previewKind === 'text' || preview.previewKind === 'markdown') {
          const text = await response.text();
          if (!cancelled) setPreviewText(text);
          return;
        }
        const blob = await response.blob();
        objectUrl = URL.createObjectURL(blob);
        if (cancelled) {
          URL.revokeObjectURL(objectUrl);
          return;
        }
        setPreviewBlobUrl(objectUrl);
      } catch {
        if (!cancelled) setPreviewFailed(true);
      }
    })();

    return () => {
      cancelled = true;
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [preview, activeSpaceId]);

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

  function openRename(entry: MemberDirectoryEntry) {
    setError('');
    setRenameTarget(entry);
    setRenameValue(entry.name);
  }

  async function onRename(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!renameTarget || !renameValue.trim()) return;
    const mountId = renameTarget.mountId ?? activeMountId;
    setRenameBusy(true);
    setError('');
    try {
      await renameObject(activeSpaceId, mountId, { from: renameTarget.relativePath, toName: renameValue.trim() });
      setRenameTarget(null);
      setRenameValue('');
      await refreshDirectory(activeSpaceId, activeMountId, relativePath);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setRenameBusy(false);
    }
  }

  function openDelete(entry: MemberDirectoryEntry) {
    setError('');
    setDeleteTarget(entry);
  }

  async function onDeleteConfirmed() {
    if (!deleteTarget) return;
    const mountId = deleteTarget.mountId ?? activeMountId;
    setDeleteBusy(true);
    setError('');
    try {
      await deleteObject(activeSpaceId, mountId, deleteTarget.relativePath);
      setDeleteTarget(null);
      await refreshDirectory(activeSpaceId, activeMountId, relativePath);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setDeleteBusy(false);
    }
  }

  function openShareForEntry(entry: MemberDirectoryEntry) {
    setShareTarget({ mountId: entry.mountId ?? activeMountId, relativePath: entry.relativePath, name: entry.name });
  }

  function openShareForCurrentPath() {
    if (!activeMountId) return;
    setShareTarget({ mountId: activeMountId, relativePath, name: activeSpace?.name ? `${activeSpace.name} / ${relativePath === '.' ? text.myFiles : relativePath}` : relativePath });
  }

  function patchTransfer(id: string, patch: Partial<TransferItem>) {
    setTransfers((current) => current.map((transfer) => transfer.id === id ? { ...transfer, ...patch } : transfer));
  }

  async function uploadFile(file: File) {
    if (!activeSpaceId || !activeMountId) {
      setError(text.noMount);
      return;
    }
    if (readOnly) {
      setError(text.uploadBlocked);
      return;
    }
    const transferId = createClientId();
    const controller = new AbortController();
    controllersRef.current.set(transferId, controller);
    const parentPath = normalizeParentPath(relativePath);
    setError('');
    setTransfers((current) => [{ id: transferId, name: file.name, progress: 0, state: 'queued' }, ...current]);
    const storageKey = uploadStorageKey(activeSpaceId, activeMountId, parentPath, file);

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
        const created = await createUpload({ spaceId: activeSpaceId, mountId: activeMountId, parentPath, fileName: file.name, size: file.size }, controller.signal);
        uploadId = created.id ?? '';
        partSize = created.partSize ?? partSize;
        if (!uploadId) throw new Error('Upload session ID is missing');
        window.localStorage.setItem(storageKey, JSON.stringify({ uploadId }));
      }

      patchTransfer(transferId, { uploadId });

      const resumed = await getUpload(uploadId, controller.signal);
      const uploaded = new Set((resumed.parts ?? []).map((part) => Number(part.Number ?? part.number ?? 0)));
      const totalParts = file.size === 0 ? 0 : Math.ceil(file.size / partSize);
      patchTransfer(transferId, {
        progress: resumedUploadProgress(resumed.parts ?? [], Math.max(file.size, 1)),
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
      await refreshDirectory(activeSpaceId, activeMountId, parentPath);
    } catch (caught) {
      if (controller.signal.aborted) {
        patchTransfer(transferId, { state: 'cancelled' });
      } else {
        const detail = describeError(caught);
        patchTransfer(transferId, { state: 'failed', detail });
        setError(detail);
      }
    } finally {
      controllersRef.current.delete(transferId);
    }
  }

  function onFileInput(event: ChangeEvent<HTMLInputElement>) {
    const files = [...(event.target.files ?? [])];
    event.target.value = '';
    if (files.length === 0) return;
    void files.reduce(async (previous, file) => {
      await previous;
      try {
        await uploadFile(file);
      } catch (caught) {
        setError(describeError(caught));
      }
    }, Promise.resolve());
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

  if (entry === 'admin' && !isAdmin) {
    return (
      <main className="member-app">
        <header className="member-topbar">
          <div className="member-brand"><span>O</span>Omnora</div>
          <div className="member-top-actions"><div className="member-language" aria-label={text.language}><button type="button" onClick={() => setLocale('zh-CN')} aria-pressed={locale === 'zh-CN'}>中文</button><button type="button" onClick={() => setLocale('en-US')} aria-pressed={locale === 'en-US'}>EN</button></div><button className="member-account" type="button" onClick={onLogout}>{text.signOut}</button></div>
        </header>
        <section className="member-no-access"><h1>{text.adminAccessDenied}</h1><p>{text.adminAccessDetail}</p><button className="member-primary" type="button" onClick={() => window.location.assign('/app')}>{text.goToFiles}</button></section>
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
  const previewSrc = preview ? previewURL(activeSpaceId, preview.mountId, preview.relativePath) : '';
  const previewDownload = preview ? downloadURL(activeSpaceId, preview.mountId, preview.relativePath) : '';
  const canManageShares = entry === 'member' && activeSpace?.role === 'manager';
  const canEditFiles = entry === 'member' && (activeSpace?.role === 'editor' || activeSpace?.role === 'manager');

  return (
    <main className="member-app">
      <header className="member-topbar">
        <div className="member-brand"><span>O</span>Omnora</div>
        {activeTab === 'files' && <form className="member-search" onSubmit={onSearch}><input value={searchQuery} onChange={(event) => setSearchQuery(event.target.value)} placeholder={text.searchPlaceholder} /><button type="submit">{text.search}</button></form>}
        <div className="member-top-actions"><div className="member-language" aria-label={text.language}><button type="button" onClick={() => setLocale('zh-CN')} aria-pressed={locale === 'zh-CN'}>中文</button><button type="button" onClick={() => setLocale('en-US')} aria-pressed={locale === 'en-US'}>EN</button></div><button className="member-account" type="button" onClick={onLogout}>{text.signOut}</button></div>
      </header>

      <div className="member-layout">
        <aside className="member-sidebar">
          {entry === 'admin' && <nav aria-label="Administrator workspace">
            <button className={`member-nav ${activeTab === 'overview' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('overview')}>{text.adminOverview}</button>
            <button className={`member-nav ${activeTab === 'users' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('users')}>{text.adminUsers}</button>
            <button className={`member-nav ${activeTab === 'spaces' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('spaces')}>{text.adminSpaces}</button>
            <button className={`member-nav ${activeTab === 'mounts' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('mounts')}>{text.adminMounts}</button>
            <button className={`member-nav ${activeTab === 'index-jobs' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('index-jobs')}>{text.adminIndexJobs}</button>
            <button className={`member-nav ${activeTab === 'emergency' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('emergency')}>{text.adminEmergency}</button>
            <button className={`member-nav ${activeTab === 'network' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('network')}>{text.adminNetwork}</button>
            <button className={`member-nav ${activeTab === 'route-groups' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('route-groups')}>{text.adminRouteGroups}</button>
            <button className={`member-nav ${activeTab === 'share-governance' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('share-governance')}>{text.adminShareGovernance}</button>
            <button className={`member-nav ${activeTab === 'token-governance' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('token-governance')}>{text.adminTokenGovernance}</button>
            <button className={`member-nav ${activeTab === 'backups' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('backups')}>{text.adminBackups}</button>
            <button className={`member-nav ${activeTab === 'audit' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('audit')}>{text.adminAudit}</button>
          </nav>}
          {entry === 'member' && <nav aria-label="Member workspace">
            <button className={`member-nav ${activeTab === 'files' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('files')}>{text.files}</button>
            <button className={`member-nav ${activeTab === 'shares' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('shares')}>{text.navShares}</button>
            <button className={`member-nav ${activeTab === 'tokens' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('tokens')}>{text.navTokens}</button>
            <button className={`member-nav ${activeTab === 'account' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('account')}>{text.account}</button>
          </nav>}
          {activeTab === 'files' && <><div className="member-sidebar-section"><p>{text.spaces}</p>{spaces.map((space) => <button className={`member-space ${space.id === activeSpaceId ? 'selected' : ''}`} key={space.id} type="button" onClick={() => setActiveSpaceId(space.id)}>{space.name}<small>{space.role}</small></button>)}</div>
          <div className="member-sidebar-section"><p>{text.mounts}</p>{mounts.map((mount) => <button className={`member-mount ${mount.id === activeMountId ? 'selected' : ''}`} key={mount.id} type="button" onClick={() => setActiveMountId(mount.id)}><span>{mount.name}</span><small>{mount.health === 'unavailable' ? text.statusUnavailable : mount.mode === 'read-only' ? text.readOnly : text.readWrite}</small></button>)}</div></>}
        </aside>

        <section className="member-content">
          {activeTab === 'files' ? <>
          <div className="member-crumbs"><button type="button" onClick={() => openDirectory('.')}>{activeSpace?.name ?? text.myFiles}</button>{crumbItems.map((part, index) => <span key={`${part}-${index}`}><b>/</b><button type="button" onClick={() => openDirectory(crumbItems.slice(0, index + 1).join('/'))}>{part}</button></span>)}</div>
          <div className="member-heading"><div><h1>{searchResults === null ? text.myFiles : `${text.search}: ${searchQuery}`}</h1><p>{activeMount ? `${activeMount.name} · ${mountUnavailable ? text.statusUnavailable : readOnly ? text.readOnly : text.readWrite}` : text.noMount}</p></div><div className="member-view-toggle"><button type="button" aria-pressed={viewMode === 'list'} onClick={() => setViewMode('list')}>{text.list}</button><button type="button" aria-pressed={viewMode === 'grid'} onClick={() => setViewMode('grid')}>{text.grid}</button></div></div>
          <div className="member-toolbar"><button className="member-primary" type="button" disabled={writeBlocked || !activeMountId} onClick={() => fileInputRef.current?.click()}>{text.upload}</button><button type="button" disabled={writeBlocked || !activeMountId} onClick={() => setNewFolderOpen(true)}>{text.newFolder}</button>{canManageShares && searchResults === null && <button type="button" disabled={!activeMountId} onClick={openShareForCurrentPath}>{text.shareAction}</button>}{searchResults !== null && <button type="button" onClick={() => { setSearchResults(null); setSearchNextCursor(''); setSearchQuery(''); }}>{text.clearSearch}</button>}<span className="member-toolbar-spacer" /><button type="button" onClick={() => void refreshDirectory(activeSpaceId, activeMountId, relativePath)} disabled={loading || !activeMountId || mountUnavailable}>{text.refresh}</button><input ref={fileInputRef} type="file" multiple hidden onChange={onFileInput} /></div>
          {mountUnavailable && activeMount && <p className="member-readonly">{text.mountUnavailableHint}</p>}
          {!mountUnavailable && readOnly && activeMount && <p className="member-readonly">{text.uploadBlocked}</p>}
          {searchResults !== null && <p className="member-search-scope">{text.searchScope}</p>}
          {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
          {loading ? <div className="member-loading">{text.loading}</div> : visibleEntries.length === 0 ? <div className="member-empty">{spaces.length === 0 ? text.noSpaces : activeMount ? text.emptyFolder : text.noMount}</div> : viewMode === 'list' ? (
            <table className="member-file-table">
              <thead><tr><th>{text.name}</th><th>{text.size}</th><th>{text.modified}</th><th>{text.actions}</th></tr></thead>
              <tbody>{visibleEntries.map((entry) => (
                <tr key={`${entry.kind}-${entry.mountId ?? activeMountId}-${entry.relativePath}`}>
                  <td><div className="member-file-name"><span className={`member-file-icon ${entry.kind}`}>{entry.kind === 'dir' ? 'DIR' : entry.name.split('.').pop()?.slice(0, 3).toUpperCase() || 'FILE'}</span>{entry.kind === 'dir' ? <button type="button" onClick={() => openDirectory(entry.relativePath, entry.mountId)}>{entry.name}</button> : canPreview(entry.previewKind) ? <button type="button" className="member-file-preview" onClick={() => openPreview(entry)}>{entry.name}</button> : <span>{entry.name}</span>}{searchResults !== null && <small>{entry.mountName}</small>}</div></td>
                  <td>{entry.kind === 'dir' ? '--' : formatBytes(entry.size, locale)}</td>
                  <td>{formatDate(entry.modifiedAt, locale)}</td>
                  <td><div className="member-file-actions">{entry.kind === 'dir' ? <button type="button" onClick={() => openDirectory(entry.relativePath, entry.mountId)}>{text.open}</button> : <>{canPreview(entry.previewKind) && <button type="button" onClick={() => openPreview(entry)}>{text.preview}</button>}<a href={downloadURL(activeSpaceId, entry.mountId ?? activeMountId, entry.relativePath)}>{text.download}</a></>}{canManageShares && searchResults === null && <button type="button" onClick={() => openShareForEntry(entry)}>{text.shareAction}</button>}{canEditFiles && !writeBlocked && !entry.readOnly && searchResults === null && <button type="button" onClick={() => openRename(entry)}>{text.rename}</button>}{canEditFiles && !writeBlocked && !entry.readOnly && searchResults === null && <button type="button" onClick={() => openDelete(entry)}>{text.deleteFile}</button>}</div></td>
                </tr>
              ))}</tbody>
            </table>
          ) : (
            <div className="member-file-grid">{visibleEntries.map((entry) => (
              <article key={`${entry.kind}-${entry.mountId ?? activeMountId}-${entry.relativePath}`}>
                <span className={`member-file-icon ${entry.kind}`}>{entry.kind === 'dir' ? 'DIR' : entry.name.split('.').pop()?.slice(0, 3).toUpperCase() || 'FILE'}</span>
                {entry.kind === 'dir' || canPreview(entry.previewKind) ? <button type="button" className={entry.kind === 'file' ? 'member-file-preview' : undefined} onClick={() => entry.kind === 'dir' ? openDirectory(entry.relativePath, entry.mountId) : openPreview(entry)}><strong>{entry.name}</strong></button> : <strong>{entry.name}</strong>}
                <small>{entry.kind === 'dir' ? '--' : formatBytes(entry.size, locale)}</small>
                {searchResults !== null && <small>{entry.mountName}</small>}
                <div className="member-file-actions">{entry.kind === 'dir' ? <button type="button" onClick={() => openDirectory(entry.relativePath, entry.mountId)}>{text.open}</button> : <>{canPreview(entry.previewKind) && <button type="button" onClick={() => openPreview(entry)}>{text.preview}</button>}<a className="member-grid-download" href={downloadURL(activeSpaceId, entry.mountId ?? activeMountId, entry.relativePath)}>{text.download}</a></>}{canManageShares && searchResults === null && <button type="button" onClick={() => openShareForEntry(entry)}>{text.shareAction}</button>}{canEditFiles && !writeBlocked && !entry.readOnly && searchResults === null && <button type="button" onClick={() => openRename(entry)}>{text.rename}</button>}{canEditFiles && !writeBlocked && !entry.readOnly && searchResults === null && <button type="button" onClick={() => openDelete(entry)}>{text.deleteFile}</button>}</div>
              </article>
            ))}</div>
          )}
          {searchResults !== null && searchNextCursor && <button className="member-load-more" type="button" onClick={() => void loadMoreSearchResults()} disabled={loading}>{text.loadMore}</button>}
          </> : activeTab === 'shares' ? <MemberSharesPanel locale={locale} />
            : activeTab === 'tokens' ? <MemberTokensPanel locale={locale} />
            : activeTab === 'account' ? <MemberAccountPanel locale={locale} />
            : <AdminWorkspace tab={activeTab as AdminTab} locale={locale} />}
        </section>
      </div>

      {transfers.length > 0 && <aside className="member-transfers"><div><strong>{text.activeTransfers}</strong><button type="button" onClick={() => setTransfers((items) => items.filter((item) => item.state === 'uploading' || item.state === 'queued'))}>{text.clearCompleted}</button></div>{transfers.map((transfer) => <div className="member-transfer" key={transfer.id}><span>{transfer.name}</span><progress value={transfer.progress} max="100" /><small>{transfer.state === 'failed' ? `${text.uploadFailed}: ${transfer.detail ?? ''}` : transfer.state === 'completed' ? text.uploadComplete : `${text.uploadProgress} ${transfer.progress}%`}</small>{(transfer.state === 'failed' || transfer.state === 'cancelled') && <small>{text.resumeUploadHint}</small>}{(transfer.state === 'uploading' || transfer.state === 'queued') && <button type="button" onClick={() => void cancelTransfer(transfer)}>{text.cancel}</button>}</div>)}</aside>}

      {newFolderOpen && <div className="member-modal-backdrop"><form className="member-modal" onSubmit={onCreateFolder}><h2>{text.newFolder}</h2><label>{text.folderName}<input autoFocus value={folderName} onChange={(event) => setFolderName(event.target.value)} required /></label><div><button type="button" onClick={() => setNewFolderOpen(false)}>{text.cancel}</button><button className="member-primary" type="submit" disabled={loading}>{text.create}</button></div></form></div>}

      {preview && <div className="member-preview-backdrop" onClick={() => setPreview(null)} role="presentation"><div className="member-preview-dialog" role="dialog" aria-modal="true" aria-label={preview.name} onClick={(event) => event.stopPropagation()}><div className="member-preview-toolbar"><strong>{preview.name}</strong><div className="member-preview-actions"><a className="member-preview-download" href={previewDownload}>{text.download}</a><button type="button" onClick={() => setPreview(null)}>{text.closePreview}</button></div></div><div className="member-preview-stage">{previewFailed ? <p className="member-preview-error">{text.previewFailed}</p> : preview.previewKind === 'image' ? <img src={previewSrc} alt={preview.name} onError={() => setPreviewFailed(true)} /> : preview.previewKind === 'media' ? (isAudioName(preview.name) ? <audio src={previewSrc} controls onError={() => setPreviewFailed(true)} /> : <video src={previewSrc} controls onError={() => setPreviewFailed(true)} />) : preview.previewKind === 'text' || preview.previewKind === 'markdown' ? (previewText === null ? <p className="member-preview-loading">{text.loading}</p> : <pre className="member-preview-text">{previewText}</pre>) : previewBlobUrl ? <iframe title={preview.name} src={previewBlobUrl} /> : <p className="member-preview-loading">{text.loading}</p>}</div></div></div>}

      {renameTarget && (
        <div className="member-modal-backdrop">
          <form className="member-modal" onSubmit={onRename}>
            <h2>{text.renameTitle}</h2>
            <label>{text.renameNewName}<input autoFocus value={renameValue} onChange={(event) => setRenameValue(event.target.value)} required /></label>
            {error && <p className="member-error">{text.error}: {error}</p>}
            <div><button type="button" onClick={() => setRenameTarget(null)}>{text.cancel}</button><button className="member-primary" type="submit" disabled={renameBusy || !renameValue.trim()}>{text.create}</button></div>
          </form>
        </div>
      )}

      {deleteTarget && (
        <div className="member-modal-backdrop">
          <div className="member-modal">
            <h2>{text.deleteConfirmTitle}</h2>
            <p className="member-modal-hint">{text.deleteConfirmDetail}</p>
            <p className="member-modal-hint"><strong>{deleteTarget.name}</strong></p>
            <div><button type="button" onClick={() => setDeleteTarget(null)}>{text.cancel}</button><button className="member-modal-danger" type="button" onClick={() => void onDeleteConfirmed()} disabled={deleteBusy}>{text.deleteFile}</button></div>
          </div>
        </div>
      )}

      {shareTarget && (
        <ShareCreateModal
          text={text}
          spaceId={activeSpaceId}
          mountId={shareTarget.mountId}
          relativePath={shareTarget.relativePath}
          targetLabel={shareTarget.name}
          onCancel={() => setShareTarget(null)}
          onCreated={(result) => {
            setShareTarget(null);
            setShareCreatedResult(result);
          }}
        />
      )}

      {shareCreatedResult && <ShareCreatedResult text={text} result={shareCreatedResult} onClose={() => setShareCreatedResult(null)} />}
    </main>
  );
}
