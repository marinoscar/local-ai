// The slice manifest (`packages/shared/slices.json`) and its resolution: what
// "drop a slice by removing one line" promises, as tests. `@app/shared` owns the
// validation (the web app reads the same function), the API owns the definitions.
import { ENABLED_SLICES, SLICE_CATALOG, SLICE_IDS, resolveSliceIds, type SliceId } from '@app/shared';

import { ALL_SLICES } from '../src/platform/slices/definitions';

const everything = [...SLICE_IDS];

describe('the slice catalog', () => {
  it('has one API definition per slice, under its own id', () => {
    expect(Object.keys(ALL_SLICES)).toEqual(everything);
    for (const id of SLICE_IDS) expect(ALL_SLICES[id].id).toBe(id);
  });

  it('only ever requires a slice that mounts earlier', () => {
    for (const id of SLICE_IDS) {
      for (const required of SLICE_CATALOG[id].requires) {
        expect(SLICE_IDS.indexOf(required)).toBeLessThan(SLICE_IDS.indexOf(id));
      }
    }
  });

  it('no longer carries the slices whose code was deleted from this app', () => {
    for (const id of ['sharing', 'exports', 'android-app']) expect(SLICE_IDS).not.toContain(id);
  });

  it('describes every slice', () => {
    for (const id of SLICE_IDS) expect(SLICE_CATALOG[id].label.length).toBeGreaterThan(10);
  });

  it('is what the app is configured with: a valid list, in mount order', () => {
    expect([...ENABLED_SLICES]).toEqual(everything.filter((id) => ENABLED_SLICES.includes(id)));
  });
});

describe('resolveSliceIds', () => {
  it('accepts all slices and returns them in mount order whatever the file order', () => {
    expect(resolveSliceIds([...everything].reverse())).toEqual(everything);
  });

  it('accepts an empty list: the core alone', () => {
    expect(resolveSliceIds([])).toEqual([]);
  });

  it('names the slice and the line to add when a requirement is missing', () => {
    const without = everything.filter((id) => id !== 'notifications');
    expect(() => resolveSliceIds(without)).toThrow(/slice "db-backup" requires "notifications"/);
    expect(() => resolveSliceIds(without)).toThrow(/Add it to "enabled", or remove "db-backup" too/);
  });

  it('refuses an unknown id and a duplicate', () => {
    expect(() => resolveSliceIds(['storege'])).toThrow(/unknown slice "storege"/);
    expect(() => resolveSliceIds(['credentials', 'credentials'])).toThrow(/listed twice/);
  });

  it.each(everything.map((id) => [id]))('lets %s stand with its requirements only', (id) => {
    const closure = new Set<SliceId>();
    const visit = (next: SliceId): void => {
      if (closure.has(next)) return;
      closure.add(next);
      SLICE_CATALOG[next].requires.forEach(visit);
    };
    visit(id as SliceId);
    expect(() => resolveSliceIds([...closure])).not.toThrow();
  });
});
