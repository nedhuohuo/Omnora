import { useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent, type FormEvent } from 'react';
import {
  ApiError,
  completeUpload,
  copyMemberObject,
  createMemberDirectory,
  createUpload,
  deleteMemberObject,
  emptyMemberTrash,
  listMemberCollaborations,
  listMemberContentSources,
  listMemberDirectoryChildren,
  listMemberTrash,
  memberDownloadURL,
  moveMemberObject,
  purgeMemberTrash,
  renameMemberObject,
  restoreMemberTrash,
  searchMemberFiles,
  uploadPart,
  type MemberCollaboration,
  type MemberContentLocator,
  type MemberContentSourcesPayload,
} from '../api';
import FileTypeIcon from './FileTypeIcon';
import MemberCollaborationsDirectory from './MemberCollaborationsDirectory';
import MemberContentSourceDirectory from './MemberContentSourceDirectory';
import { localeMessages, type MemberLocale } from './i18n';
import { formatDirectoryChildren, type MemberDirectoryEntry } from './types';

 type Props = {
  locale: MemberLocale;
  view: 'personal' | 'team-folders' | 'collaborations';
};

type ActiveSource = {
  locator: MemberContentLocator;
  label: string;
  readOnly: boolean;
};

type TrashItem = {
  id: string;
  originalPath: string;
  name: string;
  kind: string;
  size: number;
  deletedAt: string;
};

export function MemberToolbarSearch({ label, value, loading, onChange, onSearch }: { label: string; value: string; loading: boolean; onChange: (value: string) => void; onSearch: () => void }) {
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!loading && value.trim()) onSearch();
  }

  return <form className="member-toolbar-search" role="search" onSubmit={submit}>
    <input aria-label={label} value={value} onChange={(event) => onChange(event.target.value)} placeholder={label} />
    <button type="submit" disabled={loading || !value.trim()}>{label}</button>
  </form>;
}

function describeError(error: unknown) {
  if (error instanceof ApiError) {
    const body = error.body as { error?: { message?: string } } | undefined;
    return body?.error?.message || error.message;
  }
  return error instanceof Error ? error.message : String(error);
}

function withPath(locator: MemberContentLocator, path: string): MemberContentLocator {
  return { ...locator, path };
}

function parentPath(path: string) {
  const parts = path.split('/').filter((part) => part && part !== '.');
  parts.pop();
  return parts.join('/') || '.';
}

function joinPath(directory: string, name: string) {
  return directory === '.' ? name : `${directory.replace(/\/+$/, '')}/${name}`;
}

function sourceUploadFields(locator: MemberContentLocator) {
  if (locator.source === 'personal') return { source: locator.source as 'personal' };
  if (locator.source === 'common_mount') return { source: locator.source as 'common_mount', mountId: locator.mountId };
  return { source: locator.source as 'collaboration', collaborationId: locator.collaborationId };
}

