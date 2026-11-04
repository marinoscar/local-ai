// Who approved a version, as a display name, for the Catalog page header ("approved by Person A", cloud-app.md 3.11).
//
// Only the display name leaves the API (never an email or an id beyond the one the row already exposes), and only to
// holders of `lab:read`, who already see every node. A user without a display name, or one deleted since
// (`approved_by_id` is set null on delete), reads as `null` and the page says "an administrator".

/** The Prisma `include` that loads the approver's display name next to a version row. */
export const WITH_APPROVER = { approvedBy: { select: { displayName: true } } } as const;

export interface WithApprover {
  approvedBy?: { displayName: string | null } | null;
}

/** The approver's display name when the row was loaded `WITH_APPROVER` and the user has one, else null. */
export function approverNameOf(row: WithApprover): string | null {
  const name = row.approvedBy?.displayName?.trim();
  return name ? name.slice(0, 120) : null;
}
