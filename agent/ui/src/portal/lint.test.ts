import { ESLint } from "eslint";
import { describe, expect, it } from "vitest";

// The two lint rules that keep the Portal bilingual and CSP-clean must really fail (issue PORTAL-04).
async function lint(code: string): Promise<string[]> {
  const eslint = new ESLint({ cwd: process.cwd() });
  const [res] = await eslint.lintText(code, {
    filePath: "src/portal/screens/Fixture.tsx",
  });
  return res.messages.map((m) => m.ruleId ?? "parse");
}

describe("portal lint rules", () => {
  it("rejects a JSX text literal", async () => {
    expect(await lint("export const A = () => <p>Hello</p>;")).toContain(
      "react/jsx-no-literals",
    );
  });
  it("accepts a translated expression", async () => {
    expect(
      await lint("export const A = ({ s }: { s: string }) => <p>{s}</p>;"),
    ).toEqual([]);
  });
  it("rejects an inline style attribute", async () => {
    expect(
      await lint(
        "export const A = () => <p style={{ color: 'red' }}>{'x'}</p>;",
      ),
    ).toContain("no-restricted-syntax");
  });
});
