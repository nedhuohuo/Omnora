import { localeMessages, type MemberLocale } from './i18n';

type MemberContentTab = 'files' | 'collaborations';

type Props = {
  locale: MemberLocale;
  active: MemberContentTab | null;
  onSelect: (tab: MemberContentTab) => void;
};

export default function MemberContentNavigation({ locale, active, onSelect }: Props) {
  const text = localeMessages[locale];
  return (
    <>
      <button className={`member-nav ${active === 'files' ? 'active' : ''}`} type="button" onClick={() => onSelect('files')}>{text.myFiles}</button>
      <button className={`member-nav ${active === 'collaborations' ? 'active' : ''}`} type="button" onClick={() => onSelect('collaborations')}>{text.collaboration}</button>
    </>
  );
}
