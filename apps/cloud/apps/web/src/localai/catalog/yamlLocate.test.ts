import { locateYamlPath, tokenizePath } from './yamlLocate';

const MODELS = [
  'schema_version: 1', // 1
  'models:', // 2
  '  - id: model-one', // 3
  '    source:', // 4
  '      revision: main', // 5
  '  - id: model-two', // 6
  '    source:', // 7
  '      revision: dev', // 8
  '    files:', // 9
  '      - name: a', // 10
  '      - name: b', // 11
  'other:', // 12
  '  source:', // 13
  '    revision: x', // 14
].join('\n');

describe('tokenizePath', () => {
  it('splits keys and list indexes', () => {
    expect(tokenizePath('models[3].source.revision')).toEqual([{ key: 'models' }, { index: 3 }, { key: 'source' }, { key: 'revision' }]);
    expect(tokenizePath('a[0][1].b-c')).toEqual([{ key: 'a' }, { index: 0 }, { index: 1 }, { key: 'b-c' }]);
    expect(tokenizePath('schema_version')).toEqual([{ key: 'schema_version' }]);
    expect(tokenizePath('')).toEqual([]);
  });
});

describe('locateYamlPath', () => {
  it('walks the path: the right list item, not the first match of the last key', () => {
    expect(locateYamlPath(MODELS, 'models[0].source.revision')).toBe(5);
    expect(locateYamlPath(MODELS, 'models[1].source.revision')).toBe(8);
    expect(locateYamlPath(MODELS, 'models[1].files[1].name')).toBe(11);
    expect(locateYamlPath(MODELS, 'other.source.revision')).toBe(14);
  });

  it('finds a key at the top, and a list item itself', () => {
    expect(locateYamlPath(MODELS, 'schema_version')).toBe(1);
    expect(locateYamlPath(MODELS, 'models')).toBe(2);
    expect(locateYamlPath(MODELS, 'models[1]')).toBe(6);
  });

  it('handles a list written at the same indent as its key', () => {
    const yaml = ['items:', '- id: a', '  rev: 1', '- id: b', '  rev: 2'].join('\n');
    expect(locateYamlPath(yaml, 'items[1].rev')).toBe(5);
    expect(locateYamlPath(yaml, 'items[0].id')).toBe(2);
  });

  it('falls back to the last key when the walk loses the thread, and to null when nothing matches', () => {
    // models[9] does not exist: fall back to the first line with "revision".
    expect(locateYamlPath(MODELS, 'models[9].source.revision')).toBe(5);
    expect(locateYamlPath(MODELS, 'nothing.here')).toBeNull();
    expect(locateYamlPath(MODELS, '')).toBeNull();
    expect(locateYamlPath('', 'a.b')).toBeNull();
  });

  it('ignores comments and blank lines, and keys inside other keys\' text', () => {
    const yaml = ['# revision: no', '', 'models:', '  # revision: nope', '  - id: a', '    note: "revision: inside text"', '    revision: yes'].join('\n');
    expect(locateYamlPath(yaml, 'models[0].revision')).toBe(7);
  });
});
