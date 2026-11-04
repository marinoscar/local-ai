import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { modelSizesFromFiles, modelSizesFromYaml } from './catalog-model-sizes';

const YAML = `schema_version: 1
models:
  - id: model-one-q4
    display_name: "Model One"
    files:
      - kind: weights
        name: one.gguf
        size_bytes: 1000
        sha256: "aa"
      - kind: mmproj
        name: one-proj.gguf
        size_bytes: 250 # vision projector
  - id: "model-two-q4"
    files:
      - kind: weights
        size_bytes: 4000
other:
  - id: not-a-model
    size_bytes: 9
`;

describe('modelSizesFromYaml', () => {
  it('sums the size_bytes of each model under models: and ignores other top-level lists', () => {
    expect(modelSizesFromYaml(YAML)).toEqual({ 'model-one-q4': 1250, 'model-two-q4': 4000 });
  });

  it('leaves a model it cannot size out, and survives empty or odd input', () => {
    expect(modelSizesFromYaml('models:\n  - id: model-three\n    files: []\n')).toEqual({});
    expect(modelSizesFromYaml('')).toEqual({});
    expect(modelSizesFromYaml('models: []\r\n')).toEqual({});
  });

  it('reads every model of the repository catalog', () => {
    const text = readFileSync(join(__dirname, '../../../../../../../catalog/models.yaml'), 'utf8');
    const ids = [...text.matchAll(/^ {2}- id: (\S+)$/gm)].map((match) => match[1]!.replace(/["']/g, ''));
    const sizes = modelSizesFromYaml(text);
    expect(ids.length).toBeGreaterThan(3);
    for (const id of ids) expect(sizes[id]).toBeGreaterThan(0);
  });
});

describe('modelSizesFromFiles', () => {
  it('reads models.yaml out of a stored catalog and tolerates anything else', () => {
    expect(modelSizesFromFiles([{ path: 'ark.yaml', content: 'x' }, { path: 'models.yaml', content: YAML }])).toEqual({ 'model-one-q4': 1250, 'model-two-q4': 4000 });
    expect(modelSizesFromFiles([{ path: 'ark.yaml', content: 'x' }])).toEqual({});
    expect(modelSizesFromFiles(null)).toEqual({});
    expect(modelSizesFromFiles('nope')).toEqual({});
  });
});
