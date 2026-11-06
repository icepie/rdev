import { useEffect, useRef, useState } from "react";
import { getDevicePassword } from "../lib/api";

/** Credentials and automatic submission belong to one open device prompt. */
export function useDevicePassword({ device, open = true, error, onSubmit, clearOnError = false }: {
  device: string;
  open?: boolean;
  error: string;
  onSubmit: (password: string) => void;
  clearOnError?: boolean;
}) {
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const onSubmitRef = useRef(onSubmit);
  onSubmitRef.current = onSubmit;

  const cancelAutomatic = () => {
    clearTimeout(timerRef.current);
    timerRef.current = undefined;
  };

  useEffect(() => {
    cancelAutomatic();
    busyRef.current = false;
    setBusy(false);
    const remembered = open ? getDevicePassword(device) : "";
    setValue(remembered);
    if (remembered) {
      timerRef.current = setTimeout(() => {
        timerRef.current = undefined;
        busyRef.current = true;
        setBusy(true);
        onSubmitRef.current(remembered);
      }, 50);
    }
    return cancelAutomatic;
  }, [device, open]);

  useEffect(() => {
    if (!error) return;
    cancelAutomatic();
    busyRef.current = false;
    setBusy(false);
    if (clearOnError) setValue("");
  }, [error, device, open, clearOnError]);

  return {
    value,
    busy,
    setValue(password: string) {
      cancelAutomatic();
      setValue(password);
    },
    submit() {
      if (!open || !value || busyRef.current) return;
      cancelAutomatic();
      busyRef.current = true;
      setBusy(true);
      onSubmitRef.current(value);
    },
  };
}
