// Corona, the one dark theme of every shell page (ADR-0014, docs/specs/desktop.md 3.4). Every colour comes from
// packages/ui/brand/tokens.json; nothing here repeats a hex value except TOPBAR_BG (the top bar's own shade).
// Rules from the ADR: gold is scarce (only the main action of a screen: contained buttons, the focus ring, the selected
// tab's line), quiet dark surfaces, text >= 4.5:1 on its surface, a 3 px focus ring that is always visible, and status
// never shown by colour alone (each status chip and alert also carries a word or a mark).
import tokens from '@localai/ui/brand/tokens.json';
import { alpha, createTheme, type Theme } from '@mui/material/styles';

export const corona = tokens.color;
export const coronaRadius = tokens.radius;

/** The top bar and the survival strip: one step below the page background, so the bar reads as the frame. */
export const TOPBAR_BG = '#0B0B0E';

const focusRing = { outline: `3px solid ${corona.focus}`, outlineOffset: 2 } as const;

/** The status colours: their tint background, line and foreground, for alerts. */
const STATUS = {
  success: { fg: corona.ok, bg: corona.okBg, line: alpha(corona.ok, 0.35) },
  warning: { fg: corona.warn, bg: corona.warnBg, line: corona.warnLine },
  error: { fg: corona.bad, bg: corona.badBg, line: corona.badLine },
  info: { fg: corona.info, bg: corona.infoBg, line: corona.infoLine },
} as const;

export const coronaTheme: Theme = createTheme({
  palette: {
    mode: 'dark',
    background: { default: corona.bg, paper: corona.surface },
    text: { primary: corona.text, secondary: corona.textMuted, disabled: corona.textDim },
    divider: corona.line,
    primary: { main: corona.gold, light: corona.goldLight, dark: corona.gold, contrastText: corona.onGold },
    // The status colours are light: dark text on them (>= 7:1, see tests/unit/theme.test.ts).
    success: { main: corona.ok, contrastText: corona.bg },
    warning: { main: corona.warn, contrastText: corona.bg },
    error: { main: corona.bad, contrastText: corona.bg },
    info: { main: corona.info, contrastText: corona.bg },
    action: { hover: alpha(corona.text, 0.06), selected: alpha(corona.text, 0.1), focus: alpha(corona.focus, 0.24) },
  },
  shape: { borderRadius: coronaRadius.control },
  typography: { fontFamily: `"${tokens.font}", system-ui, sans-serif`, button: { textTransform: 'none', fontWeight: 700 } },
  components: {
    MuiCssBaseline: {
      styleOverrides: {
        body: { backgroundColor: corona.bg, color: corona.text, colorScheme: 'dark' },
        ':focus-visible': focusRing,
        '::selection': { backgroundColor: alpha(corona.gold, 0.35) },
      },
    },
    MuiButtonBase: { defaultProps: { disableRipple: true } },
    MuiButton: {
      styleOverrides: {
        root: { borderRadius: coronaRadius.control, '&.Mui-focusVisible': focusRing },
        contained: { boxShadow: 'none', '&:hover': { boxShadow: 'none' } },
      },
      variants: [
        {
          props: { variant: 'contained', color: 'primary' },
          style: { '&:hover': { backgroundColor: corona.goldHover }, '&.Mui-disabled': { backgroundColor: corona.raised, color: corona.textDim } },
        },
        // Gold only on the main action: outlined and text buttons of the primary colour are quiet (light text, a neutral
        // line); the line turns gold on hover so the pointer still shows what it is on.
        {
          props: { variant: 'outlined', color: 'primary' },
          style: { color: corona.text, borderColor: corona.lineStrong, '&:hover': { borderColor: corona.gold, backgroundColor: alpha(corona.text, 0.06) } },
        },
        { props: { variant: 'text', color: 'primary' }, style: { color: corona.text, '&:hover': { backgroundColor: alpha(corona.text, 0.06) } } },
      ],
    },
    MuiIconButton: { styleOverrides: { root: { '&.Mui-focusVisible': focusRing } } },
    MuiToggleButton: {
      styleOverrides: {
        root: {
          color: corona.textMuted,
          borderColor: corona.lineStrong,
          '&.Mui-selected': { color: corona.text, backgroundColor: corona.raised },
          '&.Mui-focusVisible': focusRing,
        },
      },
    },
    MuiPaper: {
      // No light overlay on raised dark paper: elevation is shown by the surface steps of the tokens.
      styleOverrides: { root: { backgroundImage: 'none' }, outlined: { borderColor: corona.line } },
    },
    MuiDialog: { styleOverrides: { paper: { backgroundColor: corona.surface2, borderRadius: coronaRadius.card, border: `1px solid ${corona.line}` } } },
    MuiOutlinedInput: {
      styleOverrides: {
        root: {
          backgroundColor: corona.surface2,
          '& .MuiOutlinedInput-notchedOutline': { borderColor: corona.lineStrong },
          '&:hover .MuiOutlinedInput-notchedOutline': { borderColor: corona.textDim },
          '&.Mui-focused .MuiOutlinedInput-notchedOutline': { borderColor: corona.focus, borderWidth: 3 },
        },
      },
    },
    MuiInputLabel: { styleOverrides: { root: { color: corona.textMuted, '&.Mui-focused': { color: corona.text } } } },
    MuiChip: {
      styleOverrides: {
        root: { fontWeight: 700, '&.Mui-focusVisible': focusRing },
        filled: { '&.MuiChip-colorDefault': { backgroundColor: corona.raised, color: corona.text } },
        outlined: { '&.MuiChip-colorDefault': { borderColor: corona.lineStrong, color: corona.text } },
      },
    },
    MuiAlert: {
      styleOverrides: {
        root: { borderRadius: coronaRadius.control, border: '1px solid', color: corona.text },
      },
      // Every alert of the shell pages is the standard variant: a dark tint of its status colour, light text, the icon in
      // the status colour (the icon and the words carry the meaning too).
      variants: (Object.keys(STATUS) as (keyof typeof STATUS)[]).map((severity) => ({
        props: { severity },
        style: { color: corona.text, backgroundColor: STATUS[severity].bg, borderColor: STATUS[severity].line, '& .MuiAlert-icon': { color: STATUS[severity].fg } },
      })),
    },
    MuiTabs: { styleOverrides: { indicator: { backgroundColor: corona.gold, height: 3 } } },
    MuiTab: {
      styleOverrides: {
        root: {
          color: corona.textMuted,
          fontWeight: 700,
          '&.Mui-selected': { color: corona.text },
          '&.Mui-focusVisible': { ...focusRing, outlineOffset: -3 },
        },
      },
    },
    MuiLinearProgress: { styleOverrides: { root: { backgroundColor: corona.raised } } },
    MuiDivider: { styleOverrides: { root: { borderColor: corona.line } } },
  },
});
