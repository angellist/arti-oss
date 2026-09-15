"use client";

import { useEffect, useState } from "react";
import {
  getNotificationSettings,
  setDeploymentNotifications,
  setNotificationSetting,
} from "@/lib/arti";
import type { NotificationCategory, NotificationSettings } from "@/lib/types";

function Row({
  category,
  busy,
  onChange,
}: {
  category: NotificationCategory;
  busy: boolean;
  onChange: (enabled: boolean) => void;
}) {
  return (
    <label className="flex cursor-pointer items-start gap-2 py-2">
      <input
        type="checkbox"
        checked={category.enabled}
        disabled={busy}
        onChange={(e) => onChange(e.target.checked)}
        className="mt-0.5 h-3.5 w-3.5 accent-neutral-900"
      />
      <span>
        <span className="block text-[13px] text-neutral-800">{category.label}</span>
        {category.description && (
          <span className="block text-[11px] text-neutral-500">{category.description}</span>
        )}
      </span>
    </label>
  );
}

export default function NotificationSettingsPage() {
  const [settings, setSettings] = useState<NotificationSettings | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState("");
  const [busyKey, setBusyKey] = useState("");

  const load = async () => {
    try {
      setSettings(await getNotificationSettings());
      setErr("");
    } catch (ex) {
      setErr(ex instanceof Error ? ex.message : "Failed to load notification settings.");
    }
    setLoading(false);
  };

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { void load(); }, []);

  const flip = async (key: string, enabled: boolean) => {
    setBusyKey(key);
    setErr("");
    try {
      setSettings(await setNotificationSetting(key, enabled));
    } catch (ex) {
      setErr(ex instanceof Error ? ex.message : String(ex));
    } finally {
      setBusyKey("");
    }
  };

  const flipDeployment = async (enabled: boolean) => {
    setBusyKey("deployment");
    setErr("");
    try {
      await setDeploymentNotifications(enabled);
      await load();
    } catch (ex) {
      setErr(ex instanceof Error ? ex.message : String(ex));
    } finally {
      setBusyKey("");
    }
  };

  return (
    <div className="px-6 pt-4">
      <h2 className="text-[14px] font-semibold text-neutral-900">Notifications</h2>
      <p className="mt-0.5 max-w-2xl text-[12px] text-neutral-500">
        Slack messages arti sends <strong>you</strong>. These are your own settings and
        affect nobody else. Everything they cover stays visible in the web app either
        way — a key&apos;s sources under API Keys, and comments on the document itself.
      </p>

      {loading ? (
        <p className="mt-4 text-[12px] text-neutral-400">Loading…</p>
      ) : err && !settings ? (
        <p className="mt-4 text-[12px] text-red-600">{err}</p>
      ) : settings ? (
        <div className="mt-4 max-w-lg">
          {!settings.slack_configured && (
            <p className="mb-3 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-[12px] text-amber-800">
              This deployment has no Slack bot token, so nothing is sent whatever you
              choose here.
            </p>
          )}
          {settings.slack_configured && !settings.deployment_enabled && (
            <p className="mb-3 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-[12px] text-amber-800">
              An admin has switched Slack notifications off for everyone, so your
              choices below take effect only once that is switched back on.
            </p>
          )}

          <div className="divide-y divide-neutral-100">
            {settings.categories.map((c) => (
              <Row key={c.key} category={c} busy={busyKey === c.key} onChange={(v) => flip(c.key, v)} />
            ))}
          </div>

          {err && <p className="mt-3 text-[12px] text-red-600">{err}</p>}

          {settings.can_manage_deployment && (
            <div className="mt-8 border-t border-neutral-200 pt-4">
              <h3 className="text-[12px] font-semibold text-neutral-700">
                Whole deployment (admin)
              </h3>
              <p className="mt-0.5 text-[11px] text-neutral-500">
                One switch that stops every Slack message arti sends, to everyone. For a
                notification that is firing wrongly — it takes effect within seconds and
                needs no deploy.
              </p>
              <label className="mt-2 flex cursor-pointer items-center gap-2">
                <input
                  type="checkbox"
                  checked={settings.deployment_enabled}
                  disabled={busyKey === "deployment"}
                  onChange={(e) => flipDeployment(e.target.checked)}
                  className="h-3.5 w-3.5 accent-neutral-900"
                />
                <span className="text-[13px] text-neutral-800">
                  Slack notifications enabled for everyone
                </span>
              </label>
            </div>
          )}
        </div>
      ) : null}
    </div>
  );
}
