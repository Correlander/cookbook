import App from "../App.tsx";
import { AppSessionProvider } from "./AppSession.tsx";

export default function AppRoot({
  pathname,
}: {
  pathname: string;
}) {
  return (
    <AppSessionProvider>
      <App pathname={pathname} />
    </AppSessionProvider>
  );
}
