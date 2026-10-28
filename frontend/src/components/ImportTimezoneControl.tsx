import { useTranslation } from "react-i18next";

export default function ImportTimezoneControl({
  checked,
  onChange,
  disabled = false,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <label className="flex cursor-pointer items-start gap-2 pt-1 text-xs text-muted-foreground">
      <input
        type="checkbox"
        className="mt-0.5 size-3.5"
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
      />
      <span>
        {t("accounts.importNewYorkTimezone")}
        <span className="mt-1 block">
          {t("accounts.importNewYorkTimezoneHint")}
        </span>
      </span>
    </label>
  );
}
