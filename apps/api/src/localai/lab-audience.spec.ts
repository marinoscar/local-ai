import { LabAudience } from './lab-audience';

describe('LabAudience', () => {
  it('asks for active users holding the permission through a system role OR an active membership role', async () => {
    const findMany = jest.fn(async () => [{ id: 'u1' }, { id: 'u2' }]);
    const grants = { rolePermissions: { some: { permission: { name: 'lab:operate' } } } };
    const holders = await new LabAudience({ user: { findMany } } as never).holdersOf('lab:operate');
    expect(holders).toEqual(['u1', 'u2']);
    expect(findMany).toHaveBeenCalledWith({
      where: { isActive: true, OR: [{ userRoles: { some: { role: grants } } }, { memberships: { some: { status: 'active', role: grants } } }] },
      select: { id: true },
    });
  });

  it('a failed lookup is an empty audience, not an error (the alert is still raised)', async () => {
    const findMany = jest.fn(async () => {
      throw new Error('db down');
    });
    await expect(new LabAudience({ user: { findMany } } as never).holdersOf('lab:operate')).resolves.toEqual([]);
  });
});
