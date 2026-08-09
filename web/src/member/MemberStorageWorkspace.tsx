import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ApiError,
  listMemberCollaborations,
  listMemberContentSources,
  listMemberDirectoryChildren,
  createMemberDirectory,
  createMemberUpload,
  completeUpload,
  uploadPart,
  renameMemberObject,
  copyMemberObject,
  moveMemberObject,
  deleteMemberObject,
  memberDownloadURL,
  type MemberCollaboration,
  type MemberContentLocator,
  type MemberContentSourcesPayload,
  type MemberMutationLocator,
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

export default function MemberStorageWorkspace({ locale, view }: Props) {
  const text = localeMessages[locale];
  const [sources, setSources] = useState<MemberContentSourcesPayload | null>(null);
  const [incoming, setIncoming] = useState<MemberCollaboration[]>([]);
  const [outgoing, setOutgoing] = useState<MemberCollaboration[]>([]);
  const [activeSource, setActiveSource] = useState<ActiveSource | null>(null);
  const [entries, setEntries] = useState<MemberDirectoryEntry[]>([]);
  const [path, setPath] = useState('.');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const fileInputRef = useRef<HTMLInputElement>(null);

  const loadDirectory = useCallback(async (source: ActiveSource, nextPath: string) => {
    setLoading(true);
    setError('');
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

  useEffect(() => {
    setActiveSource(null);
    setEntries([]);
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
    const source = { locator, label, readOnly };
    setActiveSource(source);
    void loadDirectory(source, '.');
  }

  function mutableLocator(source: ActiveSource, nextPath = source.locator.path): MemberMutationLocator | null {
    if (source.readOnly || source.locator.source === 'collaboration') return null;
    return source.locator.source === 'common_mount'
      ? { source: 'common_mount', mountId: source.locator.mountId, path: nextPath }
      : { source: 'personal', path: nextPath };
  }

  async function mutate(operation: () => Promise<unknown>) {
    try { await operation(); await loadDirectory(activeSource!, path); } catch (caught) { setError(describeError(caught)); }
  }

  async function uploadFiles(files: FileList | null) {
    const locator = activeSource && mutableLocator(activeSource, path);
    if (!locator || !files) return;
    for (const file of Array.from(files)) {
      const upload = await createMemberUpload(locator, file.name, file.size);
      if (!upload.id) throw new Error('upload session is missing an ID');
      await uploadPart(upload.id, 1, file);
      await completeUpload(upload.id);
    }
    await loadDirectory(activeSource!, path);
  }

  if (activeSource) {
    return (
      <div className="member-page-flow">
        <div className="member-crumbs">
          <button type="button" onClick={() => { setActiveSource(null); setEntries([]); setPath('.'); }}>{view === 'personal' ? text.personalSpace : view === 'team-folders' ? text.teamFolders : text.collaboration}</button>
          <span><b>/</b><button type="button" onClick={() => void loadDirectory(activeSource, '.')}>{activeSource.label}</button></span>
          {crumbs.map((part, index) => <span key={`${part}-${index}`}><b>/</b><button type="button" onClick={() => void loadDirectory(activeSource, crumbs.slice(0, index + 1).join('/'))}>{part}</button></span>)}
        </div>
        <div className="member-heading"><div><h1>{activeSource.label}</h1>{activeSource.readOnly && <p>{text.readOnly}</p>}</div></div>
        <div className="member-toolbar">
          {path !== '.' && <button type="button" onClick={() => void loadDirectory(activeSource, parentPath(path))}>{text.back}</button>}
          {!activeSource.readOnly && activeSource.locator.source !== 'collaboration' && <><button className="member-primary" type="button" onClick={() => fileInputRef.current?.click()}>{text.upload}</button><button type="button" onClick={() => { const name = window.prompt(text.folderName); const locator = mutableLocator(activeSource, path); if (name && locator) void mutate(() => createMemberDirectory(locator, name)); }}>{text.newFolder}</button><input ref={fileInputRef} type="file" multiple hidden onChange={(event) => void uploadFiles(event.target.files)} /></>}
          <span className="member-toolbar-spacer" />
          <button type="button" onClick={() => void loadDirectory(activeSource, path)} disabled={loading}>{text.refresh}</button>
        </div>
        {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
        {loading ? <div className="member-loading">{text.loading}</div> : entries.length === 0 ? <div className="member-empty">{activeSource.locator.source === 'personal' ? text.personalSpaceEmpty : text.emptyFolder}</div> : (
          <table className="member-file-table">
            <thead><tr><th>{text.name}</th><th>{text.size}</th><th>{text.modified}</th><th>{text.actions}</th></tr></thead>
            <tbody>{entries.map((entry) => <tr key={`${entry.kind}-${entry.relativePath}`}>
              <td><div className="member-file-name"><FileTypeIcon kind={entry.kind} name={entry.name} className={`member-file-icon ${entry.kind}`} />{entry.kind === 'dir' ? <button type="button" onClick={() => void loadDirectory(activeSource, entry.relativePath)}>{entry.name}</button> : <span>{entry.name}</span>}</div></td>
              <td>{entry.kind === 'dir' ? '—' : new Intl.NumberFormat(locale).format(entry.size)}</td>
              <td>{entry.modifiedAt ? new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(entry.modifiedAt)) : '—'}</td>
              <td>{entry.kind === 'dir' ? <button type="button" onClick={() => void loadDirectory(activeSource, entry.relativePath)}>{text.open}</button> : <a href={activeSource.locator.source === 'collaboration' ? '#' : memberDownloadURL(mutableLocator(activeSource, entry.relativePath)!) }>{text.download}</a>}{!activeSource.readOnly && activeSource.locator.source !== 'collaboration' && <span className="member-file-actions"><button type="button" onClick={() => { const name = window.prompt(text.rename, entry.name); const locator = mutableLocator(activeSource, entry.relativePath); if (name && locator) void mutate(() => renameMemberObject(locator, name)); }}>{text.rename}</button><button type="button" onClick={() => { const locator = mutableLocator(activeSource, entry.relativePath); const destination = mutableLocator(activeSource, path); if (locator && destination) void mutate(() => copyMemberObject(locator, destination)); }}>{text.copy}</button><button type="button" onClick={() => { const locator = mutableLocator(activeSource, entry.relativePath); const destination = mutableLocator(activeSource, path); if (locator && destination) void mutate(() => moveMemberObject(locator, destination)); }}>{text.move}</button><button type="button" onClick={() => { const locator = mutableLocator(activeSource, entry.relativePath); if (locator && window.confirm(text.deleteConfirm)) void mutate(() => deleteMemberObject(locator)); }}>{text.deleteObject}</button></span>}</td>
            </tr>)}</tbody>
          </table>
        )}
      </div>
    );
  }

  if (loading) return <div className="member-loading">{text.loading}</div>;
  if (error) return <div className="member-error member-page-error">{text.error}: {error}</div>;
  if (view === 'collaborations') return <MemberCollaborationsDirectory locale={locale} incoming={incoming} outgoing={outgoing} onOpen={openSource} />;
  if (!sources) return <div className="member-empty">{text.noCommonStorage}</div>;
  return <MemberContentSourceDirectory locale={locale} sources={sources} onOpen={openSource} />;
}
