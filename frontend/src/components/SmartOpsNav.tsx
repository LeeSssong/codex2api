import { NavLink } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Activity, Bell, ShieldCheck, WandSparkles, ListOrdered, KeyRound, FlaskConical, Puzzle } from "lucide-react";
export const smartOpsLinks = [
  { to: "/smart-ops/auto-config", label: "pluginManager.names.auto-config", icon: WandSparkles },
  { to: "/smart-ops/priority", label: "pluginManager.names.priority-scheduling", icon: ListOrdered },
  { to: "/smart-ops/quality", label: "smartOps.quality", icon: Activity },
  { to: "/smart-ops/alerts", label: "smartOps.alerts", icon: Bell },
  { to: "/smart-ops/tokens", label: "smartOps.tokens", icon: ShieldCheck },
  { to: "/smart-ops/credentials", label: "pluginManager.names.credential-ops", icon: KeyRound },
  { to: "/smart-ops/pelican", label: "pluginManager.names.pelican-tests", icon: FlaskConical },
  { to: "/smart-ops/plugins", label: "pluginManager.title", icon: Puzzle },
];
export default function SmartOpsNav() {
  const { t } = useTranslation();
  return (
    <div className="smart-ops-top">
      <div className="smart-ops-heading">
        <h1>{t("smartOps.title")}</h1>
      </div>
      <nav className="ops-tabs" aria-label={t("smartOps.title")}>
        {smartOpsLinks.map(({ to, label, icon: Icon }) => <NavLink key={to} to={to} className={({ isActive }) => isActive ? "active" : undefined}><Icon size={16} />{t(label)}</NavLink>)}
      </nav>
    </div>
  );
}
