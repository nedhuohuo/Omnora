import type { MemberCollaboration, MemberContentLocator } from '../api';
import FileTypeIcon from './FileTypeIcon';
import { localeMessages, type MemberLocale } from './i18n';

type Props = {
  locale: MemberLocale;
  incoming: readonly MemberCollaboration[];
  outgoing: readonly MemberCollaboration[];
  onOpen: (locator: MemberContentLocator, label: string, readOnly: boolean) => void;
};

function labelFor(item: MemberCollaboration) {
  return item.displayName?.trim() || item.path?.trim() || '—';
}

export default function MemberCollaborationsDirectory({ locale, incoming, outgoing, onOpen }: Props) {
  const text = localeMessages[locale];
  return (
    <div className="member-page-flow">
      <div className="member-heading"><div><h1>{text.collaboration}</h1><p>{text.collaborationDetail}</p></div></div>
      <section aria-labelledby="incoming-collaborations">
        <div className="member-heading member-section-heading"><div><h2 id="incoming-collaborations">{text.incomingCollaborations}</h2></div></div>
        {incoming.length === 0 ? <div className="member-empty">{text.noIncomingCollaborations}</div> : <div className="member-space-grid">{incoming.map((item) => (
          <button className="member-space-card" type="button" key={item.id} onClick={() => onOpen({ source: 'collaboration', collaborationId: item.id, path: '.' }, labelFor(item), item.permission === 'viewer')}>
            <FileTypeIcon kind="dir" name={labelFor(item)} className="member-file-icon dir" />
            <strong>{labelFor(item)}</strong>
            <small>{item.ownerName || (item.permission === 'viewer' ? text.readOnly : text.readWrite)}</small>
          </button>
        ))}</div>}
      </section>
      <section aria-labelledby="outgoing-collaborations">
        <div className="member-heading member-section-heading"><div><h2 id="outgoing-collaborations">{text.outgoingCollaborations}</h2></div></div>
        {outgoing.length === 0 ? <div className="member-empty">{text.noOutgoingCollaborations}</div> : <div className="member-space-grid">{outgoing.map((item) => (
          <article className="member-space-card" key={item.id}>
            <FileTypeIcon kind="dir" name={labelFor(item)} className="member-file-icon dir" />
            <strong>{labelFor(item)}</strong>
            <small>{item.recipientName || (item.permission === 'viewer' ? text.readOnly : text.readWrite)}</small>
          </article>
        ))}</div>}
      </section>
    </div>
  );
}
