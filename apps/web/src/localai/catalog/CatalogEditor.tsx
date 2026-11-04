// The draft editor (docs/specs/cloud-app.md 3.11, 4.6): a file tree, a plain textarea with line numbers, the lint
// panel of the saved version, and the Save draft / Approve buttons.
//
// Versions are immutable: "Save draft" always creates a NEW version from the text on screen (`POST /versions`, lint
// queued by the server). The editor then follows that new version: it polls it every 2 s until the lint has a
// verdict (`useCatalogVersion`) and the lint panel updates without a reload. Approve is enabled only for a saved
// version whose status is `valid` and whose text has not been edited since.
import CloseIcon from '@mui/icons-material/Close';
import DescriptionOutlinedIcon from '@mui/icons-material/DescriptionOutlined';
import { Alert, Badge, Box, Button, CircularProgress, IconButton, List, ListItemButton, ListItemText, TextField, Typography } from '@mui/material';
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';

import { errorMessage, type CatalogApi, type CatalogVersionSummary, type LintProblem } from '../api/catalog';
import { byteLength, compareCatalogPaths, formatBytes, isCatalogVersion, isGreaterThanAll, MAX_CATALOG_BYTES, profilePath, suggestNextVersion, VERSION_FILE, compareVersions } from './catalogRules';
import { LineNumberTextarea, type LineNumberTextareaHandle } from './LineNumberTextarea';
import { LintPanel } from './LintPanel';
import { useCatalogVersion } from './useCatalog';
import { locateYamlPath } from './yamlLocate';

export type EditorSource = { kind: 'new'; baseVersion: string | null } | { kind: 'edit'; version: string };

export interface CatalogEditorProps {
  client: CatalogApi;
  source: EditorSource;
  /** Every version the cloud has (for the "greater than all" rule and the lint verdict of the saved one). */
  versions: readonly CatalogVersionSummary[];
  /** The instant the version suggestion counts from (tests). */
  today?: Date;
  onSaved(version: string): void;
  onApprove(version: string): void;
  onClose(): void;
  /** Called when "there are unsaved edits" changes, so the page can ask before it throws them away. */
  onDirtyChange?(dirty: boolean): void;
}

type Files = Record<string, string>;

const sameFiles = (a: Files, b: Files) => {
  const keys = Object.keys(a);
  return keys.length === Object.keys(b).length && keys.every((key) => a[key] === b[key]);
};

function filesFromDetail(files: ReadonlyArray<{ path: string; content: string }>): Files {
  const map: Files = {};
  for (const file of files) if (file.path !== VERSION_FILE) map[file.path] = file.content;
  return map;
}

export function CatalogEditor(props: CatalogEditorProps) {
  const { client, source } = props;
  const baseVersion = source.kind === 'edit' ? source.version : source.baseVersion;
  const [loaded, setLoaded] = useState<Files | null>(baseVersion === null ? { 'models.yaml': '', 'ark.yaml': '' } : null);
  const [loadError, setLoadError] = useState<Error | null>(null);

  useEffect(() => {
    if (baseVersion === null) return undefined;
    let cancelled = false;
    client
      .get(baseVersion)
      .then((detail) => {
        if (!cancelled) setLoaded(filesFromDetail(detail.files));
      })
      .catch((caught: unknown) => {
        if (!cancelled) setLoadError(caught instanceof Error ? caught : new Error(String(caught)));
      });
    return () => {
      cancelled = true;
    };
  }, [client, baseVersion]);

  const title = source.kind === 'edit' ? `Edit ${source.version}` : source.baseVersion ? `New draft from ${source.baseVersion}` : 'New draft';

  if (loadError) {
    return (
      <Box component="section" aria-label={title} sx={{ border: 1, borderColor: 'divider', borderRadius: 1, p: 2 }}>
        <Alert severity="error" action={<Button color="inherit" size="small" onClick={props.onClose}>Close</Button>}>
          Could not load {baseVersion} for editing.
        </Alert>
      </Box>
    );
  }
  if (loaded === null) {
    return (
      <Box component="section" aria-label={title} sx={{ display: 'grid', placeItems: 'center', minHeight: 160 }}>
        <CircularProgress aria-label="Loading the catalog files" />
      </Box>
    );
  }
  return <EditorBody {...props} title={title} initialFiles={loaded} baseVersion={baseVersion} />;
}

