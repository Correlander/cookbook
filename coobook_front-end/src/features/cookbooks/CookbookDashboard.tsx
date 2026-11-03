import type { ReactNode } from "react";
import type { AuthenticatedUser } from "../../apiModels";
import CookbookCollection from "./CookbookCollection.tsx";

interface CookbookDashboardProps {
  user: AuthenticatedUser | null;
  children?: ReactNode;
  surface?: "home" | "page";
}

export default function CookbookDashboard({
  user,
  children,
  surface = "home",
}: CookbookDashboardProps) {
  return (
    <CookbookCollection
      mode="accessible"
      surface={surface}
      user={user}
    >
      {children}
    </CookbookCollection>
  );
}
