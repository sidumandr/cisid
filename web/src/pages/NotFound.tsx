import { Link } from "react-router";

import { t } from "../lib/i18n";
import { useTitle } from "../lib/useTitle";
import { Header, Note } from "../ui/parts";

export function NotFound() {
  useTitle(t("Not found"));
  return (
    <>
      <Header title={t("Nothing here")} lede={t("This address is not a page of cisid.")} />
      <Note action={<Link className="text-action" to="/jobs">{t("Open the jobs")}</Link>}>{t("Check the address, or start from the jobs.")}</Note>
    </>
  );
}