function EditorBody({
  client,
  source,
  versions,
  today,
  onSaved,
  onApprove,
  onClose,
  onDirtyChange,
  title,
  initialFiles,
  baseVersion,
}: CatalogEditorProps & { title: string; initialFiles: Files; baseVersion: string | null }) {
  const existing = useMemo(() => versions.map((version) => version.version), [versions]);
  const [files, setFiles] = useState<Files>(initialFiles);
  const [baseline, setBaseline] = useState<Files>(initialFiles);
  const [selected, setSelected] = useState<string>(initialFiles['models.yaml'] !== undefined ? 'models.yaml' : (Object.keys(initialFiles).sort(compareCatalogPaths)[0] ?? VERSION_FILE));
  const [versionField, setVersionField] = useState(() => suggestNextVersion(existing, today ?? new Date()));
  const [notes, setNotes] = useState('');
  const [savedVersion, setSavedVersion] = useState<string | null>(source.kind === 'edit' ? source.version : null);
  const [base, setBase] = useState<string | null>(baseVersion);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [profileName, setProfileName] = useState('');
  const [profileError, setProfileError] = useState<string | null>(null);
  const [jump, setJump] = useState<LintProblem | null>(null);
  const textareaRef = useRef<LineNumberTextareaHandle>(null);

  // The saved version's verdict: polled every 2 s while the lint has not written one, and reloaded when the list sees
  // a new `lintedAt` (a re-lint asked for from the table).
  const summary = versions.find((version) => version.version === savedVersion) ?? null;
  const saved = useCatalogVersion(client, savedVersion, summary ? `${summary.status}|${summary.lintedAt}` : null);
  const detail = saved.data;

  const dirty = !sameFiles(files, baseline);
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange?.(false), [onDirtyChange]);

  const paths = useMemo(() => [VERSION_FILE, ...Object.keys(files)].sort(compareCatalogPaths), [files]);
  const text = selected === VERSION_FILE ? `${versionField}\n` : (files[selected] ?? '');
  const problemsByFile = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const problem of detail?.lintResult?.problems ?? []) counts[problem.file] = (counts[problem.file] ?? 0) + 1;
    return counts;
  }, [detail]);

  const totalBytes = useMemo(() => Object.values(files).reduce((sum, content) => sum + byteLength(content), 0) + byteLength(`${versionField}\n`), [files, versionField]);
  const tooLarge = totalBytes > MAX_CATALOG_BYTES;

  const newest = useMemo(() => [...existing].filter(isCatalogVersion).sort(compareVersions).pop() ?? null, [existing]);
  const versionError = !isCatalogVersion(versionField)
    ? 'Use YYYY.MM.DD-N, for example 2026.11.03-1.'
    : !isGreaterThanAll(versionField, existing)
      ? `Must be greater than ${newest}, the newest existing version.`
      : null;

  // Jump to a lint problem: open its file, then (best effort) its line.
  useEffect(() => {
    if (!jump || selected !== jump.file) return;
    const content = selected === VERSION_FILE ? `${versionField}\n` : (files[selected] ?? '');
    const line = jump.path ? locateYamlPath(content, jump.path) : null;
    textareaRef.current?.jumpToLine(line ?? 1);
    setJump(null);
  }, [jump, selected, files, versionField]);

  const onSelectProblem = useCallback(
    (problem: LintProblem) => {
      if (problem.file === VERSION_FILE || problem.file in files) {
        setSelected(problem.file);
        setJump(problem);
      }
    },
    [files],
  );

  const onChangeText = (value: string) => {
    if (selected === VERSION_FILE) return;
    setFiles((current) => ({ ...current, [selected]: value }));
  };

  const addProfile = (event: FormEvent) => {
    event.preventDefault();
    const path = profilePath(profileName);
    if (!path) {
      setProfileError('Use lower-case letters, digits and dashes, for example small-8gb.');
      return;
    }
    if (path in files) {
      setProfileError(`${path} already exists.`);
      return;
    }
    setFiles((current) => ({ ...current, [path]: '' }));
    setSelected(path);
    setProfileName('');
    setProfileError(null);
  };

  const removeProfile = (path: string) => {
    setFiles((current) => {
      const next = { ...current };
      delete next[path];
      return next;
    });
    if (selected === path) setSelected('models.yaml' in files ? 'models.yaml' : VERSION_FILE);
  };

  const save = async () => {
    setSaving(true);
    setSaveError(null);
    try {
      const body = {
        version: versionField,
        ...(base ? { baseVersion: base } : {}),
        files: Object.keys(files)
          .sort(compareCatalogPaths)
          .map((path) => ({ path, content: files[path]! })),
        ...(notes.trim() ? { notes: notes.trim() } : {}),
        source: 'editor' as const,
      };
      await client.createDraft(body);
      const next = suggestNextVersion([...existing, versionField], today ?? new Date());
      setSavedVersion(versionField);
      setBase(versionField);
      setBaseline(files);
      setNotes('');
      setVersionField(next);
      onSaved(versionField);
    } catch (caught) {
      setSaveError(errorMessage(caught, 'Could not save the draft. Try again.'));
    } finally {
      setSaving(false);
    }
  };

  const canApprove = savedVersion !== null && detail?.status === 'valid' && !dirty;
  const approveHint =
    savedVersion === null
      ? 'Save the draft first; Approve turns on when the checker finds no problems.'
      : dirty
        ? 'You changed the text after saving. Save the draft again to check it.'
        : detail?.status === 'valid'
          ? null
          : detail && detail.lintedAt === null
            ? 'Waiting for the checker.'
            : 'Approve turns on when the saved draft has no lint problems.';

  return (
    <Box component="section" aria-label={title} data-testid="catalog-editor" sx={{ display: 'flex', flexDirection: 'column', gap: 2, border: 1, borderColor: 'divider', borderRadius: 1, p: { xs: 1.5, sm: 2 } }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 1 }}>
        <Typography variant="h6" component="h2">
          {title}
        </Typography>
        <IconButton onClick={onClose} aria-label="Close the editor" size="small">
          <CloseIcon />
        </IconButton>
      </Box>

      <Box sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', md: '240px minmax(0, 1fr)' }, alignItems: 'start' }}>
        <Box>
          <List dense disablePadding aria-label="Catalog files" sx={{ border: 1, borderColor: 'divider', borderRadius: 1 }}>
            {paths.map((path) => (
              <Box component="li" key={path} sx={{ display: 'flex', alignItems: 'center' }}>
                <ListItemButton selected={selected === path} onClick={() => setSelected(path)} aria-current={selected === path ? 'true' : undefined} sx={{ minWidth: 0 }}>
                  <DescriptionOutlinedIcon fontSize="small" sx={{ mr: 1, color: 'text.secondary' }} aria-hidden="true" />
                  <ListItemText primary={path} slotProps={{ primary: { sx: { fontFamily: 'monospace', fontSize: 13, overflowWrap: 'anywhere' } } }} />
                  {problemsByFile[path] ? (
                    <Badge color="error" badgeContent={problemsByFile[path]} sx={{ mr: 1 }} aria-label={`${problemsByFile[path]} lint problems`} />
                  ) : null}
                </ListItemButton>
                {path.startsWith('profiles/') ? (
                  <IconButton size="small" onClick={() => removeProfile(path)} aria-label={`Remove ${path}`}>
                    <CloseIcon fontSize="small" />
                  </IconButton>
                ) : null}
              </Box>
            ))}
          </List>
          <Box component="form" onSubmit={addProfile} sx={{ display: 'flex', gap: 1, mt: 1, alignItems: 'flex-start' }} noValidate>
            <TextField
              size="small"
              label="New profile"
              value={profileName}
              onChange={(event) => setProfileName(event.target.value)}
              error={profileError !== null}
              helperText={profileError ?? 'profiles/<name>.yaml'}
              autoComplete="off"
              slotProps={{ htmlInput: { spellCheck: false, autoCapitalize: 'off' } }}
              sx={{ flex: 1, minWidth: 0 }}
            />
            <Button type="submit" size="small" variant="outlined" sx={{ mt: 0.5 }}>
              Add profile
            </Button>
          </Box>
        </Box>

        <Box sx={{ minWidth: 0 }}>
          <Typography variant="caption" color="text.secondary" component="p" sx={{ mb: 0.5, fontFamily: 'monospace' }}>
            {selected}
            {selected === VERSION_FILE ? ' (set from the Version field)' : ''}
          </Typography>
          <LineNumberTextarea ref={textareaRef} label={`Contents of ${selected}`} value={text} readOnly={selected === VERSION_FILE} onChange={onChangeText} />
        </Box>
      </Box>

      {savedVersion !== null ? (
        <LintPanel
          version={savedVersion}
          status={detail?.status ?? summary?.status ?? 'draft'}
          lintedAt={detail ? detail.lintedAt : null}
          lintResult={detail?.lintResult ?? null}
          stale={dirty}
          onSelect={onSelectProblem}
        />
      ) : (
        <Typography variant="body2" color="text.secondary">
          Save the draft to run the catalog checker on it.
        </Typography>
      )}

      <Box sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', sm: '220px minmax(0, 1fr)' } }}>
        <TextField
          label="Version"
          value={versionField}
          onChange={(event) => setVersionField(event.target.value.trim())}
          error={versionError !== null}
          helperText={versionError ?? 'Must be greater than every existing version.'}
          autoComplete="off"
          slotProps={{ htmlInput: { spellCheck: false, autoCapitalize: 'off' } }}
        />
        <TextField label="Notes" value={notes} onChange={(event) => setNotes(event.target.value)} multiline minRows={1} maxRows={4} slotProps={{ htmlInput: { maxLength: 2000 } }} />
      </Box>

      <Typography variant="caption" color={tooLarge ? 'error' : 'text.secondary'} role={tooLarge ? 'alert' : undefined}>
        {tooLarge ? `The catalog is ${formatBytes(totalBytes)}; the limit is 2 MB.` : `${formatBytes(totalBytes)} of 2 MB`}
      </Typography>

      {saveError ? <Alert severity="error">{saveError}</Alert> : null}

      <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap', alignItems: 'center' }}>
        <Button variant="contained" onClick={() => void save()} disabled={saving || versionError !== null || tooLarge}>
          {saving ? 'Saving…' : 'Save draft'}
        </Button>
        <Button variant="outlined" onClick={() => savedVersion && onApprove(savedVersion)} disabled={!canApprove} aria-describedby={approveHint ? 'approve-hint' : undefined}>
          Approve
        </Button>
        {approveHint ? (
          <Typography id="approve-hint" variant="caption" color="text.secondary">
            {approveHint}
          </Typography>
        ) : null}
      </Box>
    </Box>
  );
}
