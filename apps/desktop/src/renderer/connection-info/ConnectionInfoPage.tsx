// The connection-info window (docs/specs/desktop.md 4.6): the addresses, the key (masked until Reveal) with Copy and
// Rotate, and ready snippets for OpenCode, Open WebUI and curl. Everything comes from the main process through
// `window.localaiDesktop`; the page never calls the agent itself and never holds the key unless the user pressed Reveal
// (it forgets it on Hide, after a minute, and after a rotation).
import { Alert, Box, Button, Divider, Stack, Tab, Tabs, Typography } from '@mui/material';
import { type ReactElement, useCallback, useEffect, useRef, useState } from 'react';
import type { ConnectionInfo, KeyFailure, Lang, LocalaiDesktop, RotateKeyResult } from '../../shared/types';
import { OPENCODE_WRITE_COMMAND, curlModelsCommand, describeFailure, renderSnippet } from './snippets';
import { STRINGS, fill, pick } from './strings';

const REVEAL_MS = 60_000;

type Problem = { text: string; code?: string; fix: string };
type RotateState = { kind: 'idle' } | { kind: 'running' } | { kind: 'done'; result: Extract<RotateKeyResult, { ok: true }> } | { kind: 'kept'; fingerprint?: string };

const Mono = ({ children, testId }: { children: string; testId?: string }): ReactElement => (
  <Box component="pre" data-testid={testId} sx={{ m: 0, p: 1.5, bgcolor: 'action.hover', borderRadius: 1, fontFamily: 'ui-monospace, monospace', fontSize: '0.8125rem', whiteSpace: 'pre-wrap', wordBreak: 'break-all', userSelect: 'text' }}>
    {children}
  </Box>
);

function ProblemAlert({ problem, lang }: { problem: Problem; lang: Lang }): ReactElement {
  return (
    <Alert severity="error" role="alert" data-testid="problem">
      <strong>{problem.text}</strong>
      <br />
      {pick(STRINGS.fix, lang)}: {problem.fix}
      {problem.code ? <Typography component="div" variant="caption">{problem.code}</Typography> : null}
    </Alert>
  );
}

