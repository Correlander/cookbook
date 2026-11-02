import { useEffect, useRef } from "react";
import type { RefObject } from "react";

const focusableSelector =
  'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';

export default function useModalFocus(
  dialogRef: RefObject<HTMLElement | null>,
  onClose?: () => void,
  active = true,
) {
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!active || !dialog) return undefined;
    const activeDialog = dialog;

    const previousFocus = document.activeElement;
    const controls = () =>
      [...activeDialog.querySelectorAll<HTMLElement>(focusableSelector)].filter(
        (control) =>
          !control.closest('[hidden],[aria-hidden="true"]') &&
          control.getAttribute("type") !== "hidden",
      );
    const isTopmost = () =>
      [...document.querySelectorAll('[role="dialog"][aria-modal="true"]')].at(
        -1,
      ) === activeDialog;
    const first = controls()[0];
    (first || activeDialog).focus();

    function keepFocusInside(event: KeyboardEvent) {
      if (!isTopmost()) return;
      if (event.key === "Escape") {
        event.preventDefault();
        closeRef.current?.();
        return;
      }
      if (event.key !== "Tab") return;

      const items = controls();
      if (!items.length) {
        event.preventDefault();
        activeDialog.focus();
        return;
      }
      const firstControl = items[0];
      const lastControl = items[items.length - 1];
      if (
        event.shiftKey &&
        (document.activeElement === firstControl ||
          !activeDialog.contains(document.activeElement))
      ) {
        event.preventDefault();
        lastControl.focus();
      } else if (
        !event.shiftKey &&
        (document.activeElement === lastControl ||
          !activeDialog.contains(document.activeElement))
      ) {
        event.preventDefault();
        firstControl.focus();
      }
    }

    document.addEventListener("keydown", keepFocusInside);
    return () => {
      document.removeEventListener("keydown", keepFocusInside);
      if (previousFocus instanceof HTMLElement && previousFocus.isConnected)
        previousFocus.focus();
    };
  }, [active, dialogRef]);
}
