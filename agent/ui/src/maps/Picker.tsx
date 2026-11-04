import { mapName, type MapEntry } from "./catalog";
import { t, useLang } from "./lang";

/** Picker lists every installed map; only one is shown at a time (companion-services.md 4.4.3). */
export function Picker({
  maps,
  currentId,
  onPick,
}: {
  maps: MapEntry[];
  currentId: string;
  onPick: (id: string) => void;
}) {
  const lang = useLang();
  return (
    <label className="picker">
      <span className="picker-label">{t("picker.label")}</span>
      <select
        className="field"
        value={currentId}
        onChange={(e) => onPick(e.target.value)}
      >
        {maps.map((m) => (
          <option key={m.id} value={m.id}>
            {mapName(m, lang)}
          </option>
        ))}
      </select>
    </label>
  );
}
