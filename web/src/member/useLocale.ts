import { useState } from 'react';
import { getStoredLocale, saveLocale, type MemberLocale } from './i18n';

export function useLocale() {
  const [locale, setLocaleState] = useState<MemberLocale>(getStoredLocale);

  function setLocale(next: MemberLocale) {
    saveLocale(next);
    setLocaleState(next);
  }

  return { locale, setLocale };
}
