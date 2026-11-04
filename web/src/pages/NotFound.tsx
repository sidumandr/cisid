import { Link } from "react-router";

import { useTitle } from "../lib/useTitle";
import { Header, Note } from "../ui/parts";

export function NotFound() {
  useTitle("Not found");
  return (
    <>
      <Header title="Nothing here" lede="This address is not a page of cisid." />
      <Note action={<Link className="text-action" to="/jobs">Open the jobs</Link>}>Check the address, or start from the jobs.</Note>
    </>
  );
}
