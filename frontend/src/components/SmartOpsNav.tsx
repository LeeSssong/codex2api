import { NavLink } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Activity, Bell, ShieldCheck } from "lucide-react";
export default function SmartOpsNav() {
  const { t } = useTranslation();
  return (
    <div className="smart-ops-top">
      <div className="smart-ops-heading">
        <h1>{t("smartOps.title")}</h1>
        <p>{t("smartOps.description")}</p>
      </div>
      <nav className="ops-tabs" aria-label={t("smartOps.title")}>
        <NavLink
          to="/smart-ops/quality"
          className={({ isActive }) => (isActive ? "active" : undefined)}
        >
          <Activity size={16} />
          {t("smartOps.quality")}
        </NavLink>
        <NavLink
          to="/smart-ops/alerts"
          className={({ isActive }) => (isActive ? "active" : undefined)}
        >
          <Bell size={16} />
          {t("smartOps.alerts")}
        </NavLink>
        <NavLink
          to="/smart-ops/tokens"
          className={({ isActive }) => (isActive ? "active" : undefined)}
        >
          <ShieldCheck size={16} />
          {t("smartOps.tokens")}
        </NavLink>
      </nav>
    </div>
  );
}
