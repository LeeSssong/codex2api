import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import ChannelLogo from "./ChannelLogo";
import { SegmentedPillGroup } from "./ui/segmented-pill-group";
import { cn } from "@/lib/utils";
import { useVisibleChannels } from "../visibleChannels";

// Dashboard and usage share a persisted provider filter.
export type UsageChannel = "" | "codex" | "grok" | "antigravity" | "claude";

const USAGE_CHANNEL_KEY = "codex2api:usage:channel";

export function useUsageChannel(): [UsageChannel, (next: UsageChannel) => void] {
  const [channel, setChannel] = useState<UsageChannel>(() => {
    try {
      const raw = window.localStorage.getItem(USAGE_CHANNEL_KEY);
      if (raw === "codex" || raw === "grok" || raw === "antigravity" || raw === "claude") return raw;
    } catch {
      // ignore
    }
    return "";
  });
  useEffect(() => {
    try {
      window.localStorage.setItem(USAGE_CHANNEL_KEY, channel);
    } catch {
      // ignore
    }
  }, [channel]);
  return [channel, setChannel];
}

export default function ChannelFilter({
  value,
  onChange,
  className,
}: {
  value: UsageChannel;
  onChange: (next: UsageChannel) => void;
  className?: string;
}) {
  const { t } = useTranslation();
  const { isChannelVisible } = useVisibleChannels();
  const allOptions: Array<{
    key: UsageChannel;
    label: string;
    logo?: "codex" | "grok" | "antigravity" | "claude";
  }> = [
    { key: "", label: t("usage.channelAll") },
    { key: "codex", label: "Codex", logo: "codex" },
    { key: "grok", label: "Grok", logo: "grok" },
    { key: "antigravity", label: "Antigravity", logo: "antigravity" },
    { key: "claude", label: "Claude", logo: "claude" },
  ];
  const options = allOptions.filter((o) => !o.logo || isChannelVisible(o.logo));
  // Hidden providers cannot remain selected by a saved filter.
  const hiddenSelection = value !== "" && !options.some((o) => o.key === value);
  useEffect(() => {
    if (hiddenSelection) onChange("");
  }, [hiddenSelection, onChange]);
  return (
    <div className={cn("max-w-full overflow-x-auto rounded-lg", className)}>
      <SegmentedPillGroup
        label={t("usage.channelFilter")}
        value={value}
        onChange={onChange}
        className="min-w-max rounded-lg [&_button]:min-h-9 [&_button]:flex-none [&_button]:px-3"
        options={options.map(({ key, label, logo }) => ({
          value: key, label,
          icon: key === "claude" ? <ChannelLogo channel="claude" size={16} /> : logo ? <ChannelLogo channel={logo} size={16} /> : undefined,
        }))}
      />
    </div>
  );
}
