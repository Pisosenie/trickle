import { toaster } from "@decky/api";
import { errorText } from "./format";

export function toastError(title: string, err: unknown) {
  toaster.toast({ title, body: errorText(err) });
}
