import FileTypeIcon from './FileTypeIcon';
import { localeMessages, type MemberLocale } from './i18n';
import { spacesForCategory, type MemberSpaceCategory } from './spaceNavigation';
import type { MemberSpace } from './types';

type MemberSpaceDirectoryProps = {
  category: MemberSpaceCategory;
  locale: MemberLocale;
  spaces: readonly MemberSpace[];
  error?: string;
  onOpen: (space: MemberSpace) => void;
};

export default function MemberSpaceDirectory({
  category,
  locale,
  spaces,
  error = '',
  onOpen,
}: MemberSpaceDirectoryProps) {
  const text = localeMessages[locale];
  const visibleSpaces = spacesForCategory(spaces, category);
  const title = category === 'personal' ? text.personalSpaces : text.teamSpaces;
  const detail = category === 'personal' ? text.personalSpacesDetail : text.teamSpacesDetail;
  const empty = category === 'personal' ? text.noPersonalSpaces : text.noTeamSpaces;
  const roleLabels = {
    viewer: text.spaceRoleViewer,
    editor: text.spaceRoleEditor,
    manager: text.spaceRoleManager,
  };

  return (
    <div className="member-page-flow member-space-directory">
      <div className="member-heading">
        <div><h1>{title}</h1><p>{detail}</p></div>
      </div>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {visibleSpaces.length === 0 ? <div className="member-empty">{empty}</div> : (
        <div className="member-space-grid">
          {visibleSpaces.map((space) => (
            <button className="member-space-card" type="button" onClick={() => onOpen(space)} key={space.id}>
              <FileTypeIcon kind="dir" name={space.name} className="member-file-icon dir" />
              <strong>{space.name}</strong>
              <small>{roleLabels[space.role]}</small>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
