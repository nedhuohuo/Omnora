import { localeMessages, type MemberLocale } from './i18n';
import MemberIcon from './MemberIcon';

type MemberContentTab = 'personal' | 'team-folders' | 'collaborations';

type Props = {
  locale: MemberLocale;
  active: MemberContentTab | null;
  onSelect: (tab: MemberContentTab) => void;
};

export default function MemberContentNavigation({ locale, active, onSelect }: Props) {
  const text = localeMessages[locale];
  return (
    <>
      <button className={`member-nav ${active === 'personal' ? 'active' : ''}`} type="button" onClick={() => onSelect('personal')}><MemberIcon name="home" /> <span>{text.personalSpace}</span></button>
      <button className={`member-nav ${active === 'team-folders' ? 'active' : ''}`} type="button" onClick={() => onSelect('team-folders')}><MemberIcon name="folder" /> <span>{text.teamFolders}</span></button>
      <button className={`member-nav ${active === 'collaborations' ? 'active' : ''}`} type="button" onClick={() => onSelect('collaborations')}><MemberIcon name="collaboration" /> <span>{text.collaboration}</span></button>
    </>
  );
}
