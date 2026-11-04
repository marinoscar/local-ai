import { useState } from "react";
import { t } from "../i18n/i18n";
import { cardTarget, manualsDegraded, orderItems } from "../manuals/manuals";
import { readerPath } from "../library/reader";
import { navigate, navigatePath } from "../router";
import type { PortalStatus } from "../status";
import "../styles/manuals.css";

const BACK = "/portal/manuals";

/** Manuals (button 6): one big button per rendered runbook, the Survival card, and the full list. */
export function Manuals({ status }: { status: PortalStatus | null }) {
  const cap = status?.capabilities.manuals;
  const items = orderItems(cap?.items ?? []);
  const [busy, setBusy] = useState(false);

  async function openCard() {
    setBusy(true);
    const target = await cardTarget(status?.node_id);
    setBusy(false);
    navigatePath(readerPath(target, BACK));
  }

  return (
    <section className="screen" aria-labelledby="screen-title">
      <h1 id="screen-title" className="screen-title">
        {t("manuals.title")}
      </h1>
      {manualsDegraded(cap) ? (
        <p className="manuals-note" role="note">
          {t("manuals.degraded")}
        </p>
      ) : null}
      <ul className="manual-grid">
        {items.map((m) => (
          <li key={m.id}>
            <button
              type="button"
              className="btn manual-btn"
              onClick={() => navigatePath(readerPath(m.url, BACK))}
            >
              {m.title}
            </button>
          </li>
        ))}
        <li>
          <button
            type="button"
            className="btn manual-btn manual-card"
            disabled={busy}
            onClick={() => void openCard()}
          >
            {t("manuals.card")}
          </button>
        </li>
        <li>
          <button
            type="button"
            className="btn manual-btn"
            onClick={() => navigatePath(readerPath("/docs/", BACK))}
          >
            {t("manuals.all")}
          </button>
        </li>
      </ul>
      <div className="actions">
        <button type="button" className="btn" onClick={() => navigate("")}>
          {t("common.home")}
        </button>
      </div>
    </section>
  );
}