export default function MemberStorageWorkspace({ locale, view }: Props) {
  const text = localeMessages[locale];
  const [sources, setSources] = useState<MemberContentSourcesPayload | null>(null);
  const [incoming, setIncoming] = useState<MemberCollaboration[]>([]);
  const [outgoing, setOutgoing] = useState<MemberCollaboration[]>([]);
  const [activeSource, setActiveSource] = useState<ActiveSource | null>(null);
  const [entries, setEntries] = useState<MemberDirectoryEntry[]>([]);
  const [searchResults, setSearchResults] = useState<MemberDirectoryEntry[] | null>(null);
  const [searchQuery, setSearchQuery] = useState('');
  const [path, setPath] = useState('.');
  const [trashItems, setTrashItems] = useState<TrashItem[]>([]);
  const [showTrash, setShowTrash] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const fileInputRef = useRef<HTMLInputElement>(null);

  const loadDirectory = useCallback(async (source: ActiveSource, nextPath: string) => {
    setLoading(true);
    setError('');
    setShowTrash(false);
    setSearchResults(null);
    try {
      const listing = formatDirectoryChildren(await listMemberDirectoryChildren(withPath(source.locator, nextPath)));
      setPath(listing.relativePath);
      setEntries(listing.entries);
      setActiveSource({ ...source, locator: withPath(source.locator, listing.relativePath), readOnly: source.readOnly || listing.readOnly });
    } catch (caught) {
      setEntries([]);
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  const loadTrash = useCallback(async (source: ActiveSource) => {
    setLoading(true);
    setError('');
    try {
      const result = await listMemberTrash(withPath(source.locator, '.'));
      setTrashItems(result.items ?? []);
      setShowTrash(true);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    setActiveSource(null);
    setEntries([]);
    setSearchResults(null);
    setTrashItems([]);
    setPath('.');
    setError('');
    setLoading(true);
    if (view === 'personal' || view === 'team-folders') {
      void listMemberContentSources()
        .then((loaded) => {
          setSources(loaded);
          if (view === 'personal') openSource({ source: 'personal', path: '.' }, loaded.personal.label || text.personalSpace, false);
        })
        .catch((caught) => setError(describeError(caught)))
        .finally(() => setLoading(false));
      return;
    }
    void Promise.all([listMemberCollaborations('incoming'), listMemberCollaborations('outgoing')])
      .then(([received, sent]) => {
        setIncoming(received.items ?? []);
        setOutgoing(sent.items ?? []);
      })
      .catch((caught) => setError(describeError(caught)))
      .finally(() => setLoading(false));
  }, [view]);

  const crumbs = useMemo(() => path.split('/').filter((part) => part && part !== '.'), [path]);

  function openSource(locator: MemberContentLocator, label: string, readOnly: boolean) {
    const source = { locator, label, readOnly: readOnly || locator.source === 'collaboration' };
    setActiveSource(source);
    void loadDirectory(source, '.');
  }

  async function createFolder() {
    if (!activeSource || activeSource.readOnly) return;
    const name = window.prompt(text.folderName);
    if (!name?.trim()) return;
    setLoading(true);
    setError('');
    try {
      await createMemberDirectory(withPath(activeSource.locator, path), name.trim());
      await loadDirectory(activeSource, path);
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function uploadFile(file: File) {
    if (!activeSource || activeSource.readOnly) return;
    setLoading(true);
    setError('');
    try {
      const created = await createUpload({
        ...sourceUploadFields(activeSource.locator),
        parentPath: path,
        fileName: file.name,
        size: file.size,
      });
      const uploadID = created.id;
      const partSize = created.partSize ?? 5 * 1024 * 1024;
      if (!uploadID) throw new Error('upload session ID is missing');
      const totalParts = Math.max(1, Math.ceil(file.size / partSize));
      for (let part = 1; part <= totalParts; part += 1) {
        const start = (part - 1) * partSize;
        await uploadPart(uploadID, part, file.slice(start, Math.min(start + partSize, file.size)));
      }
      await completeUpload(uploadID);
      await loadDirectory(activeSource, path);
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function onFileSelected(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (file) await uploadFile(file);
  }

  async function renameEntry(entry: MemberDirectoryEntry) {
    if (!activeSource || activeSource.readOnly) return;
    const name = window.prompt(text.renameNewName, entry.name);
    if (!name?.trim()) return;
    setLoading(true);
    setError('');
    try {
      await renameMemberObject(withPath(activeSource.locator, entry.relativePath), joinPath(parentPath(entry.relativePath), name.trim()));
      await loadDirectory(activeSource, path);
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function deleteEntry(entry: MemberDirectoryEntry) {
    if (!activeSource || activeSource.readOnly || !window.confirm(`${text.deleteFile}: ${entry.name}`)) return;
    setLoading(true);
    setError('');
    try {
      await deleteMemberObject(withPath(activeSource.locator, entry.relativePath));
      await loadDirectory(activeSource, path);
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function copyOrMoveEntry(entry: MemberDirectoryEntry, move: boolean) {
    if (!activeSource || activeSource.readOnly) return;
    const target = window.prompt(text.moveTargetPath, joinPath(path, entry.name));
    if (!target?.trim()) return;
    setLoading(true);
    setError('');
    try {
      const source = withPath(activeSource.locator, entry.relativePath);
      const destination = withPath(activeSource.locator, target.trim());
      if (move) await moveMemberObject(source, destination);
      else await copyMemberObject(source, destination);
      await loadDirectory(activeSource, path);
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function search() {
    if (!activeSource || !searchQuery.trim()) return;
    setLoading(true);
    setError('');
    try {
      const locator = activeSource.locator.source === 'personal'
        ? { source: 'personal' as const }
        : activeSource.locator.source === 'common_mount'
          ? { source: 'common_mount' as const, mountId: activeSource.locator.mountId }
          : { source: 'collaboration' as const, collaborationId: activeSource.locator.collaborationId };
      const result = await searchMemberFiles(locator, searchQuery.trim());
      setSearchResults((result.items ?? []).map((item) => ({
        mountId: item.mountId,
        name: item.name,
        relativePath: item.relativePath,
        kind: item.kind,
        size: item.sizeBytes ?? 0,
        modifiedAt: item.modifiedAt ?? '',
        readOnly: activeSource.readOnly,
        previewKind: item.previewKind ?? '',
      })));
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function restore(item: TrashItem) {
    if (!activeSource || activeSource.readOnly) return;
    setLoading(true);
    try {
      await restoreMemberTrash(withPath(activeSource.locator, '.'), item.id);
      await loadTrash(activeSource);
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function purge(item: TrashItem) {
    if (!activeSource || activeSource.readOnly || !window.confirm(`${text.trashPurge}: ${item.name}`)) return;
    setLoading(true);
    try {
      await purgeMemberTrash(withPath(activeSource.locator, '.'), item.id);
      await loadTrash(activeSource);
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  if (activeSource) {
    const visibleEntries = searchResults ?? entries;
    const isCollaboration = activeSource.locator.source === 'collaboration';
    return (
      <div className="member-page-flow">
        <div className="member-crumbs">
          <button type="button" onClick={() => {
            setShowTrash(false);
            if (view === 'personal') void loadDirectory(activeSource, '.');
            else { setActiveSource(null); setEntries([]); setPath('.'); }
          }}>{view === 'personal' ? text.personalSpace : view === 'team-folders' ? text.teamFolders : text.collaboration}</button>
          <span><b>/</b><button type="button" onClick={() => void loadDirectory(activeSource, '.')}>{activeSource.label}</button></span>
          {crumbs.map((part, index) => <span key={`${part}-${index}`}><b>/</b><button type="button" onClick={() => void loadDirectory(activeSource, crumbs.slice(0, index + 1).join('/'))}>{part}</button></span>)}
        </div>
        <div className="member-heading"><div><h1>{activeSource.label}</h1>{activeSource.readOnly && <p>{text.readOnly}</p>}</div></div>
        <div className="member-toolbar">
          {path !== '.' && <button type="button" onClick={() => void loadDirectory(activeSource, parentPath(path))}>{text.back}</button>}
          {!activeSource.readOnly && <><button className="member-primary" type="button" onClick={() => fileInputRef.current?.click()}>{text.upload}</button><button type="button" onClick={() => void createFolder()}>{text.newFolder}</button></>}
          {!isCollaboration && <button type="button" onClick={() => void loadTrash(activeSource)}>{text.trashTitle}</button>}
          <span className="member-toolbar-spacer" />
          {!isCollaboration && <MemberToolbarSearch label={text.search} value={searchQuery} loading={loading} onChange={setSearchQuery} onSearch={() => void search()} />}
          <button type="button" onClick={() => void loadDirectory(activeSource, path)} disabled={loading}>{text.refresh}</button>
          <input ref={fileInputRef} type="file" hidden onChange={(event) => void onFileSelected(event)} />
        </div>
        {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
        {showTrash ? <>
          <div className="member-heading"><div><h2>{text.trashTitle}</h2><p>{text.trashDetail}</p></div><button type="button" onClick={() => void emptyMemberTrash(withPath(activeSource.locator, '.')).then(() => void loadTrash(activeSource))} disabled={loading || activeSource.readOnly}>{text.trashEmptyAction}</button></div>
          {loading ? <div className="member-loading">{text.loading}</div> : trashItems.length === 0 ? <div className="member-empty">{text.trashEmpty}</div> : <table className="member-file-table"><tbody>{trashItems.map((item) => <tr key={item.id}><td>{item.name}</td><td>{item.originalPath}</td><td><button type="button" onClick={() => void restore(item)} disabled={activeSource.readOnly}>{text.trashRestore}</button><button type="button" onClick={() => void purge(item)} disabled={activeSource.readOnly}>{text.trashPurge}</button></td></tr>)}</tbody></table>}
        </> : loading ? <div className="member-loading">{text.loading}</div> : visibleEntries.length === 0 ? <div className="member-empty">{text.emptyFolder}</div> : <table className="member-file-table"><thead><tr><th>{text.name}</th><th>{text.size}</th><th>{text.modified}</th><th>{text.actions}</th></tr></thead><tbody>{visibleEntries.map((entry) => <tr key={`${entry.kind}-${entry.relativePath}`}><td><div className="member-file-name"><FileTypeIcon kind={entry.kind} name={entry.name} className={`member-file-icon ${entry.kind}`} />{entry.kind === 'dir' ? <button type="button" onClick={() => void loadDirectory(activeSource, entry.relativePath)}>{entry.name}</button> : <a href={memberDownloadURL(withPath(activeSource.locator, entry.relativePath))}>{entry.name}</a>}</div></td><td>{entry.kind === 'dir' ? '—' : new Intl.NumberFormat(locale).format(entry.size)}</td><td>{entry.modifiedAt ? new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(entry.modifiedAt)) : '—'}</td><td><div className="member-file-actions">{entry.kind === 'dir' ? <button type="button" onClick={() => void loadDirectory(activeSource, entry.relativePath)}>{text.open}</button> : <a href={memberDownloadURL(withPath(activeSource.locator, entry.relativePath), { inline: true })}>{text.preview}</a>}{!activeSource.readOnly && <><button type="button" onClick={() => void copyOrMoveEntry(entry, true)}>{text.move}</button><button type="button" onClick={() => void copyOrMoveEntry(entry, false)}>{text.copyObject}</button><button type="button" onClick={() => void renameEntry(entry)}>{text.rename}</button><button type="button" onClick={() => void deleteEntry(entry)}>{text.deleteFile}</button></>}</div></td></tr>)}</tbody></table>}
      </div>
    );
  }

  if (loading) return <div className="member-loading">{text.loading}</div>;
  if (error) return <div className="member-error member-page-error">{text.error}: {error}</div>;
  if (view === 'collaborations') return <MemberCollaborationsDirectory locale={locale} incoming={incoming} outgoing={outgoing} onOpen={openSource} />;
  if (!sources) return <div className="member-empty">{text.noCommonStorage}</div>;
  return <MemberContentSourceDirectory locale={locale} sources={sources} onOpen={openSource} />;
}
