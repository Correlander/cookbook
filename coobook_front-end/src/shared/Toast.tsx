import { useEffect, useRef } from "react";

export default function Toast({ message, actorID, onDismiss, duration = 2400, revision = 0 }: { message: string; actorID: string; onDismiss: () => void; duration?: number; revision?: number }) {
  const dismissRef = useRef(onDismiss);
  dismissRef.current = onDismiss;
  useEffect(() => {
    if (!message || !actorID) return;
    const timer = window.setTimeout(() => dismissRef.current(), duration);
    return () => window.clearTimeout(timer);
  }, [actorID, duration, message, revision]);
  if (!message || !actorID) return null;
  return <p className="success-toast" role="status" aria-live="polite" aria-atomic="true">{message}</p>;
}
