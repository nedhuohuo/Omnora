import { localeMessages, type MemberLocale } from './i18n';

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
      <button className={`member-nav ${active === 'personal' ? 'active' : ''}`} type="button" onClick={() => onSelect('personal')}>{text.personalSpace}</button>
      <button className={`member-nav ${active === 'team-folders' ? 'active' : ''}`} type="button" onClick={() => onSelect('team-folders')}>{text.teamFolders}</button>
      <button className={`member-nav ${active === 'collaborations' ? 'active' : ''}`} type="button" onClick={() => onSelect('collaborations')}>{text.collaboration}</button>
    </>
  );
}
