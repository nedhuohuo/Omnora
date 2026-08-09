import { type ChangeEvent, type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ApiError,
  type CreateShareResponse,
  cancelUpload,
  completeUpload,
  createDirectory,
  createUpload,
  crossMountCopy,
  crossMountMove,
  deleteObject,
  downloadURL,
  emptyTrash,
  getPreferences,
  getBootstrap,
  getSession,
  getUpload,
  initialize,
  listDirectoryChildren,
  listMounts,
  listSpaces,
  listTrash,
  login,
  logout,
  previewURL,
  purgeTrashItem,
  renameObject,
  restoreTrashItem,
  searchSpace,
  setupTOTP,
  confirmTOTP,
  uploadPart,
  moveObject,
  type InitializePayload,
  type TrashItemPayload,
} from '../api';
import { localeMessages } from './i18n';
import AdminWorkspace, { type AdminTab } from './AdminWorkspace';
import MemberSharesPanel, { ShareCreateModal, ShareCreatedResult } from './MemberSharesPanel';
import MemberTokensPanel from './MemberTokensPanel';
import MemberAccountPanel from './MemberAccountPanel';
import MemberDocsPanel from './MemberDocsPanel';
import MemberSpaceDirectory from './MemberSpaceDirectory';
import MemberStorageWorkspace from './MemberStorageWorkspace';
import MemberContentNavigation from './MemberContentNavigation';
import MarkdownPreview from './MarkdownPreview';
import FileTypeIcon from './FileTypeIcon';
import { syncAuthenticatedTheme } from './themeSync';
import { RecentReauthProvider } from './RecentReauthProvider';
import { stateForSession } from './sessionFlow';
import { formatDirectoryChildren, mountDeletePolicy, mountSupportsTrash, type MemberDirectoryEntry, type MemberMount, type MemberSearchResult, type MemberSpace, type TransferItem } from './types';
import { resumedUploadProgress, uploadStorageKey } from './uploadQueue';
import { createClientId } from './clientId';
import { useLocale } from './useLocale';
import { readableLabel } from './displayLabels';
import {
  categoryForSpace,
  categoryForTab,
  memberSpaceTabs,
  tabForCategory,
  type MemberSpaceCategory,
  type MemberSpaceTab,
} from './spaceNavigation';
import './member-files.css';

type MemberTab = MemberSpaceTab | 'files' | 'collaborations' | 'trash' | 'shares' | 'tokens' | 'docs' | 'account';
type AdminNavGroup = 'overview' | 'identity-space' | 'storage-search' | 'access-security' | 'backups';
type AdminNavItem = { id: AdminTab; label: string };
type AdminNavGroupItem = { id: AdminNavGroup; label: string; tabs: AdminNavItem[] };

type SessionState = 'checking' | 'signed-out' | 'enrollment' | 'ready';
type ViewMode = 'list' | 'grid';

const defaultAdminGroupTabs: Record<AdminNavGroup, AdminTab> = {
  overview: 'overview',
  'identity-space': 'users',
  'storage-search': 'mounts',
  'access-security': 'route-groups',
  backups: 'backups',
};

function adminGroupForTab(tab: AdminTab): AdminNavGroup {
  if (tab === 'users' || tab === 'spaces') return 'identity-space';
  if (tab === 'mounts' || tab === 'index-jobs') return 'storage-search';
  if (tab === 'route-groups' || tab === 'share-governance' || tab === 'token-governance' || tab === 'audit') return 'access-security';
  if (tab === 'backups') return 'backups';
  return 'overview';
}

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

// 将 markdown 文档内的相对资源路径改写为同目录文件的 inline 预览链接。
function markdownAssetURL(spaceId: string, mountId: string, mdPath: string, src: string): string {
  if (!src || /^(?:[a-z][a-z0-9+.-]*:|\/)/i.test(src)) return src;
  const dir = normalizeParentPath(mdPath).split('/').filter(Boolean).slice(0, -1).join('/');
  return previewURL(spaceId, mountId, [dir, src].filter(Boolean).join('/'));
}

type PreviewTarget = {
  name: string;
  mountId: string;
  relativePath: string;
  previewKind: string;
};

type FileOperation = 'move' | 'copy';

type MemberFilesAppProps = {
  entry?: 'member' | 'admin';
};

