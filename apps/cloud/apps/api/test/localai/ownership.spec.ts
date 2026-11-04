// Every Local AI model is classified (docs/specs/cloud-app.md section 3.5): the ownership registry has an entry
// for each `Localai*` model of the composed schema, the user-owned registry has one for each that points at a
// user, and none of the tables carries an `org_id` (the lab is one deployment-wide control plane).
import { modelOwnershipRegistry, userOwnedModelRegistry } from '@marinoscar/platform-api/core';
import { readSchemaDatamodel } from '@marinoscar/platform-api/testing';
import { join } from 'node:path';

// Fills the registries: the platform's entries, then the app's.
import '../../src/platform/registrations';

const localai = readSchemaDatamodel(join(__dirname, '..', '..', 'prisma', 'schema')).filter((model) => model.name.startsWith('Localai'));

describe('Local AI model ownership', () => {
  it('finds the nine Local AI models in the composed schema', () => {
    expect(localai.map((model) => model.name).sort()).toEqual([
      'LocalaiAlert',
      'LocalaiCatalogVersion',
      'LocalaiCommand',
      'LocalaiGatewayKey',
      'LocalaiGatewayUsage',
      'LocalaiGatewayUsageDaily',
      'LocalaiModelPolicy',
      'LocalaiNode',
      'LocalaiNodeModel',
    ]);
  });

  // The gateway key is the one model a person owns (gateway.md section 3.6); the lab and the other gateway tables are deployment-wide.
  it.each(localai.filter((model) => model.name !== 'LocalaiGatewayKey').map((model) => [model.name]))(
    '%s is classified as a deployment-wide (system) model',
    (name) => {
      expect(modelOwnershipRegistry.get(name)).toMatchObject({ kind: 'system' });
    },
  );

  it('classifies the gateway key as user-owned: deleted with its user, exported, never with the hash', () => {
    expect(modelOwnershipRegistry.get('LocalaiGatewayKey')).toMatchObject({ kind: 'user' });
    expect(userOwnedModelRegistry.get('LocalaiGatewayKey')).toMatchObject({
      ownerField: 'userId',
      purge: 'delete',
      export: 'include',
    });
    expect(userOwnedModelRegistry.get('LocalaiGatewayKey')?.exportOmit).toContain('tokenHash');
  });

  it('carries no org_id column on any Local AI table', () => {
    for (const model of localai) expect(model.fields.map((field) => field.name)).not.toContain('orgId');
  });

  it('registers every foreign key to User as an actor field that is detached on purge', () => {
    for (const model of localai.filter((candidate) => candidate.name !== 'LocalaiGatewayKey')) {
      const userKeys = model.fields.filter((field) => field.type === 'User' && field.relation).flatMap((field) => field.relation?.fields ?? []);
      if (userKeys.length === 0) continue;
      const policy = userOwnedModelRegistry.get(model.name);
      expect(policy).toBeDefined();
      expect([...(policy?.actorFields ?? [])].sort()).toEqual([...userKeys].sort());
      expect(policy).toMatchObject({ purge: 'detach', export: 'exclude' });
    }
  });
});
