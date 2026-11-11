import { toaster } from "@decky/api";
import { ButtonItem, Field, PanelSection, PanelSectionRow, ToggleField } from "@decky/ui";
import { useEffect, useState } from "react";
import { api } from "../api";
import { errorText } from "../format";
import { toastError } from "../toast";
import type { PhoneInfo } from "../types";

interface Props {
  enabled: boolean;
  onToggle: (enabled: boolean) => Promise<void>;
}

export function PhoneSection({ enabled, onToggle }: Props) {
  const [info, setInfo] = useState<PhoneInfo>();

  const load = () => {
    api.phone().then(setInfo).catch((err) => setInfo({ error: errorText(err) }));
  };

  useEffect(load, []);

  const toggle = async (next: boolean) => {
    await onToggle(next);
    load();
  };

  const newCode = async () => {
    try {
      setInfo(await api.newPhoneCode());
      toaster.toast({ title: "New code", body: "Phones with the old code are signed out." });
    } catch (err) {
      toastError("Trickle", err);
    }
  };

  return (
    <PanelSection title="Add from phone">
      <PanelSectionRow>
        <ToggleField
          label="Phone page"
          description="Scan the code with a phone on the same Wi-Fi and paste a link there."
          checked={enabled}
          onChange={toggle}
        />
      </PanelSectionRow>
      {info?.error && (
        <PanelSectionRow>
          <Field description={info.error} bottomSeparator="none" />
        </PanelSectionRow>
      )}
      {info?.qr_png_base64 && (
        <>
          <PanelSectionRow>
            <div style={{ display: "flex", justifyContent: "center", padding: 8 }}>
              <img
                src={`data:image/png;base64,${info.qr_png_base64}`}
                width={200}
                height={200}
                style={{ background: "#fff", borderRadius: 8 }}
              />
            </div>
          </PanelSectionRow>
          <PanelSectionRow>
            <ButtonItem layout="below" onClick={newCode}>
              New code
            </ButtonItem>
          </PanelSectionRow>
        </>
      )}
    </PanelSection>
  );
}
