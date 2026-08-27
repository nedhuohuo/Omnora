import type { MemberContentLocator, MemberContentSourcesPayload } from '../api';
import FileTypeIcon from './FileTypeIcon';
import { localeMessages, type MemberLocale } from './i18n';

type Props = {
  locale: MemberLocale;
  sources: MemberContentSourcesPayload;
  onOpen: (locator: MemberContentLocator, label: string, readOnly: boolean) => void;
};

export default function MemberContentSourceDirectory({ locale, sources, onOpen }: Props) {
  const text = localeMessages[locale];
  return (
    <div className="member-page-flow member-content-source-directory">
      <div className="member-heading"><div><h1>{text.teamFolders}</h1><p>{text.teamFoldersDetail}</p></div></div>
      {sources.commonMounts.length === 0 ? <div className="member-empty">{text.noTeamFolders}</div> : (
        <div className="member-content-grid">
          {sources.commonMounts.map((mount) => {
            const readOnly = mount.permission === 'viewer' || mount.mode === 'read_only';
            return (
              <button className="member-content-card" type="button" key={mount.mountId} onClick={() => onOpen({ source: 'common_mount', mountId: mount.mountId, path: '.' }, mount.displayName, readOnly)}>
                <FileTypeIcon kind="dir" name={mount.displayName} className="member-file-icon dir" />
                <strong>{mount.displayName}</strong>
                {readOnly && <small>{text.readOnly}</small>}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
