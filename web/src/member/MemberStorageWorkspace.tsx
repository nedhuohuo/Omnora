import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  ApiError,
  listMemberCollaborations,
  listMemberContentSources,
  listMemberDirectoryChildren,
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
  view: 'files' | 'collaborations';
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
    if (view === 'files') {
      void listMemberContentSources()
        .then(setSources)
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

  if (activeSource) {
    return (
      <div className="member-page-flow">
        <div className="member-crumbs">
          <button type="button" onClick={() => { setActiveSource(null); setEntries([]); setPath('.'); }}>{view === 'files' ? text.myFiles : text.collaboration}</button>
          <span><b>/</b><button type="button" onClick={() => void loadDirectory(activeSource, '.')}>{activeSource.label}</button></span>
          {crumbs.map((part, index) => <span key={`${part}-${index}`}><b>/</b><button type="button" onClick={() => void loadDirectory(activeSource, crumbs.slice(0, index + 1).join('/'))}>{part}</button></span>)}
        </div>
        <div className="member-heading"><div><h1>{activeSource.label}</h1><p>{activeSource.readOnly ? text.readOnly : text.readWrite}</p></div></div>
        <div className="member-toolbar">
          {path !== '.' && <button type="button" onClick={() => void loadDirectory(activeSource, parentPath(path))}>{text.back}</button>}
          <span className="member-toolbar-spacer" />
          <button type="button" onClick={() => void loadDirectory(activeSource, path)} disabled={loading}>{text.refresh}</button>
        </div>
        {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
        {loading ? <div className="member-loading">{text.loading}</div> : entries.length === 0 ? <div className="member-empty">{text.emptyFolder}</div> : (
          <table className="member-file-table">
            <thead><tr><th>{text.name}</th><th>{text.size}</th><th>{text.modified}</th><th>{text.actions}</th></tr></thead>
            <tbody>{entries.map((entry) => <tr key={`${entry.kind}-${entry.relativePath}`}>
              <td><div className="member-file-name"><FileTypeIcon kind={entry.kind} name={entry.name} className={`member-file-icon ${entry.kind}`} />{entry.kind === 'dir' ? <button type="button" onClick={() => void loadDirectory(activeSource, entry.relativePath)}>{entry.name}</button> : <span>{entry.name}</span>}</div></td>
              <td>{entry.kind === 'dir' ? '—' : new Intl.NumberFormat(locale).format(entry.size)}</td>
              <td>{entry.modifiedAt ? new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(entry.modifiedAt)) : '—'}</td>
              <td>{entry.kind === 'dir' ? <button type="button" onClick={() => void loadDirectory(activeSource, entry.relativePath)}>{text.open}</button> : '—'}</td>
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
