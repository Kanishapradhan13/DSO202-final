"use client";

import { useState } from "react";
import { Alert, Button, cx, Field, Modal, useToast } from "./ui";
import { api, errMsg } from "@/lib/api";

export function OpenAccountModal({ open, onClose, onDone }: { open: boolean; onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const [type, setType] = useState("SAVINGS");
  const [nickname, setNickname] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  return (
    <Modal open={open} onClose={onClose} title="Open a new account">
      <form className="space-y-4" onSubmit={async (e) => {
        e.preventDefault(); setBusy(true); setErr("");
        try {
          const a = await api("/v1/accounts", { body: { type, nickname } });
          toast(`Account ${a.account_number} opened`);
          onDone(); onClose();
        } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
      }}>
        {err && <Alert>{err}</Alert>}
        <div className="grid grid-cols-2 gap-3">
          {["SAVINGS", "CURRENT"].map((v) => (
            <button type="button" key={v} onClick={() => setType(v)}
              className={cx("rounded-xl border p-3 text-left text-sm font-medium", type === v ? "border-brand-500 bg-brand-50 dark:bg-brand-500/10" : "border-base")}>
              {v === "SAVINGS" ? "Savings" : "Current"}
            </button>
          ))}
        </div>
        <Field label="Nickname"><input className="input" value={nickname} onChange={(e) => setNickname(e.target.value)} placeholder="Optional" /></Field>
        <Button className="w-full" loading={busy}>Open account</Button>
      </form>
    </Modal>
  );
}