export default function MemberFilesApp({ entry = 'member' }: MemberFilesAppProps) {
  const { locale, setLocale } = useLocale();
  const text = localeMessages[locale];
  const [sessionState, setSessionState] = useState<SessionState>('checking');
  const [isAdmin, setIsAdmin] = useState(false);
  const [activeTab, setActiveTab] = useState<MemberTab | AdminTab>(entry === 'admin' ? 'overview' : 'files');
  const [adminGroupTabs, setAdminGroupTabs] = useState<Record<AdminNavGroup, AdminTab>>(defaultAdminGroupTabs);
  const [loginForm, setLoginForm] = useState({ login: '', password: '', totpCode: '' });
  const [enrollmentSetup, setEnrollmentSetup] = useState<{ secret: string; otpauthUri?: string } | null>(null);
  const [enrollmentCode, setEnrollmentCode] = useState('');
  const [setupMode, setSetupMode] = useState(false);
  const [initializationAvailable, setInitializationAvailable] = useState(false);
  const [setupForm, setSetupForm] = useState<InitializePayload>({ token: '', email: '', displayName: '', password: '' });
  const [setupNotice, setSetupNotice] = useState('');
  const [spaces, setSpaces] = useState<MemberSpace[]>([]);
  const [mounts, setMounts] = useState<MemberMount[]>([]);
  const [activeSpaceId, setActiveSpaceId] = useState('');
  const [activeMountId, setActiveMountId] = useState('');
  const [destinationSpaceId, setDestinationSpaceId] = useState('');
  const [destinationMounts, setDestinationMounts] = useState<MemberMount[]>([]);
  const [destinationMountId, setDestinationMountId] = useState('');
  const [destinationPath, setDestinationPath] = useState('.');
  const [relativePath, setRelativePath] = useState('.');
  const [entries, setEntries] = useState<MemberDirectoryEntry[]>([]);
  const [trashItems, setTrashItems] = useState<TrashItemPayload[]>([]);
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
  const [operationTarget, setOperationTarget] = useState<{ entry: MemberDirectoryEntry; operation: FileOperation } | null>(null);
  const [operationBusy, setOperationBusy] = useState(false);
  const [resumeSelectMode, setResumeSelectMode] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const controllersRef = useRef(new Map<string, AbortController>());
  const pendingDirectoryPathRef = useRef<string | null>(null);

  const activeSpace = useMemo(() => spaces.find((space) => space.id === activeSpaceId) ?? null, [activeSpaceId, spaces]);
  const activeSpaceTab = activeTab === 'personal-spaces' || activeTab === 'team-spaces' ? activeTab : null;
  const activeCategory = activeSpaceTab
    ? categoryForTab(activeSpaceTab)
    : activeSpace
      ? categoryForSpace(activeSpace)
      : null;
  const showingSpaceDirectory = activeSpaceTab !== null && activeSpace === null;
  const showingSpaceFiles = activeSpaceTab !== null && activeSpace !== null;
  const activeMount = useMemo(() => mounts.find((mount) => mount.id === activeMountId) ?? null, [activeMountId, mounts]);
  const activeMountSupportsTrash = mountSupportsTrash(activeMount);
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
      setActiveSpaceId((current) => response.items.some((space) => space.id === current && categoryForSpace(space) !== null) ? current : '');
    } catch (caught) {
      setError(describeError(caught));
      if (caught instanceof ApiError && caught.status === 401) setSessionState('signed-out');
    } finally {
      setLoading(false);
    }
  }, []);

  function clearSpaceBrowserState() {
    setActiveSpaceId('');
    setMounts([]);
    setActiveMountId('');
    setTrashItems([]);
    setEntries([]);
    setRelativePath('.');
    setSearchQuery('');
    setSearchResults(null);
    setSearchNextCursor('');
    setError('');
  }

  function openSpaceCategory(category: MemberSpaceCategory) {
    setActiveTab(tabForCategory(category));
    clearSpaceBrowserState();
  }

  function selectSpace(space: MemberSpace) {
    const category = categoryForSpace(space);
    if (!category) return;
    clearSpaceBrowserState();
    setActiveTab(tabForCategory(category));
    setActiveSpaceId(space.id);
  }

  function selectMount(mount: MemberMount) {
    setActiveMountId(mount.id);
    if (!mountSupportsTrash(mount)) {
      const category = activeSpace ? categoryForSpace(activeSpace) : null;
      if (category) setActiveTab(tabForCategory(category));
      setTrashItems([]);
      setError('');
    }
  }

  const refreshTrash = useCallback(async () => {
    if (!activeSpaceId || !activeMountId || !activeMountSupportsTrash) {
      setTrashItems([]);
      return;
    }
    setLoading(true);
    setError('');
    try {
      const response = await listTrash(activeSpaceId, activeMountId);
      setTrashItems(response.items ?? []);
    } catch (caught) {
      setTrashItems([]);
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, [activeMountId, activeMountSupportsTrash, activeSpaceId]);

  useEffect(() => {
    void (async () => {
      try {
        const session = await getSession();
        setIsAdmin(session.isAdmin === true);
        const nextState = stateForSession(session);
        setSessionState(nextState);
        if (nextState === 'ready') {
          await syncAuthenticatedTheme(getPreferences);
          if (entry === 'admin') {
            await loadSpaces();
          }
        }
      } catch {
        setSessionState('signed-out');
        try {
          const bootstrap = await getBootstrap();
          setInitializationAvailable(bootstrap.initializationAvailable === true);
        } catch {
          setInitializationAvailable(false);
        }
      }
    })();
  }, [entry, loadSpaces]);

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

  useEffect(() => {
    if (activeTab !== 'trash') return;
    if (!activeMountSupportsTrash) {
      const category = activeSpace ? categoryForSpace(activeSpace) : null;
      setActiveTab(category ? tabForCategory(category) : 'personal-spaces');
      if (!category) setActiveSpaceId('');
      setTrashItems([]);
      setError('');
      return;
    }
    if (sessionState === 'ready') void refreshTrash();
  }, [activeMountSupportsTrash, activeSpace, activeTab, refreshTrash, sessionState]);

  useEffect(() => {
    if (!operationTarget || !destinationSpaceId) {
      setDestinationMounts([]);
      return;
    }
    const controller = new AbortController();
    void listMounts(destinationSpaceId, controller.signal)
      .then((response) => {
        setDestinationMounts(response.items);
        setDestinationMountId((current) => response.items.some((mount) => mount.id === current) ? current : (response.items[0]?.id ?? ''));
      })
      .catch(() => {
        if (!controller.signal.aborted) setDestinationMounts([]);
      });
    return () => controller.abort();
  }, [destinationSpaceId, operationTarget]);

  async function onLogin(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError('');
    try {
      const session = await login({ login: loginForm.login, password: loginForm.password, totpCode: loginForm.totpCode || undefined });
      setIsAdmin(session.isAdmin === true);
      const nextState = stateForSession(session);
      setSessionState(nextState);
      if (nextState === 'ready') {
        await loadSpaces();
      }
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onStartEnrollment() {
    setLoading(true);
    setError('');
    try {
      const response = await setupTOTP();
      setEnrollmentSetup({ secret: response.secret ?? '', otpauthUri: response.otpauthUri });
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onConfirmEnrollment(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError('');
    try {
      await confirmTOTP(enrollmentCode.trim());
      const session = await getSession();
      setIsAdmin(session.isAdmin === true);
      setEnrollmentSetup(null);
      setEnrollmentCode('');
      const nextState = stateForSession(session);
      setSessionState(nextState);
      if (nextState === 'ready') {
        await loadSpaces();
      }
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onInitialize(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError('');
    setSetupNotice('');
    try {
      await initialize(setupForm);
      setSetupNotice(text.setupComplete);
      setSetupMode(false);
      setInitializationAvailable(false);
      setLoginForm({ login: setupForm.email, password: '', totpCode: '' });
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
    if (entry.previewKind !== 'markdown') {
      // 图片 / PDF / 音视频 / 纯文本均由浏览器原生渲染：新标签页打开 inline 链接
      window.open(previewURL(activeSpaceId, mountId, entry.relativePath), '_blank', 'noopener');
      return;
    }
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

  function openOperation(entry: MemberDirectoryEntry, operation: FileOperation) {
    setError('');
    setOperationTarget({ entry, operation });
    setDestinationSpaceId(activeSpaceId);
    setDestinationMountId(entry.mountId ?? activeMountId);
    setDestinationPath(relativePath);
  }

  async function onDeleteConfirmed() {
    if (!deleteTarget) return;
    const mountId = deleteTarget.mountId ?? activeMountId;
    const mount = mounts.find((item) => item.id === mountId) ?? activeMount;
    const deletePolicy = mountDeletePolicy(mount);
    if (deletePolicy === 'unavailable') {
      setError(text.deleteUnavailableDetail);
      return;
    }
    setDeleteBusy(true);
    setError('');
    try {
      await deleteObject(activeSpaceId, mountId, deleteTarget.relativePath, {
        permanent: deletePolicy === 'permanent',
      });
      setDeleteTarget(null);
      await refreshDirectory(activeSpaceId, activeMountId, relativePath);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setDeleteBusy(false);
    }
  }

  async function onFileOperation(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!operationTarget || !destinationSpaceId || !destinationMountId) return;
    const sourceMountId = operationTarget.entry.mountId ?? activeMountId;
    const toDir = normalizeParentPath(destinationPath);
    setOperationBusy(true);
    setError('');
    try {
      if (operationTarget.operation === 'copy') {
        await crossMountCopy(activeSpaceId, sourceMountId, {
          from: operationTarget.entry.relativePath,
          toSpaceId: destinationSpaceId,
          toMountId: destinationMountId,
          toDir,
        });
      } else if (destinationSpaceId === activeSpaceId && destinationMountId === sourceMountId) {
        await moveObject(activeSpaceId, sourceMountId, { from: operationTarget.entry.relativePath, toDir });
      } else {
        await crossMountMove(activeSpaceId, sourceMountId, {
          from: operationTarget.entry.relativePath,
          toSpaceId: destinationSpaceId,
          toMountId: destinationMountId,
          toDir,
        });
      }
      setOperationTarget(null);
      await refreshDirectory(activeSpaceId, activeMountId, relativePath);
      if (activeTab === 'trash') await refreshTrash();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setOperationBusy(false);
    }
  }

  async function onRestoreTrash(item: TrashItemPayload) {
    if (!activeSpaceId || !activeMountId) return;
    setLoading(true);
    setError('');
    try {
      await restoreTrashItem(activeSpaceId, activeMountId, item.id);
      await refreshTrash();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onPurgeTrash(item: TrashItemPayload) {
    if (!activeSpaceId || !activeMountId) return;
    setLoading(true);
    setError('');
    try {
      await purgeTrashItem(activeSpaceId, activeMountId, item.id);
      await refreshTrash();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onEmptyTrash() {
    if (!activeSpaceId || !activeMountId || !window.confirm(text.trashEmptyConfirm)) return;
    setLoading(true);
    setError('');
    try {
      await emptyTrash(activeSpaceId, activeMountId);
      await refreshTrash();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
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
    setResumeSelectMode(false);
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

  const activateAdminTab = useCallback((tab: AdminTab) => {
    setActiveTab(tab);
    setAdminGroupTabs((current) => ({ ...current, [adminGroupForTab(tab)]: tab }));
  }, []);

  const activateAdminGroup = useCallback((group: AdminNavGroup) => {
    const nextTab = adminGroupTabs[group] ?? defaultAdminGroupTabs[group];
    activateAdminTab(nextTab);
  }, [activateAdminTab, adminGroupTabs]);

  if (sessionState === 'checking') {
    return <main className="member-auth-state">{text.loading}</main>;
  }

  if (sessionState === 'signed-out') {
    return (
      <main className="member-auth-state">
        <section className="member-login-panel">
          <div className="member-brand"><span>O</span>Omnora</div>
          <h1>{setupMode ? text.setupTitle : text.signIn}</h1>
          {setupMode && <p>{text.setupDetail}</p>}
          {setupMode ? (
            <form onSubmit={onInitialize}>
              <label>{text.setupToken}<input value={setupForm.token} onChange={(event) => setSetupForm({ ...setupForm, token: event.target.value })} autoComplete="one-time-code" required /></label>
              <label>{text.email}<input type="email" value={setupForm.email} onChange={(event) => setSetupForm({ ...setupForm, email: event.target.value })} autoComplete="username" required /></label>
              <label>{text.setupDisplayName}<input value={setupForm.displayName} onChange={(event) => setSetupForm({ ...setupForm, displayName: event.target.value })} required /></label>
              <label>{text.password}<input type="password" value={setupForm.password} onChange={(event) => setSetupForm({ ...setupForm, password: event.target.value })} autoComplete="new-password" required /></label>
              {error && <p className="member-error">{text.error}: {error}</p>}
              <button className="member-primary" type="submit" disabled={loading}>{text.setupSubmit}</button>
              <button className="member-secondary-action" type="button" onClick={() => { setSetupMode(false); setError(''); }}>{text.backToSignIn}</button>
            </form>
          ) : (
            <form onSubmit={onLogin}>
              <label>{text.email}<input value={loginForm.login} onChange={(event) => setLoginForm({ ...loginForm, login: event.target.value })} autoComplete="username" required /></label>
              <label>{text.password}<input type="password" value={loginForm.password} onChange={(event) => setLoginForm({ ...loginForm, password: event.target.value })} autoComplete="current-password" required /></label>
              {setupNotice && <p className="member-readonly">{setupNotice}</p>}
              {error && <p className="member-error">{text.error}: {error}</p>}
              <button className="member-primary" type="submit" disabled={loading}>{text.signInAction}</button>
              {initializationAvailable && <button className="member-secondary-action" type="button" onClick={() => { setSetupMode(true); setError(''); }}>{text.firstSetup}</button>}
            </form>
          )}
          <div className="member-language-auth"><button type="button" onClick={() => setLocale('zh-CN')} aria-pressed={locale === 'zh-CN'}>中文</button><button type="button" onClick={() => setLocale('en-US')} aria-pressed={locale === 'en-US'}>EN</button></div>
        </section>
      </main>
    );
  }

  if (sessionState === 'enrollment') {
    return (
      <main className="member-auth-state">
        <section className="member-login-panel">
          <div className="member-brand"><span>O</span>Omnora</div>
          <h1>{text.accountTotpSection}</h1>
          <p>{text.accountTotpSetupHint}</p>
          {!enrollmentSetup ? (
            <button className="member-primary" type="button" onClick={() => void onStartEnrollment()} disabled={loading}>{text.accountTotpSetup}</button>
          ) : (
            <form onSubmit={onConfirmEnrollment}>
              <label>{text.accountTotpSecret}<input readOnly value={enrollmentSetup.secret} /></label>
              <label>{text.accountTotpCode}<input value={enrollmentCode} onChange={(event) => setEnrollmentCode(event.target.value)} inputMode="numeric" autoComplete="one-time-code" required /></label>
              {error && <p className="member-error">{text.error}: {error}</p>}
              <button className="member-primary" type="submit" disabled={loading}>{text.accountTotpConfirm}</button>
            </form>
          )}
          <button className="member-secondary-action" type="button" onClick={() => void onLogout()}>{text.signOut}</button>
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
    mountName: readableLabel(mounts.find((mount) => mount.id === item.mountId)?.name),
  }));
  const crumbItems = breadcrumbs(relativePath);
  const previewSrc = preview ? previewURL(activeSpaceId, preview.mountId, preview.relativePath) : '';
  const previewDownload = preview ? downloadURL(activeSpaceId, preview.mountId, preview.relativePath) : '';
  const canManageShares = entry === 'member' && activeSpace?.role === 'manager';
  const canEditFiles = entry === 'member' && (activeSpace?.role === 'editor' || activeSpace?.role === 'manager');
  const deleteTargetMount = deleteTarget ? mounts.find((mount) => mount.id === (deleteTarget.mountId ?? activeMountId)) ?? activeMount : null;
  const deleteTargetPolicy = mountDeletePolicy(deleteTargetMount);
  const adminNavigation: AdminNavGroupItem[] = [
    { id: 'overview', label: text.adminOverview, tabs: [{ id: 'overview', label: text.adminOverview }] },
    { id: 'identity-space', label: text.adminIdentitySpace, tabs: [{ id: 'users', label: text.adminUsers }] },
    { id: 'storage-search', label: text.adminStorageSearch, tabs: [{ id: 'mounts', label: text.adminMounts }, { id: 'index-jobs', label: text.adminIndexJobs }] },
    { id: 'access-security', label: text.adminAccessSecurity, tabs: [{ id: 'route-groups', label: text.adminRouteGroups }, { id: 'share-governance', label: text.adminShareGovernance }, { id: 'token-governance', label: text.adminTokenGovernance }, { id: 'audit', label: text.adminAudit }] },
    { id: 'backups', label: text.adminBackups, tabs: [{ id: 'backups', label: text.adminBackups }] },
  ];
  const activeAdminTab = entry === 'admin' ? activeTab as AdminTab : null;
  const activeAdminGroup = activeAdminTab ? adminNavigation.find((group) => group.id === adminGroupForTab(activeAdminTab)) ?? adminNavigation[0] : null;

  return (
    <RecentReauthProvider locale={locale}>
    <main className="member-app">
      <header className="member-topbar">
        <div className="member-brand"><span>O</span>Omnora</div>
        {showingSpaceFiles && <form className="member-search" onSubmit={onSearch}><input value={searchQuery} onChange={(event) => setSearchQuery(event.target.value)} placeholder={text.searchPlaceholder} /><button type="submit">{text.search}</button></form>}
        <div className="member-top-actions"><div className="member-language" aria-label={text.language}><button type="button" onClick={() => setLocale('zh-CN')} aria-pressed={locale === 'zh-CN'}>中文</button><button type="button" onClick={() => setLocale('en-US')} aria-pressed={locale === 'en-US'}>EN</button></div><button className="member-account" type="button" onClick={onLogout}>{text.signOut}</button></div>
      </header>

      <div className="member-layout">
        <aside className="member-sidebar">
          {entry === 'admin' && <nav aria-label="Administrator workspace">
            {adminNavigation.map((group) => (
              <button className={`member-nav ${activeAdminGroup?.id === group.id ? 'active' : ''}`} type="button" onClick={() => activateAdminGroup(group.id)} key={group.id}>{group.label}</button>
            ))}
          </nav>}
          {entry === 'member' && <nav aria-label="Member workspace">
            <MemberContentNavigation locale={locale} active={activeTab === 'files' || activeTab === 'collaborations' ? activeTab : null} onSelect={setActiveTab} />
            {activeMountSupportsTrash && <button className={`member-nav ${activeTab === 'trash' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('trash')}>{text.recycleBin}</button>}
            <button className={`member-nav ${activeTab === 'shares' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('shares')}>{text.navShares}</button>
            <button className={`member-nav ${activeTab === 'tokens' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('tokens')}>{text.navTokens}</button>
            <button className={`member-nav ${activeTab === 'docs' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('docs')}>{text.navDocs}</button>
            <button className={`member-nav ${activeTab === 'account' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('account')}>{text.account}</button>
          </nav>}
          {entry === 'member' && activeSpace && (activeSpaceTab !== null || activeTab === 'trash') && <div className="member-sidebar-section"><p>{text.mounts}</p>{mounts.map((mount) => <button className={`member-mount ${mount.id === activeMountId ? 'selected' : ''}`} key={mount.id} type="button" onClick={() => selectMount(mount)}><span>{mount.name}</span><small>{mount.health === 'unavailable' ? text.statusUnavailable : mount.mode === 'read-only' ? text.readOnly : text.readWrite}</small></button>)}</div>}
        </aside>

        <section className="member-content">
          {entry === 'admin' && activeAdminGroup && activeAdminGroup.tabs.length > 1 && (
            <div className="member-admin-subnav" role="tablist" aria-label={activeAdminGroup.label}>
              {activeAdminGroup.tabs.map((item) => (
                <button
                  type="button"
                  role="tab"
                  aria-selected={activeAdminTab === item.id}
                  aria-pressed={activeAdminTab === item.id}
                  onClick={() => activateAdminTab(item.id)}
                  key={item.id}
                >
                  {item.label}
                </button>
              ))}
            </div>
          )}
          {activeTab === 'files' ? <MemberStorageWorkspace locale={locale} view="files" />
          : activeTab === 'collaborations' ? <MemberStorageWorkspace locale={locale} view="collaborations" />
          : showingSpaceDirectory && activeCategory ? <MemberSpaceDirectory category={activeCategory} locale={locale} spaces={spaces} error={error} onOpen={selectSpace} />
          : showingSpaceFiles && activeSpace && activeCategory ? <div className="member-page-flow">
          <div className="member-crumbs"><button type="button" onClick={() => openSpaceCategory(activeCategory)}>{activeCategory === 'personal' ? text.personalSpaces : text.teamSpaces}</button><span><b>/</b><button type="button" onClick={() => openDirectory('.')}>{activeSpace.name}</button></span>{crumbItems.map((part, index) => <span key={`${part}-${index}`}><b>/</b><button type="button" onClick={() => openDirectory(crumbItems.slice(0, index + 1).join('/'))}>{part}</button></span>)}</div>
          <div className="member-heading"><div><h1>{searchResults === null ? activeSpace.name : `${text.search}: ${searchQuery}`}</h1><p>{activeMount ? `${activeMount.name} · ${mountUnavailable ? text.statusUnavailable : readOnly ? text.readOnly : text.readWrite}` : text.noMount}</p></div><div className="member-view-toggle"><button type="button" aria-pressed={viewMode === 'list'} onClick={() => setViewMode('list')}>{text.list}</button><button type="button" aria-pressed={viewMode === 'grid'} onClick={() => setViewMode('grid')}>{text.grid}</button></div></div>
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
                  <td><div className="member-file-name"><FileTypeIcon kind={entry.kind} name={entry.name} className={`member-file-icon ${entry.kind}`} />{entry.kind === 'dir' ? <button type="button" onClick={() => openDirectory(entry.relativePath, entry.mountId)}>{entry.name}</button> : canPreview(entry.previewKind) ? <button type="button" className="member-file-preview" onClick={() => openPreview(entry)}>{entry.name}</button> : <span>{entry.name}</span>}{searchResults !== null && entry.mountName && <small>{entry.mountName}</small>}</div></td>
                  <td>{entry.kind === 'dir' ? '--' : formatBytes(entry.size, locale)}</td>
                  <td>{formatDate(entry.modifiedAt, locale)}</td>
                  <td><div className="member-file-actions">{entry.kind === 'dir' ? <button type="button" onClick={() => openDirectory(entry.relativePath, entry.mountId)}>{text.open}</button> : <>{canPreview(entry.previewKind) && <button type="button" onClick={() => openPreview(entry)}>{text.preview}</button>}<a href={downloadURL(activeSpaceId, entry.mountId ?? activeMountId, entry.relativePath)}>{text.download}</a></>}{canManageShares && searchResults === null && <button type="button" onClick={() => openShareForEntry(entry)}>{text.shareAction}</button>}{canEditFiles && !writeBlocked && !entry.readOnly && searchResults === null && <button type="button" onClick={() => openOperation(entry, 'move')}>{text.move}</button>}{canEditFiles && !writeBlocked && !entry.readOnly && searchResults === null && <button type="button" onClick={() => openOperation(entry, 'copy')}>{text.copyObject}</button>}{canEditFiles && !writeBlocked && !entry.readOnly && searchResults === null && <button type="button" onClick={() => openRename(entry)}>{text.rename}</button>}{canEditFiles && !writeBlocked && !entry.readOnly && searchResults === null && <button type="button" onClick={() => openDelete(entry)}>{text.deleteFile}</button>}</div></td>
                </tr>
              ))}</tbody>
            </table>
          ) : (
            <div className="member-file-grid">{visibleEntries.map((entry) => (
              <article key={`${entry.kind}-${entry.mountId ?? activeMountId}-${entry.relativePath}`}>
                <FileTypeIcon kind={entry.kind} name={entry.name} className={`member-file-icon ${entry.kind}`} />
                {entry.kind === 'dir' || canPreview(entry.previewKind) ? <button type="button" className={entry.kind === 'file' ? 'member-file-preview' : undefined} onClick={() => entry.kind === 'dir' ? openDirectory(entry.relativePath, entry.mountId) : openPreview(entry)}><strong>{entry.name}</strong></button> : <strong>{entry.name}</strong>}
                <small>{entry.kind === 'dir' ? '--' : formatBytes(entry.size, locale)}</small>
                {searchResults !== null && entry.mountName && <small>{entry.mountName}</small>}
                <div className="member-file-actions">{entry.kind === 'dir' ? <button type="button" onClick={() => openDirectory(entry.relativePath, entry.mountId)}>{text.open}</button> : <>{canPreview(entry.previewKind) && <button type="button" onClick={() => openPreview(entry)}>{text.preview}</button>}<a className="member-grid-download" href={downloadURL(activeSpaceId, entry.mountId ?? activeMountId, entry.relativePath)}>{text.download}</a></>}{canManageShares && searchResults === null && <button type="button" onClick={() => openShareForEntry(entry)}>{text.shareAction}</button>}{canEditFiles && !writeBlocked && !entry.readOnly && searchResults === null && <button type="button" onClick={() => openOperation(entry, 'move')}>{text.move}</button>}{canEditFiles && !writeBlocked && !entry.readOnly && searchResults === null && <button type="button" onClick={() => openOperation(entry, 'copy')}>{text.copyObject}</button>}{canEditFiles && !writeBlocked && !entry.readOnly && searchResults === null && <button type="button" onClick={() => openRename(entry)}>{text.rename}</button>}{canEditFiles && !writeBlocked && !entry.readOnly && searchResults === null && <button type="button" onClick={() => openDelete(entry)}>{text.deleteFile}</button>}</div>
              </article>
            ))}</div>
          )}
          {searchResults !== null && searchNextCursor && <button className="member-load-more" type="button" onClick={() => void loadMoreSearchResults()} disabled={loading}>{text.loadMore}</button>}
          </div> : activeTab === 'trash' ? <div className="member-page-flow">
            <div className="member-heading"><div><h1>{text.trashTitle}</h1><p>{activeMount ? `${activeMount.name} · ${text.trashDetail}` : text.noMount}</p></div><button className="member-secondary-action" type="button" onClick={() => void refreshTrash()} disabled={loading || !activeMountId}>{text.refresh}</button></div>
            <div className="member-toolbar"><button type="button" className="member-modal-danger" disabled={loading || trashItems.length === 0 || readOnly || mountUnavailable} onClick={() => void onEmptyTrash()}>{text.trashEmptyAction}</button></div>
            {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
            {loading ? <div className="member-loading">{text.loading}</div> : trashItems.length === 0 ? <div className="member-empty">{text.trashEmpty}</div> : (
              <table className="member-file-table">
                <thead><tr><th>{text.name}</th><th>{text.trashOriginalPath}</th><th>{text.size}</th><th>{text.trashDeletedAt}</th><th>{text.actions}</th></tr></thead>
                <tbody>{trashItems.map((item) => (
                  <tr key={item.id}>
                    <td><div className="member-file-name"><FileTypeIcon kind={item.kind === 'dir' ? 'dir' : 'file'} name={item.name} className={`member-file-icon ${item.kind === 'dir' ? 'dir' : 'file'}`} /><span>{item.name}</span></div></td>
                    <td>{item.originalPath}</td>
                    <td>{item.kind === 'dir' ? '--' : formatBytes(item.size, locale)}</td>
                    <td>{formatDate(item.deletedAt, locale)}</td>
                    <td><div className="member-file-actions"><button type="button" onClick={() => void onRestoreTrash(item)} disabled={loading || readOnly || mountUnavailable}>{text.trashRestore}</button><button type="button" onClick={() => void onPurgeTrash(item)} disabled={loading || readOnly || mountUnavailable}>{text.trashPurge}</button></div></td>
                  </tr>
                ))}</tbody>
              </table>
            )}
          </div> : activeTab === 'shares' ? <MemberSharesPanel locale={locale} />
            : activeTab === 'tokens' ? <MemberTokensPanel locale={locale} />
            : activeTab === 'docs' ? <MemberDocsPanel locale={locale} />
            : activeTab === 'account' ? <MemberAccountPanel locale={locale} />
            : <AdminWorkspace tab={activeTab as AdminTab} locale={locale} />}
        </section>
      </div>

      {transfers.length > 0 && <aside className="member-transfers"><div><strong>{text.activeTransfers}</strong><button type="button" onClick={() => setTransfers((items) => items.filter((item) => item.state === 'uploading' || item.state === 'queued'))}>{text.clearCompleted}</button></div>{transfers.map((transfer) => <div className="member-transfer" key={transfer.id}><span>{transfer.name}</span><progress value={transfer.progress} max="100" /><small>{transfer.state === 'failed' ? `${text.uploadFailed}: ${transfer.detail ?? ''}` : transfer.state === 'completed' ? text.uploadComplete : `${text.uploadProgress} ${transfer.progress}%`}</small>{(transfer.state === 'failed' || transfer.state === 'cancelled') && <small>{text.resumeUploadHint}</small>}{(transfer.state === 'failed' || transfer.state === 'cancelled') && <button type="button" onClick={() => { setResumeSelectMode(true); fileInputRef.current?.click(); }}>{resumeSelectMode ? text.selectFile : text.resumeUpload}</button>}{(transfer.state === 'uploading' || transfer.state === 'queued') && <button type="button" onClick={() => void cancelTransfer(transfer)}>{text.cancel}</button>}</div>)}</aside>}

      {newFolderOpen && <div className="member-modal-backdrop"><form className="member-modal" onSubmit={onCreateFolder}><h2>{text.newFolder}</h2><label>{text.folderName}<input autoFocus value={folderName} onChange={(event) => setFolderName(event.target.value)} required /></label><div className="member-modal-actions"><button type="button" onClick={() => setNewFolderOpen(false)}>{text.cancel}</button><button className="member-primary" type="submit" disabled={loading}>{text.create}</button></div></form></div>}

      {preview && <div className="member-preview-backdrop" onClick={() => setPreview(null)} role="presentation"><div className="member-preview-dialog" role="dialog" aria-modal="true" aria-label={preview.name} onClick={(event) => event.stopPropagation()}><div className="member-preview-toolbar"><strong>{preview.name}</strong><div className="member-preview-actions"><a className="member-preview-download" href={previewDownload}>{text.download}</a><button type="button" onClick={() => setPreview(null)}>{text.closePreview}</button></div></div><div className="member-preview-stage">{previewFailed ? <p className="member-preview-error">{text.previewFailed}</p> : preview.previewKind === 'image' ? <img src={previewSrc} alt={preview.name} onError={() => setPreviewFailed(true)} /> : preview.previewKind === 'media' ? (isAudioName(preview.name) ? <audio src={previewSrc} controls onError={() => setPreviewFailed(true)} /> : <video src={previewSrc} controls onError={() => setPreviewFailed(true)} />) : preview.previewKind === 'text' || preview.previewKind === 'markdown' ? (previewText === null ? <p className="member-preview-loading">{text.loading}</p> : preview.previewKind === 'markdown' ? <MarkdownPreview text={previewText} resolveAsset={(src) => markdownAssetURL(activeSpaceId, preview.mountId, preview.relativePath, src)} /> : <pre className="member-preview-text">{previewText}</pre>) : previewBlobUrl ? <iframe title={preview.name} src={previewBlobUrl} /> : <p className="member-preview-loading">{text.loading}</p>}</div></div></div>}

      {renameTarget && (
        <div className="member-modal-backdrop">
          <form className="member-modal" onSubmit={onRename}>
            <h2>{text.renameTitle}</h2>
            <label>{text.renameNewName}<input autoFocus value={renameValue} onChange={(event) => setRenameValue(event.target.value)} required /></label>
            {error && <p className="member-error">{text.error}: {error}</p>}
            <div className="member-modal-actions"><button type="button" onClick={() => setRenameTarget(null)}>{text.cancel}</button><button className="member-primary" type="submit" disabled={renameBusy || !renameValue.trim()}>{text.create}</button></div>
          </form>
        </div>
      )}

      {deleteTarget && (
        <div className="member-modal-backdrop">
          <div className="member-modal">
            <h2>{text.deleteConfirmTitle}</h2>
            <p className="member-modal-hint">{deleteTargetPolicy === 'permanent' ? text.deletePermanentConfirmDetail : deleteTargetPolicy === 'unavailable' ? text.deleteUnavailableDetail : text.deleteConfirmDetail}</p>
            {error && <p className="member-error">{text.error}: {error}</p>}
            <p className="member-modal-hint"><strong>{deleteTarget.name}</strong></p>
            <div className="member-modal-actions"><button type="button" onClick={() => setDeleteTarget(null)}>{text.cancel}</button><button className="member-modal-danger" type="button" onClick={() => void onDeleteConfirmed()} disabled={deleteBusy || deleteTargetPolicy === 'unavailable'}>{text.deleteFile}</button></div>
          </div>
        </div>
      )}

      {operationTarget && (
        <div className="member-modal-backdrop">
          <form className="member-modal" onSubmit={onFileOperation}>
            <h2>{operationTarget.operation === 'copy' ? text.moveCopyTitle : text.moveTitle}</h2>
            <p className="member-modal-hint"><strong>{operationTarget.entry.name}</strong></p>
            <label>{text.moveTargetSpace}
              <select value={destinationSpaceId} onChange={(event) => setDestinationSpaceId(event.target.value)} required>
                {spaces.map((space) => <option key={space.id} value={space.id}>{space.name}</option>)}
              </select>
            </label>
            <label>{text.moveTargetMount}
              <select value={destinationMountId} onChange={(event) => setDestinationMountId(event.target.value)} required>
                {destinationMounts.map((mount) => <option key={mount.id} value={mount.id}>{mount.name}</option>)}
              </select>
            </label>
            <label>{text.moveTargetPath}<input value={destinationPath} onChange={(event) => setDestinationPath(event.target.value)} placeholder="." /><small className="member-path-hint">{text.moveTargetPathHint}</small></label>
            {error && <p className="member-error">{text.error}: {error}</p>}
            <div className="member-modal-actions">
              <button type="button" onClick={() => setOperationTarget(null)}>{text.cancel}</button>
              <button className="member-primary" type="submit" disabled={operationBusy || !destinationMountId}>{operationTarget.operation === 'copy' ? text.copySubmit : text.moveSubmit}</button>
            </div>
          </form>
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
    </RecentReauthProvider>
  );
}
