import { redirect } from "next/navigation";

// /roles moved under Settings. Redirect old bookmarks/links.
export default function RolesRedirect() {
  redirect("/settings/roles");
}
