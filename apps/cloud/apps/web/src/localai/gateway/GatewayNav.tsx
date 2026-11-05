// The pages of the gateway destination (/gateway/*): a link row at the top of each page, so the pages reach each other
// without a settings card (a destination owns its pages; docs/specs/ux-refresh.md). Later pages (Models) append a link.
import { Box, Button } from '@mui/material';
import { Link as RouterLink, useLocation } from 'react-router-dom';

export const GATEWAY_PAGES: ReadonlyArray<{ path: string; label: string }> = [
  { path: '/gateway/keys', label: 'API keys' },
  { path: '/gateway/usage', label: 'Usage' },
];

export function GatewayNav() {
  const { pathname } = useLocation();
  return (
    <Box component="nav" aria-label="Gateway pages" sx={{ display: 'flex', gap: 1, flexWrap: 'wrap' }}>
      {GATEWAY_PAGES.map((page) => {
        const current = pathname === page.path || pathname.startsWith(`${page.path}/`);
        return (
          <Button key={page.path} component={RouterLink} to={page.path} size="small" variant={current ? 'contained' : 'outlined'} aria-current={current ? 'page' : undefined}>
            {page.label}
          </Button>
        );
      })}
    </Box>
  );
}
