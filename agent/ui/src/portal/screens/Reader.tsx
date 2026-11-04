import { useEffect } from "react";
import { ReaderFrame } from "../components/ReaderFrame";
import { parseReaderSearch } from "../library/reader";
import { navigate } from "../router";

/** /portal/read?src=<same-origin path>&back=<portal route>. A src outside the allowlist goes home. */
export function Reader({ search }: { search: string }) {
  const req = parseReaderSearch(search);
  const ok = req !== null;
  useEffect(() => {
    if (!ok) navigate("");
  }, [ok]);
  return req ? <ReaderFrame src={req.src} back={req.back} /> : null;
}
