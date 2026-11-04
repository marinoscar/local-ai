import { CatalogKnownModels, modelIdsFromFiles, modelIdsFromYaml } from './known-models';

const YAML = `schema_version: 1
models:
  - id: qwen3.6-35b-a3b-q4km
    display_name: "Qwen 3.6 35B A3B"
    files:
      - kind: weights
        size_bytes: 10
  - id: "qwen3.5-9b-q4km"   # quoted
    display_name: x
ark:
  - id: not-a-model
`;

describe('model ids of a models.yaml', () => {
  it('reads the ids under models: only', () => {
    expect(modelIdsFromYaml(YAML)).toEqual(['qwen3.6-35b-a3b-q4km', 'qwen3.5-9b-q4km']);
  });
  it('finds models.yaml in the stored files and tolerates anything else', () => {
    expect(modelIdsFromFiles([{ path: 'ark.yaml', content: 'x' }, { path: 'models.yaml', content: YAML }])).toHaveLength(2);
    expect(modelIdsFromFiles(null)).toEqual([]);
    expect(modelIdsFromFiles([{ path: 'models.yaml' }])).toEqual([]);
  });
});

describe('CatalogKnownModels', () => {
  it('is the approved catalog ids plus every policy id and alias', async () => {
    const prisma = {
      localaiCatalogVersion: { findFirst: jest.fn().mockResolvedValue({ files: [{ path: 'models.yaml', content: YAML }] }) },
      localaiModelPolicy: { findMany: jest.fn().mockResolvedValue([{ modelId: 'qwen3.6-35b-a3b-q4km', aliases: ['lab-coder'] }, { modelId: 'old-model', aliases: [] }]) },
    };
    const names = await new CatalogKnownModels(prisma as never).names();
    expect([...names].sort()).toEqual(['lab-coder', 'old-model', 'qwen3.5-9b-q4km', 'qwen3.6-35b-a3b-q4km']);
    expect(prisma.localaiCatalogVersion.findFirst).toHaveBeenCalledWith({ where: { status: 'approved' }, select: { files: true } });
  });

  it('is only the policies when no catalog is approved yet', async () => {
    const prisma = { localaiCatalogVersion: { findFirst: jest.fn().mockResolvedValue(null) }, localaiModelPolicy: { findMany: jest.fn().mockResolvedValue([]) } };
    expect((await new CatalogKnownModels(prisma as never).names()).size).toBe(0);
  });
});
