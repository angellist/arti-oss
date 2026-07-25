import { redirect } from "next/navigation";

// /groups moved under Settings. Redirect old bookmarks/links.
export default function GroupsRedirect() {
  redirect("/settings/groups");
}
