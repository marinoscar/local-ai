// The three ready-to-paste snippets of the show-once panel: curl, OpenCode and Open WebUI (docs/specs/gateway.md
// Appendix A). None of them contains the key: the shell snippet reads `$LAI_KEY`, OpenCode reads a private file, and Open
// WebUI is told to paste the key into its own field. The component does not even receive the token.
import { Box, Tab, Tabs, Typography } from '@mui/material';
import { useId, useState } from 'react';

import { CodeBlock } from '../wizard/CodeBlock';
import { CopyButton } from '../wizard/CopyButton';
import { curlSnippet, gatewayBaseUrl, opencodeSnippet, openWebUiFields, storeKeySnippet } from './keySnippets';

export interface KeySnippetsProps {
  /** The page's origin (`https://ai.marin.cr`). */
  origin: string;
  /** The models the key may use; empty = all (the OpenCode block then shows a placeholder to replace). */
  models: readonly string[];
}

type Snippet = 'curl' | 'opencode' | 'openwebui';

export function KeySnippets({ origin, models }: KeySnippetsProps) {
  const [tab, setTab] = useState<Snippet>('curl');
  const id = useId();
  const baseUrl = gatewayBaseUrl(origin);
  const curl = curlSnippet(baseUrl);
  const store = storeKeySnippet();
  const opencode = opencodeSnippet(baseUrl, models);
  const fields = openWebUiFields(baseUrl);
  const panel = (name: Snippet) => ({ id: `${id}-${name}-panel`, 'aria-labelledby': `${id}-${name}-tab`, hidden: tab !== name, role: 'tabpanel' as const });
  const tabProps = (name: Snippet) => ({ id: `${id}-${name}-tab`, 'aria-controls': `${id}-${name}-panel`, value: name });

  return (
    <Box>
      <Tabs value={tab} onChange={(_event, value: Snippet) => setTab(value)} aria-label="Ways to use the key" variant="scrollable" scrollButtons="auto">
        <Tab label="curl" {...tabProps('curl')} />
        <Tab label="OpenCode" {...tabProps('opencode')} />
        <Tab label="Open WebUI" {...tabProps('openwebui')} />
      </Tabs>

      <Box {...panel('curl')} sx={{ pt: 1.5, display: tab === 'curl' ? 'flex' : 'none', flexDirection: 'column', gap: 1 }}>
        <Typography variant="body2">1. Save the key in a private file (paste it, press Enter, then Ctrl+D):</Typography>
        <CodeBlock label="Store the key in a private file">{store}</CodeBlock>
        <CopyButton text={store} context="the command that stores the key" />
        <Typography variant="body2">2. List the models the key can use:</Typography>
        <CodeBlock label="curl command">{curl}</CodeBlock>
        <CopyButton text={curl} context="the curl command" />
      </Box>

      <Box {...panel('opencode')} sx={{ pt: 1.5, display: tab === 'opencode' ? 'flex' : 'none', flexDirection: 'column', gap: 1 }}>
        <Typography variant="body2">1. Save the key in a private file (paste it, press Enter, then Ctrl+D):</Typography>
        <CodeBlock label="Store the key in a private file">{store}</CodeBlock>
        <CopyButton text={store} context="the command that stores the key" />
        <Typography variant="body2">
          2. Add this provider to <code>~/.config/opencode/opencode.json</code>
          {models.length === 0 ? ' (replace MODEL_ID with an id from the curl command)' : ''}:
        </Typography>
        <CodeBlock label="OpenCode provider block">{opencode}</CodeBlock>
        <CopyButton text={opencode} context="the OpenCode provider block" />
      </Box>

      <Box {...panel('openwebui')} sx={{ pt: 1.5, display: tab === 'openwebui' ? 'flex' : 'none', flexDirection: 'column', gap: 1 }}>
        <Typography variant="body2">In Open WebUI, go to Admin Settings, Connections, add an OpenAI connection with:</Typography>
        <Box component="dl" sx={{ m: 0, display: 'grid', gridTemplateColumns: 'max-content 1fr', columnGap: 2, rowGap: 0.5 }}>
          {fields.map((field) => (
            <Box key={field.label} sx={{ display: 'contents' }}>
              <Typography component="dt" variant="body2" sx={{ fontWeight: 600 }}>
                {field.label}
              </Typography>
              <Typography component="dd" variant="body2" sx={{ m: 0, overflowWrap: 'anywhere', fontFamily: field.label === 'URL' ? 'monospace' : undefined }}>
                {field.value}
              </Typography>
            </Box>
          ))}
        </Box>
        <Box>
          <CopyButton text={fields[0]!.value} context="the Open WebUI URL" />
        </Box>
      </Box>
    </Box>
  );
}
