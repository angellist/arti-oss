import { redirect } from "next/navigation";

// /settings lands on User Groups (visible to everyone).
export default function SettingsIndex() {
  redirect("/settings/groups");
}
