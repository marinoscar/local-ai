// A tiny in-memory stand-in for the Prisma delegates the Doctor checks read (`findMany`, `findFirst`, `count`), with the
// `where` operators the checks use: equality, `lt`, `in`, `not`. It exists so a unit test can plant rows and watch a
// check's verdict change; whether the real SQL means the same is proven by `test/localai/doctor.db.spec.ts`.
type Row = Record<string, unknown>;
type Where = Record<string, unknown>;

function comparable(value: unknown): unknown {
  return value instanceof Date ? value.getTime() : value;
}

function matchesField(actual: unknown, expected: unknown): boolean {
  if (expected instanceof Date || expected === null || typeof expected !== 'object') return comparable(actual) === comparable(expected);
  const ops = expected as Record<string, unknown>;
  return Object.entries(ops).every(([op, operand]) => {
    switch (op) {
      case 'lt':
        return actual !== null && actual !== undefined && (comparable(actual) as number) < (comparable(operand) as number);
      case 'in':
        return (operand as unknown[]).includes(actual);
      case 'not':
        return !matchesField(actual, operand);
      default:
        throw new Error(`FakeTable does not support the "${op}" operator`);
    }
  });
}

export class FakeTable {
  constructor(public rows: Row[] = []) {}

  private filter(where: Where = {}): Row[] {
    return this.rows.filter((row) => Object.entries(where).every(([field, expected]) => matchesField(row[field], expected)));
  }

  findMany = async (args: { where?: Where; select?: Record<string, boolean>; orderBy?: Record<string, 'asc' | 'desc'>; take?: number } = {}) => {
    let rows = this.filter(args.where);
    const [orderField, direction] = Object.entries(args.orderBy ?? {})[0] ?? [];
    if (orderField) {
      rows = [...rows].sort((a, b) => {
        const left = comparable(a[orderField]) as number | string;
        const right = comparable(b[orderField]) as number | string;
        return (left < right ? -1 : left > right ? 1 : 0) * (direction === 'desc' ? -1 : 1);
      });
    }
    if (args.take !== undefined) rows = rows.slice(0, args.take);
    const select = args.select;
    return rows.map((row) => (select ? Object.fromEntries(Object.keys(select).map((key) => [key, row[key]])) : { ...row }));
  };

  findFirst = async (args: { where?: Where; select?: Record<string, boolean> } = {}) => (await this.findMany({ ...args, take: 1 }))[0] ?? null;

  count = async (args: { where?: Where } = {}) => this.filter(args.where).length;
}

/** The delegates the seven checks touch. */
export function fakePrisma(tables: { localaiNode?: Row[]; localaiCommand?: Row[]; localaiCatalogVersion?: Row[]; auditEvent?: Row[] } = {}) {
  return {
    localaiNode: new FakeTable(tables.localaiNode),
    localaiCommand: new FakeTable(tables.localaiCommand),
    localaiCatalogVersion: new FakeTable(tables.localaiCatalogVersion),
    auditEvent: new FakeTable(tables.auditEvent),
  };
}