export function ConnectionInfoPage({ bridge }: { bridge?: LocalaiDesktop }): ReactElement {
  const lang: Lang = bridge?.lang() ?? 'en';
  const [info, setInfo] = useState<ConnectionInfo | null>(null);
  const [problem, setProblem] = useState<Problem | null>(null);
  const [key, setKey] = useState<string | null>(null);
  const [tab, setTab] = useState<'opencode' | 'webui' | 'curl'>('opencode');
  const [copied, setCopied] = useState<string | null>(null);
  const [rotate, setRotate] = useState<RotateState>({ kind: 'idle' });
  const hideTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const fail = useCallback((failure: KeyFailure, fallback: { text: string; fix: string }) => setProblem(describeFailure(failure, fallback)), []);
  const forget = useCallback(() => {
    if (hideTimer.current) clearTimeout(hideTimer.current);
    hideTimer.current = null;
    setKey(null);
  }, []);

  const load = useCallback(async () => {
    if (!bridge) return;
    try {
      const result = await bridge.connectionInfo();
      if (result.ok) {
        setInfo(result);
        setProblem(null);
      } else fail(result, { text: pick(STRINGS.loadFailed, lang), fix: pick(STRINGS.loadFailedFix, lang) });
    } catch {
      setProblem({ text: pick(STRINGS.loadFailed, lang), fix: pick(STRINGS.loadFailedFix, lang) });
    }
  }, [bridge, fail, lang]);

  useEffect(() => {
    void load();
    return () => {
      if (hideTimer.current) clearTimeout(hideTimer.current);
    };
  }, [load]);

  const generic = { text: pick(STRINGS.actionFailed, lang), fix: pick(STRINGS.actionFailedFix, lang) };

  const reveal = async (): Promise<void> => {
    if (!bridge) return;
    try {
      const result = await bridge.revealKey();
      if (!result.ok) return fail(result, generic);
      setProblem(null);
      setKey(result.key);
      hideTimer.current = setTimeout(forget, REVEAL_MS);
    } catch {
      setProblem({ ...generic });
    }
  };

  const copy = async (): Promise<void> => {
    if (!bridge) return;
    try {
      const result = await bridge.copyApiKey();
      if (!result.ok) return fail(result, generic);
      setProblem(null);
      setCopied(fill(pick(STRINGS.copied, lang), { n: result.clearsInS }));
    } catch {
      setProblem({ ...generic });
    }
  };

  const doRotate = async (): Promise<void> => {
    if (!bridge || rotate.kind === 'running') return;
    forget();
    setCopied(null);
    setRotate({ kind: 'running' });
    try {
      const result = await bridge.rotateKey();
      if (result.ok) {
        setRotate({ kind: 'done', result });
        setProblem(null);
        void load();
        return;
      }
      if (result.cancelled) return setRotate({ kind: 'idle' });
      // A failure after the key was written: the old key is dead whatever the error says.
      setRotate(result.keyRotated ? { kind: 'kept', ...(result.fingerprint !== undefined ? { fingerprint: result.fingerprint } : {}) } : { kind: 'idle' });
      if (result.keyRotated) void load();
      fail(result, generic);
    } catch {
      setRotate({ kind: 'idle' });
      setProblem({ ...generic });
    }
  };

  const shown = (text: string): string => renderSnippet(text, key);
  const Row = ({ label, value, note, testId }: { label: string; value: string; note?: string; testId: string }): ReactElement => (
    <Box>
      <Typography variant="caption" color="text.secondary">{label}</Typography>
      <Mono testId={testId}>{value}</Mono>
      {note ? <Typography variant="caption" color="text.secondary">{note}</Typography> : null}
    </Box>
  );

  return (
    <Box component="main" sx={{ p: 3, maxWidth: 720, mx: 'auto' }}>
      <Stack spacing={2}>
        <Typography component="h1" variant="h5">{pick(STRINGS.title, lang)}</Typography>
        <Typography variant="body2">{pick(STRINGS.intro, lang)}</Typography>
        {problem ? <ProblemAlert problem={problem} lang={lang} /> : null}
        {!info && !problem ? <Typography>{pick(STRINGS.loading, lang)}</Typography> : null}
        {info ? (
          <>
            <Typography component="h2" variant="subtitle1">{pick(STRINGS.addresses, lang)}</Typography>
            <Row label={pick(STRINGS.local, lang)} value={info.localUrl} testId="url-local" />
            {info.tailnetUrl ? <Row label={pick(STRINGS.tailnet, lang)} value={info.tailnetUrl} testId="url-tailnet" /> : null}
            {info.publicUrl ? <Row label={pick(STRINGS.publicUrl, lang)} value={info.publicUrl} testId="url-public" /> : null}
            <Row label={pick(STRINGS.lab, lang)} value={info.labUrl} note={pick(STRINGS.labNote, lang)} testId="url-lab" />
            {info.models.length > 0 ? <Typography variant="body2">{pick(STRINGS.models, lang)}: {info.models.join(', ')}</Typography> : null}
            <Divider />
            <Typography component="h2" variant="subtitle1">{pick(STRINGS.apiKey, lang)}</Typography>
            <Mono testId="key">{key ?? info.keyMasked}</Mono>
            <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap' }}>
              <Button variant="outlined" onClick={() => (key === null ? void reveal() : forget())} data-testid="reveal">
                {pick(key === null ? STRINGS.reveal : STRINGS.hide, lang)}
              </Button>
              <Button variant="outlined" onClick={() => void copy()} data-testid="copy">{pick(STRINGS.copy, lang)}</Button>
              <Button variant="outlined" color="warning" disabled={rotate.kind === 'running'} onClick={() => void doRotate()} data-testid="rotate">
                {pick(STRINGS.rotate, lang)}
              </Button>
            </Stack>
            {copied ? <Typography role="status" variant="body2" data-testid="copied">{copied}</Typography> : null}
            {rotate.kind === 'running' ? <Typography role="status" variant="body2" data-testid="rotating">{pick(STRINGS.rotating, lang)}</Typography> : null}
            {rotate.kind === 'done' ? (
              <Alert severity="success" data-testid="rotated">
                <strong>{fill(pick(STRINGS.rotatedTitle, lang), { fp: rotate.result.fingerprint })}</strong>
                {!rotate.result.cloudSynced ? <Typography variant="body2">{pick(STRINGS.rotatedLocalOnly, lang)}</Typography> : null}
                {rotate.result.clientsToUpdate.length > 0 ? (
                  <>
                    <Typography variant="body2">{pick(STRINGS.rotatedUpdate, lang)}</Typography>
                    <ul data-testid="clients">
                      {rotate.result.clientsToUpdate.map((client) => <li key={client}>{client}</li>)}
                    </ul>
                  </>
                ) : null}
              </Alert>
            ) : null}
            {rotate.kind === 'kept' ? <Alert severity="warning" data-testid="rotated-anyway">{pick(STRINGS.rotatedAnyway, lang)}</Alert> : null}
            <Divider />
            <Tabs value={tab} onChange={(_e, value: typeof tab) => setTab(value)} aria-label={pick(STRINGS.title, lang)}>
              <Tab value="opencode" label={pick(STRINGS.tabOpencode, lang)} data-testid="tab-opencode" />
              <Tab value="webui" label={pick(STRINGS.tabWebui, lang)} data-testid="tab-webui" />
              <Tab value="curl" label={pick(STRINGS.tabCurl, lang)} data-testid="tab-curl" />
            </Tabs>
            {tab === 'opencode' ? (
              <Stack spacing={1} data-testid="panel-opencode">
                <Typography variant="body2">{pick(STRINGS.opencodeStep1, lang)}</Typography>
                <Mono testId="snippet-write">{OPENCODE_WRITE_COMMAND}</Mono>
                <Typography variant="body2">{pick(STRINGS.opencodeStep2, lang)}</Typography>
                <Mono testId="snippet-opencode">{shown(info.snippets.opencode)}</Mono>
              </Stack>
            ) : null}
            {tab === 'webui' ? (
              <Stack spacing={1} data-testid="panel-webui">
                <Typography variant="body2">{pick(STRINGS.webuiIntro, lang)}</Typography>
                <Mono testId="snippet-webui">{`${pick(STRINGS.webuiUrl, lang)}: ${info.localUrl}\n${pick(STRINGS.apiKey, lang)}: ${key ?? info.keyMasked}`}</Mono>
                <Typography variant="body2">{pick(STRINGS.webuiEnv, lang)}</Typography>
                <Mono testId="snippet-env">{shown(info.snippets.env)}</Mono>
              </Stack>
            ) : null}
            {tab === 'curl' ? (
              <Stack spacing={1} data-testid="panel-curl">
                <Typography variant="body2">{pick(STRINGS.curlModels, lang)}</Typography>
                <Mono testId="snippet-curl-models">{curlModelsCommand(info.localUrl)}</Mono>
                <Typography variant="body2">{pick(STRINGS.curlChat, lang)}</Typography>
                <Mono testId="snippet-curl">{shown(info.snippets.curl)}</Mono>
              </Stack>
            ) : null}
          </>
        ) : null}
      </Stack>
    </Box>
  );
}
