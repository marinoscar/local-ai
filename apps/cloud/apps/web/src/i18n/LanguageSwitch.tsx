import { ToggleButton, ToggleButtonGroup } from '@mui/material';

import { useLanguage, useT, type Lang } from './LanguageProvider';

/**
 * The language switch of the top bar: two buttons, EN and ES, each with the full name as its accessible name, so a
 * screen reader says "Español" and not "ES". The word is always there; nothing is told by colour alone.
 */
export function LanguageSwitch() {
  const { lang, setLang } = useLanguage();
  const t = useT();
  return (
    <ToggleButtonGroup
      size="small"
      exclusive
      value={lang}
      aria-label={t('Language', 'Idioma')}
      onChange={(_event, next: Lang | null) => {
        if (next !== null) setLang(next);
      }}
      sx={{ mr: 1 }}
    >
      <ToggleButton value="en" aria-label="English" lang="en" sx={{ px: 1, py: 0.25, color: 'inherit' }}>
        EN
      </ToggleButton>
      <ToggleButton value="es" aria-label="Español" lang="es" sx={{ px: 1, py: 0.25, color: 'inherit' }}>
        ES
      </ToggleButton>
    </ToggleButtonGroup>
  );
}
