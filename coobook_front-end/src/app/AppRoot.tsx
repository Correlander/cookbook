import App from "../App.tsx";
import { AppSessionProvider } from "./AppSession.tsx";

type AppRoute = "home" | "account-security" | "admin-settings";

export default function AppRoot({
  route,
  pathname,
}: {
  route: AppRoute;
  pathname: string;
}) {
  return (
    <AppSessionProvider>
      <App route={route} pathname={pathname} />
    </AppSessionProvider>
  );
}
