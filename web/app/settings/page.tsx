import { redirect } from "next/navigation";

// /settings lands on the first entry of the sub-nav, which everyone can see.
export default function SettingsIndex() {
  redirect("/settings/keys");
}
