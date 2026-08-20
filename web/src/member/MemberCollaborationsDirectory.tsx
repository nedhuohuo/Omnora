import { type FormEvent, useState } from 'react';
import { ApiError, createMemberCollaboration, deleteMemberCollaboration, type MemberCollaboration, type MemberContentLocator } from '../api';
import FileTypeIcon from './FileTypeIcon';
import { localeMessages, type MemberLocale } from './i18n';
import { useMemberDialog } from './MemberDialog';

type Props = {
  locale: MemberLocale;
  incoming: readonly MemberCollaboration[];
  outgoing: readonly MemberCollaboration[];
  onOpen: (locator: MemberContentLocator, label: string, readOnly: boolean) => void;
  onRefresh?: () => void;
};

function labelFor(item: MemberCollaboration) {
  return item.displayName?.trim() || item.path?.trim() || item.folderName?.trim() || '—';
}

function normalizeCollaborationPath(path: string) {
  const trimmed = path.trim();
  if (!trimmed || trimmed === '.' || trimmed === '/') return '.';
  return trimmed.replace(/^\/+|\/+$/g, '');
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

export default function MemberCollaborationsDirectory({ locale, incoming, outgoing, onOpen, onRefresh }: Props) {
  const text = localeMessages[locale];
  let confirm: ReturnType<typeof useMemberDialog>['confirm'] = async () => false;
  try {
    // eslint-disable-next-line react-hooks/rules-of-hooks
    confirm = useMemberDialog().confirm;
  } catch {
    // No provider in unit tests — fallback to no-op confirm.
  }
  const [recipientID, setRecipientID] = useState('');
  const [rootPath, setRootPath] = useState('.');
  const [permission, setPermission] = useState<'viewer' | 'editor'>('viewer');
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState('');

  async function handleCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!recipientID.trim()) return;
    setCreating(true);
    setError('');
    try {
      await createMemberCollaboration({ recipientId: recipientID.trim(), rootRelativePath: normalizeCollaborationPath(rootPath), permission });
      setRecipientID('');
      setRootPath('.');
      onRefresh?.();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setCreating(false);
    }
  }

  async function handleRevoke(id: string) {
    const confirmed = await confirm({
      title: text.collaborationRevokeConfirmTitle,
      description: text.collaborationRevokeConfirmDetail,
      confirmLabel: text.collaborationRevoke,
      cancelLabel: text.cancel,
      tone: 'danger',
    });
    if (!confirmed) return;
    setError('');
    try {
      await deleteMemberCollaboration(id);
      onRefresh?.();
    } catch (caught) {
      setError(describeError(caught));
    }
  }

  return (
    <div className="member-page-flow member-collaborations-directory">
      <div className="member-heading"><div><h1>{text.collaboration}</h1><p>{text.collaborationDetail}</p></div></div>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      <section aria-labelledby="incoming-collaborations">
        <div className="member-heading member-section-heading"><div><h2 id="incoming-collaborations">{text.incomingCollaborations}</h2></div></div>
        {incoming.length === 0 ? <div className="member-empty">{text.noIncomingCollaborations}</div> : <div className="member-content-grid">{incoming.map((item) => (
          <button className="member-content-card" type="button" key={item.id} onClick={() => onOpen({ source: 'collaboration', collaborationId: item.id, path: '.' }, labelFor(item), true)}>
            <FileTypeIcon kind="dir" name={labelFor(item)} className="member-file-icon dir" />
            <strong>{labelFor(item)}</strong>
            <small>{item.ownerName ? `${item.ownerName} · ${item.permission === 'editor' ? text.readWrite : text.readOnly}` : item.permission === 'editor' ? text.readWrite : text.readOnly}</small>
          </button>
        ))}</div>}
      </section>
      <section aria-labelledby="outgoing-collaborations">
        <div className="member-heading member-section-heading"><div><h2 id="outgoing-collaborations">{text.outgoingCollaborations}</h2></div></div>
        <form className="member-admin-inline-form" onSubmit={handleCreate}>
          <label>{text.collaborationRecipient}<input value={recipientID} onChange={(event) => setRecipientID(event.target.value)} required /></label>
          <label>{text.collaborationRoot}<input value={rootPath} onChange={(event) => setRootPath(event.target.value)} required /></label>
          <label>{text.collaborationPermission}<select value={permission} onChange={(event) => setPermission(event.target.value as 'viewer' | 'editor')}><option value="viewer">{text.readOnly}</option><option value="editor">{text.readWrite}</option></select></label>
          <button className="member-primary" type="submit" disabled={creating}>{text.collaborationCreate}</button>
        </form>
        {outgoing.length === 0 ? <div className="member-empty">{text.noOutgoingCollaborations}</div> : <ul className="member-collaboration-list">{outgoing.map((item) => (
          <li className="member-collaboration-row" key={item.id}><strong>{labelFor(item)}</strong><small>{item.recipientName ? `${item.recipientName} · ${item.permission === 'editor' ? text.readWrite : text.readOnly}` : item.permission === 'editor' ? text.readWrite : text.readOnly}</small><button type="button" onClick={() => void handleRevoke(item.id)}>{text.collaborationRevoke}</button></li>
        ))}</ul>}
      </section>
    </div>
  );
}
