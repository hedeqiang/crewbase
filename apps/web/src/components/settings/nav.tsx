import { Bell, Boxes, FileText, Globe, Library, Cpu, Keyboard, KeyRound, ListChecks, MessagesSquare, MousePointerClick, Puzzle, Server, Settings as Gear, Shapes, User, Webhook, Workflow, Users } from "lucide-react";
import type { ReactNode } from "react";
import type { Key } from "../../i18n";
import type { DeskPage, SettingsSection } from "../../store";

// The settings sections, apart from the pages so the palette can list them
// without loading every page.
export const NAV: { group: Key; items: { id: SettingsSection; icon: ReactNode }[] }[] = [
  {
    group: "set.group.you",
    items: [
      { id: "general", icon: <Gear size={16} /> },
      { id: "profile", icon: <User size={16} /> },
      { id: "vault", icon: <KeyRound size={16} /> },
      { id: "notify", icon: <Bell size={16} /> },
      { id: "keys", icon: <Keyboard size={16} /> },
    ],
  },
  {
    group: "set.group.ability",
    items: [
      { id: "models", icon: <Cpu size={16} /> },
      { id: "market", icon: <Puzzle size={16} /> },
      { id: "computer", icon: <MousePointerClick size={16} /> },
      { id: "approvals", icon: <ListChecks size={16} /> },
    ],
  },
  {
    group: "set.group.connect",
    items: [
      { id: "im", icon: <MessagesSquare size={16} /> },
      { id: "webhooks", icon: <Webhook size={16} /> },
      { id: "proxy", icon: <Globe size={16} /> },
      // shown once Open Agent Docs / Open Artifact is set up (Settings.tsx)
      { id: "docs", icon: <FileText size={16} /> },
      { id: "artifacts", icon: <Shapes size={16} /> },
    ],
  },
  {
    group: "set.group.device",
    items: [
      { id: "teams", icon: <Boxes size={16} /> },
      { id: "team", icon: <Users size={16} /> },
      { id: "hosts", icon: <Server size={16} /> },
    ],
  },
];

/** The 工作台 at the foot of the conversation list: what is used every day
 *  (approvals stay in settings; the 需要你 pill brings what waits). */
export const DESK: { id: DeskPage; icon: ReactNode; title: Key }[] = [
  { id: "library", icon: <Library size={16} />, title: "lib.title" },
  { id: "auto", icon: <Workflow size={16} />, title: "set.auto" },
];

export const SECTION_ICONS: Record<SettingsSection, ReactNode> = Object.fromEntries(NAV.flatMap((g) => g.items.map((i) => [i.id, i.icon]))) as any;
export const SECTIONS: SettingsSection[] = NAV.flatMap((g) => g.items.map((i) => i.id));
