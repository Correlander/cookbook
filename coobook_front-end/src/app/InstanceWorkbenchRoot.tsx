import { AppSessionProvider } from "./AppSession.tsx";
import InstanceWorkbench from "../InstanceWorkbench.tsx";

export default function InstanceWorkbenchRoot() {
  return (
    <AppSessionProvider>
      <InstanceWorkbench />
    </AppSessionProvider>
  );
}
