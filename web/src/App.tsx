// App maps the addresses to the pages. The working screens share the rail;
// the introduction and the ways in stand on their own.

import { Route, Routes } from "react-router";

import { useLang } from "./lib/i18n";

import { Account } from "./pages/Account";
import { Login, Register } from "./pages/Auth";
import { Job } from "./pages/Job";
import { Jobs } from "./pages/Jobs";
import { Landing } from "./pages/Landing";
import { NotFound } from "./pages/NotFound";
import { Repo } from "./pages/Repo";
import { Repos } from "./pages/Repos";
import { Users } from "./pages/Users";
import { DialogHost } from "./ui/DialogHost";
import { Shell } from "./ui/Shell";

export function App() {
  // A change of language re-renders every page, so their words follow it.
  useLang();
  return (
    <>
      <Routes>
        <Route index element={<Landing />} />
        <Route path="login" element={<Login />} />
        <Route path="register" element={<Register />} />
        <Route element={<Shell />}>
          <Route path="jobs" element={<Jobs />} />
          <Route path="jobs/:id" element={<Job />} />
          <Route path="repos" element={<Repos />} />
          <Route path="repos/*" element={<Repo />} />
          <Route path="account" element={<Account />} />
          <Route path="users" element={<Users />} />
          <Route path="*" element={<NotFound />} />
        </Route>
      </Routes>
      <DialogHost />
    </>
  );
}
